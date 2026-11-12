package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/myronsi/messenger-back/internal/store/postgres"
)

type unsentPage struct {
	Items []UnsentMessage `json:"items"`
}

// ListUnsentMessages implements GET /chats/{id}/unsent.
func (m *MessageServer) ListUnsentMessages(w http.ResponseWriter, r *http.Request, chatID ChatId, _ ListUnsentMessagesParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, "chat_id", chatID)
	if !ok {
		return
	}
	kept, err := m.o.Service.ListUnsent(r.Context(), p.UserID, id)
	if err != nil {
		serviceError(w, r, m.o.Log, "unsent messages", err)
		return
	}
	var stickerIDs []int64
	for _, u := range kept {
		if u.StickerID != nil {
			stickerIDs = append(stickerIDs, *u.StickerID)
		}
	}
	stickers := map[int64]postgres.Sticker{}
	if len(stickerIDs) > 0 {
		if stickers, err = m.o.Store.Stickers().Stickers(r.Context(), stickerIDs); err != nil {
			serviceError(w, r, m.o.Log, "unsent stickers", err)
			return
		}
	}
	out := unsentPage{Items: make([]UnsentMessage, 0, len(kept))}
	for _, u := range kept {
		item, err := m.presentUnsent(r, p.UserID, u, stickers)
		if err != nil {
			serviceError(w, r, m.o.Log, "unsent attachments", err)
			return
		}
		out.Items = append(out.Items, item)
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, out)
}

func (m *MessageServer) presentUnsent(r *http.Request, viewerID int64, u postgres.UnsentMessage, stickers map[int64]postgres.Sticker) (UnsentMessage, error) {
	item := UnsentMessage{
		ChatId: FormatID(u.ChatID), ClientTempId: u.ClientTempID, Type: UnsentMessageType(u.Type), Content: u.Content,
		ReplyTo: formatIDPtr(u.ReplyTo), Attachments: []Attachment{}, ErrorCode: ErrorCode(u.ErrorCode), CreatedAt: utc(u.CreatedAt),
	}
	drawings := map[string]json.RawMessage{}
	if len(u.Drawings) > 0 {
		_ = json.Unmarshal(u.Drawings, &drawings)
	}
	for _, attachmentID := range u.AttachmentIDs {
		a, err := m.o.Store.Attachments().Get(r.Context(), attachmentID)
		if errors.Is(err, postgres.ErrNotFound) {
			continue
		}
		if err != nil {
			return UnsentMessage{}, err
		}
		if a.UploaderID == nil || *a.UploaderID != viewerID {
			continue
		}
		item.Attachments = append(item.Attachments, withDrawing(PresentAttachment(a, u.Type, m.o.BasePath), drawings[attachmentID.String()]))
	}
	if u.StickerID != nil {
		if st, ok := stickers[*u.StickerID]; ok && !st.Deleted {
			sticker := PresentSticker(st, m.o.BasePath)
			item.Sticker = &sticker
		}
	}
	return item, nil
}

// DeleteUnsentMessage implements DELETE /chats/{id}/unsent/{client_temp_id}.
func (m *MessageServer) DeleteUnsentMessage(w http.ResponseWriter, r *http.Request, chatID ChatId, clientTempID ClientTempId, _ DeleteUnsentMessageParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, "chat_id", chatID)
	if !ok {
		return
	}
	if err := m.o.Service.DeleteUnsent(r.Context(), p.UserID, id, clientTempID); err != nil {
		serviceError(w, r, m.o.Log, "discard unsent message", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
