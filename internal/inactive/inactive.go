package inactive

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/myronsi/messenger-back/internal/auth"
	"github.com/myronsi/messenger-back/internal/events"
	"github.com/myronsi/messenger-back/internal/store/postgres"
)

const (
	Years     = 1
	Period    = "1 year"
	WarnAhead = 30 * 24 * time.Hour
	day       = 24 * time.Hour
	batchSize = 100
)

type Store interface {
	ClearRevived(ctx context.Context) (int64, error)
	ToWarn(ctx context.Context, rule postgres.InactivityRule, after int64, limit int) ([]postgres.InactiveAccount, error)
	Warn(ctx context.Context, rule postgres.InactivityRule, userID int64, deletesAt time.Time, payload string) (bool, error)
	ToDelete(ctx context.Context, rule postgres.InactivityRule, after int64, limit int) ([]int64, error)
	Count(ctx context.Context, rule postgres.InactivityRule) (warn, del int64, err error)
}

type Accounts interface {
	InactivityWarningPayload(userID int64) (string, error)
	DeleteInactiveAccount(ctx context.Context, userID int64, rule postgres.InactivityRule) (postgres.DeletedAccount, error)
}

type Emitter interface {
	Emit(ctx context.Context, e events.Event) error
}

type Claimer interface {
	Claim(ctx context.Context, id string, ttl time.Duration) (bool, error)
}

type Deps struct {
	Store         Store
	Accounts      Accounts
	AfterDeletion func(ctx context.Context, userID int64, d postgres.DeletedAccount)
	Events        Emitter
	Enabled       bool
	Mail          bool
	Now           func() time.Time
	Log           *slog.Logger
}

type Service struct{ d Deps }

func New(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	return &Service{d: d}
}

func (s *Service) Rule() postgres.InactivityRule {
	now := s.d.Now().UTC()
	active := now.AddDate(-Years, 0, 0)
	return postgres.InactivityRule{ActiveBefore: active, WarnBefore: active.Add(WarnAhead), Now: now, WarnFirst: s.d.Mail}
}

func DeletionDay(warnedAt time.Time) time.Time {
	return warnedAt.UTC().Truncate(day).Add(WarnAhead + day)
}

type Plan struct {
	Rule   postgres.InactivityRule
	Warn   int64
	Delete int64
}

func (s *Service) Plan(ctx context.Context) (Plan, error) {
	rule := s.Rule()
	warn, del, err := s.d.Store.Count(ctx, rule)
	return Plan{Rule: rule, Warn: warn, Delete: del}, err
}

type Result struct {
	Revived int64
	Warned  int
	Deleted int
}

func (s *Service) Run(ctx context.Context) (Result, error) {
	var out Result
	if !s.d.Enabled {
		return out, nil
	}
	var err error
	rule := s.Rule()
	if out.Revived, err = s.d.Store.ClearRevived(ctx); err != nil {
		return out, err
	}
	if out.Warned, err = s.warn(ctx, rule); err != nil {
		return out, err
	}
	out.Deleted, err = s.delete(ctx, rule)
	return out, err
}

func (s *Service) warn(ctx context.Context, rule postgres.InactivityRule) (int, error) {
	if !rule.WarnFirst {
		return 0, nil
	}
	deletesAt := DeletionDay(rule.Now)
	warned := 0
	var after int64
	for {
		batch, err := s.d.Store.ToWarn(ctx, rule, after, batchSize)
		if err != nil {
			return warned, err
		}
		for _, a := range batch {
			after = a.UserID
			payload, err := s.d.Accounts.InactivityWarningPayload(a.UserID)
			if err != nil {
				return warned, err
			}
			ok, err := s.d.Store.Warn(ctx, rule, a.UserID, deletesAt, payload)
			if err != nil {
				return warned, err
			}
			if ok {
				warned++
				s.d.Log.InfoContext(ctx, "inactive account warned", "user_id", a.UserID, "deletes_at", deletesAt)
			}
		}
		if len(batch) < batchSize || ctx.Err() != nil {
			return warned, ctx.Err()
		}
	}
}

func (s *Service) delete(ctx context.Context, rule postgres.InactivityRule) (int, error) {
	deleted := 0
	var after int64
	for {
		batch, err := s.d.Store.ToDelete(ctx, rule, after, batchSize)
		if err != nil {
			return deleted, err
		}
		for _, userID := range batch {
			after = userID
			if ctx.Err() != nil {
				return deleted, ctx.Err()
			}
			d, err := s.d.Accounts.DeleteInactiveAccount(ctx, userID, rule)
			if errors.Is(err, postgres.ErrNotFound) {
				continue
			}
			if err != nil && !errors.Is(err, auth.ErrRevocationIncomplete) {
				return deleted, err
			}
			cleanup := context.WithoutCancel(ctx)
			if s.d.AfterDeletion != nil {
				s.d.AfterDeletion(cleanup, userID, d)
			}
			if s.d.Events != nil {
				if err := s.d.Events.Emit(cleanup, events.Event{Type: events.UserDeleted, UserID: userID}); err != nil {
					s.d.Log.WarnContext(ctx, "inactive accounts: emit user.deleted", "error", err)
				}
			}
			deleted++
			s.d.Log.InfoContext(ctx, "inactive account deleted", "user_id", userID)
		}
		if len(batch) < batchSize {
			return deleted, nil
		}
	}
}

func (s *Service) RunDaily(ctx context.Context, every time.Duration, once Claimer) {
	run := func() {
		if once != nil {
			slot := s.d.Now().UTC().Truncate(every).Format(time.RFC3339)
			claimed, err := once.Claim(ctx, slot, every)
			if err != nil || !claimed {
				return
			}
		}
		res, err := s.Run(ctx)
		if err != nil && ctx.Err() == nil {
			s.d.Log.ErrorContext(ctx, "inactive accounts job failed", "err", err, "warned", res.Warned, "deleted", res.Deleted)
			return
		}
		if res.Revived+int64(res.Warned+res.Deleted) > 0 {
			s.d.Log.InfoContext(ctx, "inactive accounts job", "revived", res.Revived, "warned", res.Warned, "deleted", res.Deleted)
		}
	}
	run()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			run()
		}
	}
}
