package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/messages"
	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/scylla"
)

type SyncStore interface {
	messages.SyncReader
	StorageKey(ctx context.Context, sessionID uuid.UUID) (string, error)
	SaveStorageKey(ctx context.Context, sessionID uuid.UUID, sealed string) (string, error)
}

func storageKeyAAD(sessionID uuid.UUID) string { return "storage-key:" + sessionID.String() }

func (m *MessageServer) ListSyncChanges(w http.ResponseWriter, r *http.Request, params ListSyncChangesParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	if m.o.Sync == nil {
		notImplemented(w, r)
		return
	}
	var after *int64
	if params.Cursor != nil {
		n, err := strconv.ParseInt(*params.Cursor, 10, 64)
		if err != nil || n < 0 {
			WriteProblem(w, http.StatusUnprocessableEntity, ErrorCodeValidationFailed, ProblemError{Field: "cursor", Message: "not a cursor of this endpoint"})
			return
		}
		after = &n
	}
	limit := 0
	if params.Limit != nil {
		limit = *params.Limit
	}
	page, err := m.o.Service.Changes(r.Context(), m.o.Sync, p.UserID, after, limit, m.o.SyncSettle)
	if err != nil {
		serviceError(w, r, m.o.Log, "sync changes", err)
		return
	}
	changes, err := m.presentChanges(r, p.UserID, page.Entries)
	if err != nil {
		serviceError(w, r, m.o.Log, "render sync changes", err)
		return
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, SyncChangesPage{
		Changes: changes, NextCursor: strconv.FormatInt(page.Cursor, 10), HasMore: page.HasMore, ResetRequired: page.Reset,
	})
}

func (m *MessageServer) presentChanges(r *http.Request, viewer int64, entries []messages.SyncEntry) ([]SyncChange, error) {
	out := make([]SyncChange, 0, len(entries))
	for _, e := range entries {
		c := SyncChange{ChatId: FormatID(e.ChatID)}
		if e.Scope != "" {
			scope := SyncChangeScope(e.Scope)
			c.Scope = &scope
		}
		switch e.Kind {
		case postgres.SyncMessageEdited:
			rendered, err := m.render(r.Context(), viewer, e.ChatID, []scylla.Message{*e.Message}, "")
			if err != nil {
				return nil, err
			}
			c.Type, c.Message = SyncChangeTypeMessageUpdated, &rendered[0]
		case postgres.SyncMessageDeleted:
			c.Type, c.MessageId = SyncChangeTypeMessageDeleted, formatIDPtr(&e.MessageID)
		case postgres.SyncPins:
			c.Type = SyncChangeTypePinsChanged
		case postgres.SyncHistoryCleared:
			c.Type, c.BeforeMessageId = SyncChangeTypeHistoryCleared, formatIDPtr(&e.MessageID)
		case postgres.SyncChatHidden:
			c.Type, c.BeforeMessageId = SyncChangeTypeChatHidden, formatIDPtr(&e.MessageID)
		case postgres.SyncHistoryImported:
			c.Type = SyncChangeTypeHistoryImported
		default:
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

func (m *MessageServer) GetStorageKey(w http.ResponseWriter, r *http.Request, _ GetStorageKeyParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	if m.o.Sync == nil || m.o.Sealer == nil {
		notImplemented(w, r)
		return
	}
	aad := storageKeyAAD(p.SessionID)
	sealed, err := m.o.Sync.StorageKey(r.Context(), p.SessionID)
	if errors.Is(err, postgres.ErrNotFound) {
		sealed, err = m.newStorageKey(r, p.SessionID, aad)
	}
	if errors.Is(err, postgres.ErrNotFound) {
		WriteProblem(w, http.StatusUnauthorized, ErrorCodeUnauthenticated)
		return
	}
	if err != nil {
		serviceError(w, r, m.o.Log, "storage key", errors.Join(messages.ErrUnavailable, err))
		return
	}
	key, err := m.o.Sealer.Open(sealed, aad)
	if err != nil {
		serviceError(w, r, m.o.Log, "open storage key", errors.Join(messages.ErrUnavailable, err))
		return
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, StorageKey{Key: key, Algorithm: A256GCM})
}

func (m *MessageServer) newStorageKey(r *http.Request, sessionID uuid.UUID, aad string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	sealed, err := m.o.Sealer.Seal(base64.RawURLEncoding.EncodeToString(raw), aad)
	if err != nil {
		return "", err
	}
	return m.o.Sync.SaveStorageKey(r.Context(), sessionID, sealed)
}
