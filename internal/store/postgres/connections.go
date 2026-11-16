package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/myronsi/messenger-back/internal/store/postgres/sqlcdb"
)

const MaxConnections = 15

var ErrConnectionLimit = errors.New("postgres: connection limit reached")

type Connection struct {
	ID          int64
	UserID      int64
	Provider    string
	ExternalID  string
	DisplayName string
	ProfileURL  *string
	Position    int
	VerifiedAt  time.Time
}

type ConnectedAccount struct {
	Provider    string
	ExternalID  string
	DisplayName string
	ProfileURL  *string
}

type ConnectionRepository interface {
	List(ctx context.Context, userID int64) ([]Connection, error)
	Connect(ctx context.Context, userID int64, a ConnectedAccount) (Connection, error)
	Delete(ctx context.Context, userID, id int64) error
	Reorder(ctx context.Context, userID int64, ids []int64) ([]Connection, error)
}

type connectionRepo struct{ s *Store }

var _ ConnectionRepository = connectionRepo{}

func (s *Store) Connections() ConnectionRepository { return connectionRepo{s} }

func connectionFrom(c sqlcdb.UserConnection) Connection {
	return Connection{
		ID: c.ID, UserID: c.UserID, Provider: c.Provider, ExternalID: c.ExternalID, DisplayName: c.DisplayName,
		ProfileURL: c.ProfileUrl, Position: int(c.Position), VerifiedAt: c.VerifiedAt,
	}
}

func (r connectionRepo) List(ctx context.Context, userID int64) ([]Connection, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	rows, err := r.s.q.ListUserConnections(ctx, userID)
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]Connection, len(rows))
	for i, row := range rows {
		out[i] = connectionFrom(row)
	}
	return out, nil
}

func (r connectionRepo) Connect(ctx context.Context, userID int64, a ConnectedAccount) (Connection, error) {
	var out Connection
	err := r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		if _, err := q.LockUser(ctx, userID); err != nil {
			return err
		}
		existing, err := q.FindUserConnection(ctx, sqlcdb.FindUserConnectionParams{UserID: userID, Provider: a.Provider, ExternalID: a.ExternalID})
		if err == nil {
			row, err := q.RefreshUserConnection(ctx, sqlcdb.RefreshUserConnectionParams{ID: existing.ID, DisplayName: a.DisplayName, ProfileUrl: a.ProfileURL})
			out = connectionFrom(row)
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		n, err := q.CountUserConnections(ctx, userID)
		if err != nil {
			return err
		}
		if n >= MaxConnections {
			return ErrConnectionLimit
		}
		row, err := q.InsertUserConnection(ctx, sqlcdb.InsertUserConnectionParams{
			UserID: userID, Provider: a.Provider, ExternalID: a.ExternalID, DisplayName: a.DisplayName, ProfileUrl: a.ProfileURL,
		})
		out = connectionFrom(row)
		return err
	})
	if errors.Is(err, ErrConnectionLimit) {
		return Connection{}, err
	}
	if err != nil {
		return Connection{}, mapError(err)
	}
	return out, nil
}

func (r connectionRepo) Delete(ctx context.Context, userID, id int64) error {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	n, err := r.s.q.DeleteUserConnection(ctx, sqlcdb.DeleteUserConnectionParams{ID: id, UserID: userID})
	return affected(n, err)
}

func (r connectionRepo) Reorder(ctx context.Context, userID int64, ids []int64) ([]Connection, error) {
	var out []Connection
	err := r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		if _, err := q.LockUser(ctx, userID); err != nil {
			return err
		}
		rows, err := q.ListUserConnections(ctx, userID)
		if err != nil {
			return err
		}
		if len(rows) != len(ids) {
			return ErrInvalid
		}
		known := make(map[int64]bool, len(rows))
		for _, row := range rows {
			known[row.ID] = true
		}
		for i, id := range ids {
			if !known[id] {
				return ErrInvalid
			}
			delete(known, id)
			if _, err := q.SetUserConnectionPosition(ctx, sqlcdb.SetUserConnectionPositionParams{ID: id, UserID: userID, Position: int32(i)}); err != nil { //nolint:gosec // at most MaxConnections
				return err
			}
		}
		rows, err = q.ListUserConnections(ctx, userID)
		if err != nil {
			return err
		}
		out = make([]Connection, len(rows))
		for i, row := range rows {
			out[i] = connectionFrom(row)
		}
		return nil
	})
	if errors.Is(err, ErrInvalid) {
		return nil, err
	}
	if err != nil {
		return nil, mapError(err)
	}
	return out, nil
}
