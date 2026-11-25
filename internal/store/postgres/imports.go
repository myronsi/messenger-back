package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/myronsi/messenger-back/internal/store/postgres/sqlcdb"
)

const (
	ImportRunning  = "running"
	ImportDone     = "done"
	ImportRemoving = "removing"

	ImportSenderMe         = "me"
	ImportSenderUnassigned = "unassigned"
	ImportSenderPending    = "pending"
	ImportSenderConfirmed  = "confirmed"
	ImportSenderDeclined   = "declined"
)

const codeExclusionViolation = "23P01"

var (
	ErrImportRunning = errors.New("an import of this chat is running")
	ErrWindowTaken   = errors.New("an import id window is taken")
)

type ChatImport struct {
	ID             int64
	ChatID         int64
	ImportedBy     *int64
	Source         string
	State          string
	MessageCount   int
	FileCount      int
	FirstAt        time.Time
	LastAt         time.Time
	LowerBound     int64
	FirstMessageID int64
	LastMessageID  int64
	NextIndex      int
	CreatedAt      time.Time
	FinishedAt     *time.Time
}

type ImportSender struct {
	ImportID        int64
	Key             string
	Name            string
	MessageCount    int
	AssignedUserID  *int64
	State           string
	DeclinedUserIDs []int64
	DecidedAt       *time.Time
}

type ImportWindow struct {
	Chunk   int
	Node    int
	StartMS int64
	Size    int
}

type NewChatImport struct {
	ChatID         int64
	ImportedBy     int64
	Source         string
	MessageCount   int
	FileCount      int
	FirstAt        time.Time
	LastAt         time.Time
	LowerBound     int64
	FirstMessageID int64
	LastMessageID  int64
	Senders        []ImportSender
	Windows        []ImportWindow
}

type ImportRepository interface {
	Create(ctx context.Context, n NewChatImport) (ChatImport, error)
	Get(ctx context.Context, id int64) (ChatImport, error)
	OfChat(ctx context.Context, chatID int64) ([]ChatImport, error)
	ByID(ctx context.Context, ids []int64) (map[int64]ChatImport, error)
	Senders(ctx context.Context, importIDs []int64) (map[int64][]ImportSender, error)
	Windows(ctx context.Context, importID int64) ([]ImportWindow, error)
	Advance(ctx context.Context, id int64, from, to int) (bool, error)
	Finish(ctx context.Context, id int64) (ChatImport, error)
	MarkRemoving(ctx context.Context, id int64) (ChatImport, error)
	Delete(ctx context.Context, id int64) error
	Assign(ctx context.Context, importID int64, key string, userID *int64) (ImportSender, error)
	Confirm(ctx context.Context, importID int64, key string, userID int64) (ImportSender, error)
	Decline(ctx context.Context, importID int64, key string, userID int64) (ImportSender, error)
	Withdraw(ctx context.Context, importID int64, key string, userID int64) (ImportSender, error)
	LowerBound(ctx context.Context, chatID int64) (int64, error)
	AdvanceReadPointers(ctx context.Context, chatID, from, to int64) error
	ForgetMessages(ctx context.Context, chatID, from, to int64) error
}

func (s *Store) Imports() ImportRepository { return importRepo{s} }

type importRepo struct{ s *Store }

var _ ImportRepository = importRepo{}

func chatImport(r sqlcdb.ChatImport) ChatImport {
	return ChatImport{
		ID: r.ID, ChatID: r.ChatID, ImportedBy: r.ImportedBy, Source: r.Source, State: r.State,
		MessageCount: int(r.MessageCount), FileCount: int(r.FileCount), FirstAt: r.FirstAt, LastAt: r.LastAt,
		LowerBound: r.LowerBound, FirstMessageID: r.FirstMessageID, LastMessageID: r.LastMessageID,
		NextIndex: int(r.NextIndex), CreatedAt: r.CreatedAt, FinishedAt: r.FinishedAt,
	}
}

func importSender(r sqlcdb.ChatImportSender) ImportSender {
	return ImportSender{
		ImportID: r.ImportID, Key: r.SenderKey, Name: r.Name, MessageCount: int(r.MessageCount),
		AssignedUserID: r.AssignedUserID, State: r.State, DeclinedUserIDs: r.DeclinedUserIds, DecidedAt: r.DecidedAt,
	}
}

func (r importRepo) Create(ctx context.Context, n NewChatImport) (ChatImport, error) {
	var out ChatImport
	err := r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		row, err := q.InsertChatImport(ctx, sqlcdb.InsertChatImportParams{
			ChatID: n.ChatID, ImportedBy: &n.ImportedBy, Source: n.Source,
			MessageCount: int32(n.MessageCount), FileCount: int32(n.FileCount), //nolint:gosec // bounded by the schema
			FirstAt: n.FirstAt, LastAt: n.LastAt, LowerBound: n.LowerBound,
			FirstMessageID: n.FirstMessageID, LastMessageID: n.LastMessageID,
		})
		if err != nil {
			return err
		}
		out = chatImport(row)
		for _, snd := range n.Senders {
			err := q.InsertImportSender(ctx, sqlcdb.InsertImportSenderParams{
				ImportID: row.ID, SenderKey: snd.Key, Name: snd.Name, MessageCount: int32(snd.MessageCount), //nolint:gosec // bounded by the caller
				AssignedUserID: snd.AssignedUserID, State: snd.State,
			})
			if err != nil {
				return err
			}
		}
		for _, w := range n.Windows {
			err := q.InsertImportWindow(ctx, sqlcdb.InsertImportWindowParams{
				ImportID: row.ID, Chunk: int32(w.Chunk), Node: int32(w.Node), StartMs: w.StartMS, Size: int32(w.Size), //nolint:gosec // bounded by the caller
			})
			if err != nil {
				return err
			}
		}
		return nil
	})
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		switch {
		case pg.Code == codeExclusionViolation:
			return ChatImport{}, ErrWindowTaken
		case pg.Code == codeUniqueViolation && pg.ConstraintName == "chat_imports_running_idx":
			return ChatImport{}, ErrImportRunning
		}
	}
	if err != nil {
		return ChatImport{}, mapError(err)
	}
	return out, nil
}

func (r importRepo) Get(ctx context.Context, id int64) (ChatImport, error) {
	row, err := r.s.q.GetChatImport(ctx, id)
	if err != nil {
		return ChatImport{}, mapError(err)
	}
	return chatImport(row), nil
}

func (r importRepo) OfChat(ctx context.Context, chatID int64) ([]ChatImport, error) {
	rows, err := r.s.q.ChatImportsOfChat(ctx, chatID)
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]ChatImport, len(rows))
	for i, row := range rows {
		out[i] = chatImport(row)
	}
	return out, nil
}

func (r importRepo) ByID(ctx context.Context, ids []int64) (map[int64]ChatImport, error) {
	out := make(map[int64]ChatImport, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := r.s.q.ChatImportsByID(ctx, ids)
	if err != nil {
		return nil, mapError(err)
	}
	for _, row := range rows {
		out[row.ID] = chatImport(row)
	}
	return out, nil
}

func (r importRepo) Senders(ctx context.Context, importIDs []int64) (map[int64][]ImportSender, error) {
	out := make(map[int64][]ImportSender, len(importIDs))
	if len(importIDs) == 0 {
		return out, nil
	}
	rows, err := r.s.q.ImportSendersOf(ctx, importIDs)
	if err != nil {
		return nil, mapError(err)
	}
	for _, row := range rows {
		out[row.ImportID] = append(out[row.ImportID], importSender(row))
	}
	return out, nil
}

func (r importRepo) Windows(ctx context.Context, importID int64) ([]ImportWindow, error) {
	rows, err := r.s.q.ImportWindowsOf(ctx, importID)
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]ImportWindow, len(rows))
	for i, row := range rows {
		out[i] = ImportWindow{Chunk: int(row.Chunk), Node: int(row.Node), StartMS: row.StartMs, Size: int(row.Size)}
	}
	return out, nil
}

func (r importRepo) Advance(ctx context.Context, id int64, from, to int) (bool, error) {
	n, err := r.s.q.AdvanceChatImport(ctx, sqlcdb.AdvanceChatImportParams{ID: id, FromIndex: int32(from), NextIndex: int32(to)}) //nolint:gosec // bounded by the schema
	if err != nil {
		return false, mapError(err)
	}
	return n > 0, nil
}

func (r importRepo) Finish(ctx context.Context, id int64) (ChatImport, error) {
	row, err := r.s.q.FinishChatImport(ctx, id)
	if err != nil {
		return ChatImport{}, mapError(err)
	}
	return chatImport(row), nil
}

func (r importRepo) MarkRemoving(ctx context.Context, id int64) (ChatImport, error) {
	row, err := r.s.q.MarkChatImportRemoving(ctx, id)
	if err != nil {
		return ChatImport{}, mapError(err)
	}
	return chatImport(row), nil
}

func (r importRepo) Delete(ctx context.Context, id int64) error {
	return mapError(r.s.q.DeleteChatImport(ctx, id))
}

func (r importRepo) Assign(ctx context.Context, importID int64, key string, userID *int64) (ImportSender, error) {
	row, err := r.s.q.AssignImportSender(ctx, sqlcdb.AssignImportSenderParams{ImportID: importID, SenderKey: key, UserID: userID})
	if err != nil {
		return ImportSender{}, mapError(err)
	}
	return importSender(row), nil
}

func (r importRepo) Confirm(ctx context.Context, importID int64, key string, userID int64) (ImportSender, error) {
	row, err := r.s.q.ConfirmImportSender(ctx, sqlcdb.ConfirmImportSenderParams{ImportID: importID, SenderKey: key, UserID: &userID})
	if err != nil {
		return ImportSender{}, mapError(err)
	}
	return importSender(row), nil
}

func (r importRepo) Decline(ctx context.Context, importID int64, key string, userID int64) (ImportSender, error) {
	row, err := r.s.q.DeclineImportSender(ctx, sqlcdb.DeclineImportSenderParams{ImportID: importID, SenderKey: key, UserID: userID})
	if err != nil {
		return ImportSender{}, mapError(err)
	}
	return importSender(row), nil
}

func (r importRepo) Withdraw(ctx context.Context, importID int64, key string, userID int64) (ImportSender, error) {
	row, err := r.s.q.WithdrawImportSender(ctx, sqlcdb.WithdrawImportSenderParams{ImportID: importID, SenderKey: key, UserID: userID})
	if err != nil {
		return ImportSender{}, mapError(err)
	}
	return importSender(row), nil
}

func (r importRepo) LowerBound(ctx context.Context, chatID int64) (int64, error) {
	bound, err := r.s.q.ImportLowerBound(ctx, chatID)
	if err != nil {
		return 0, mapError(err)
	}
	return bound, nil
}

func (r importRepo) AdvanceReadPointers(ctx context.Context, chatID, from, to int64) error {
	return mapError(r.s.q.AdvanceReadPointers(ctx, sqlcdb.AdvanceReadPointersParams{ChatID: chatID, FromID: &from, ToID: &to}))
}

func (r importRepo) ForgetMessages(ctx context.Context, chatID, from, to int64) error {
	return r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		if err := q.UnlinkImportedAttachments(ctx, sqlcdb.UnlinkImportedAttachmentsParams{ChatID: chatID, FromID: from, ToID: to}); err != nil {
			return err
		}
		if err := q.DeleteImportedURLs(ctx, sqlcdb.DeleteImportedURLsParams{ChatID: chatID, FromID: from, ToID: to}); err != nil {
			return err
		}
		if err := q.DeleteImportedSharedPins(ctx, sqlcdb.DeleteImportedSharedPinsParams{ChatID: chatID, FromID: from, ToID: to}); err != nil {
			return err
		}
		return q.DeleteImportedPersonalPins(ctx, sqlcdb.DeleteImportedPersonalPinsParams{ChatID: chatID, FromID: from, ToID: to})
	})
}
