package messages

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/scylla"
)

const (
	MaxSyncChanges = 500
	SyncSettle     = 2 * time.Second
)

type SyncReader interface {
	Since(ctx context.Context, userID, after int64, settle time.Duration, limit int) ([]postgres.SyncChange, error)
	Head(ctx context.Context, settle time.Duration) (int64, error)
	Oldest(ctx context.Context) (int64, error)
}

type SyncEntry struct {
	Seq       int64
	ChatID    int64
	Kind      postgres.SyncKind
	MessageID int64
	Scope     string
	Message   *scylla.Message
}

type SyncPage struct {
	Entries []SyncEntry
	Cursor  int64
	HasMore bool
	Reset   bool
}

func (s *Service) Changes(ctx context.Context, r SyncReader, userID int64, after *int64, limit int, settle time.Duration) (SyncPage, error) {
	if settle <= 0 {
		settle = SyncSettle
	}
	if limit <= 0 || limit > MaxSyncChanges {
		limit = MaxSyncChanges
	}
	if after == nil {
		head, err := r.Head(ctx, settle)
		if err != nil {
			return SyncPage{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		return SyncPage{Cursor: head}, nil
	}
	return s.changesAfter(ctx, r, userID, *after, limit, settle)
}

func (s *Service) changesAfter(ctx context.Context, r SyncReader, userID, after int64, limit int, settle time.Duration) (SyncPage, error) {
	oldest, err := r.Oldest(ctx)
	if err != nil {
		return SyncPage{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if oldest > after+1 {
		head, err := r.Head(ctx, settle)
		if err != nil {
			return SyncPage{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		return SyncPage{Cursor: head, Reset: true}, nil
	}
	changes, err := r.Since(ctx, userID, after, settle, limit+1)
	if err != nil {
		return SyncPage{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	page := SyncPage{Cursor: after, HasMore: len(changes) > limit}
	if page.HasMore {
		changes = changes[:limit]
	}
	if len(changes) > 0 {
		page.Cursor = changes[len(changes)-1].Seq
	}
	page.Entries, err = s.syncEntries(ctx, userID, collapse(changes))
	return page, err
}

type collapseKey struct {
	chat, message int64
	kind          string
}

func keyOf(c postgres.SyncChange) collapseKey {
	switch c.Kind {
	case postgres.SyncMessageEdited, postgres.SyncReactions, postgres.SyncMessageDeleted, postgres.SyncMessageHidden:
		return collapseKey{c.ChatID, c.MessageID, "message"}
	case postgres.SyncPins:
		return collapseKey{c.ChatID, 0, "pins"}
	default:
		return collapseKey{c.ChatID, c.Seq, string(c.Kind)}
	}
}

func collapse(changes []postgres.SyncChange) []postgres.SyncChange {
	last := make(map[collapseKey]int, len(changes))
	for i, c := range changes {
		last[keyOf(c)] = i
	}
	out := make([]postgres.SyncChange, 0, len(last))
	for i, c := range changes {
		if last[keyOf(c)] == i {
			out = append(out, c)
		}
	}
	return out
}

func (s *Service) syncEntries(ctx context.Context, userID int64, changes []postgres.SyncChange) ([]SyncEntry, error) {
	out := make([]SyncEntry, 0, len(changes))
	for _, c := range changes {
		e := SyncEntry{Seq: c.Seq, ChatID: c.ChatID, Kind: c.Kind, MessageID: c.MessageID}
		switch c.Kind {
		case postgres.SyncHistoryCleared:
			e.Scope = ClearScope(c.UserID)
		case postgres.SyncMessageDeleted:
			e.Scope = ScopeEveryone
		case postgres.SyncMessageHidden:
			e.Kind, e.Scope = postgres.SyncMessageDeleted, ScopeMe
		case postgres.SyncMessageEdited, postgres.SyncReactions:
			m, ok, err := s.current(ctx, userID, c.ChatID, c.MessageID)
			if err != nil {
				return nil, err
			}
			if !ok {
				e.Kind, e.Scope = postgres.SyncMessageDeleted, ScopeMe
				break
			}
			e.Kind, e.Message = postgres.SyncMessageEdited, &m
		}
		out = append(out, e)
	}
	return out, nil
}

func ClearScope(userID int64) string {
	if userID == 0 {
		return ScopeEveryone
	}
	return ScopeMe
}

func (s *Service) current(ctx context.Context, userID, chatID, messageID int64) (scylla.Message, bool, error) {
	ok, err := s.Visible(ctx, userID, chatID, messageID)
	if err != nil {
		return scylla.Message{}, false, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if !ok {
		return scylla.Message{}, false, nil
	}
	m, err := s.message(ctx, chatID, messageID)
	if errors.Is(err, ErrNotFound) {
		return scylla.Message{}, false, nil
	}
	return m, err == nil, err
}
