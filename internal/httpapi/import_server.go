package httpapi

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/messages"
	"github.com/myronsi/messenger-back/internal/store/postgres"
)

func presentImportSender(s postgres.ImportSender) ChatImportSender {
	return ChatImportSender{
		Key: s.Key, Name: s.Name, MessageCount: s.MessageCount, State: ImportSenderState(s.State), UserId: formatIDPtr(s.AssignedUserID),
	}
}

func presentChatImport(ci messages.ChatImport) ChatImport {
	out := ChatImport{
		Id: FormatID(ci.ID), ChatId: FormatID(ci.ChatID), Source: ImportSource(ci.Source), State: ChatImportState(ci.State),
		MessageCount: ci.MessageCount, FileCount: ci.FileCount, FirstAt: utc(ci.FirstAt), LastAt: utc(ci.LastAt),
		NextIndex: ci.NextIndex, ImportedBy: formatIDPtr(ci.ImportedBy), CreatedAt: utc(ci.CreatedAt),
		Senders: make([]ChatImportSender, len(ci.Senders)),
	}
	if ci.FinishedAt != nil {
		t := utc(*ci.FinishedAt)
		out.FinishedAt = &t
	}
	for i, s := range ci.Senders {
		out.Senders[i] = presentImportSender(s)
	}
	return out
}

func (m *MessageServer) ListChatImports(w http.ResponseWriter, r *http.Request, chatID ChatId, _ ListChatImportsParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	chat, ok := parseID(w, "chat_id", chatID)
	if !ok {
		return
	}
	list, err := m.o.Service.Imports(r.Context(), p.UserID, chat)
	if err != nil {
		serviceError(w, r, m.o.Log, "chat imports", err)
		return
	}
	out := ChatImportList{Items: make([]ChatImport, len(list))}
	for i, ci := range list {
		out.Items[i] = presentChatImport(ci)
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, out)
}

func (m *MessageServer) StartChatImport(w http.ResponseWriter, r *http.Request, chatID ChatId, _ StartChatImportParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	chat, ok := parseID(w, "chat_id", chatID)
	if !ok {
		return
	}
	var body StartChatImportRequest
	if !decode(w, r, &body) {
		return
	}
	req := messages.StartImport{
		ChatID: chat, UserID: p.UserID, Source: string(body.Source), MessageCount: body.MessageCount, FileCount: body.FileCount,
		FirstAt: body.FirstAt, LastAt: body.LastAt, Senders: make([]messages.ImportSenderInput, len(body.Senders)),
	}
	for i, s := range body.Senders {
		in := messages.ImportSenderInput{Key: s.Key, Name: s.Name, MessageCount: s.MessageCount, Me: s.Me != nil && *s.Me}
		if s.UserId != nil {
			uid, ok := parseID(w, "senders.user_id", *s.UserId)
			if !ok {
				return
			}
			in.AssignTo = &uid
		}
		req.Senders[i] = in
	}
	ci, err := m.o.Service.StartImport(r.Context(), req)
	if err != nil {
		serviceError(w, r, m.o.Log, "start import", err)
		return
	}
	noStore(w)
	writeJSONStatus(w, http.StatusCreated, presentChatImport(ci))
}

func (m *MessageServer) GetChatImport(w http.ResponseWriter, r *http.Request, importID ImportId, _ GetChatImportParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, "import_id", importID)
	if !ok {
		return
	}
	ci, err := m.o.Service.Import(r.Context(), p.UserID, id)
	if err != nil {
		serviceError(w, r, m.o.Log, "chat import", err)
		return
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, presentChatImport(ci))
}

func (m *MessageServer) RemoveChatImport(w http.ResponseWriter, r *http.Request, importID ImportId, _ RemoveChatImportParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, "import_id", importID)
	if !ok {
		return
	}
	if err := m.o.Service.RemoveImport(r.Context(), p.UserID, id); err != nil {
		serviceError(w, r, m.o.Log, "remove import", err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (m *MessageServer) AddImportedMessages(w http.ResponseWriter, r *http.Request, importID ImportId, _ AddImportedMessagesParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, "import_id", importID)
	if !ok {
		return
	}
	var body AddImportedMessagesRequest
	if !decode(w, r, &body) {
		return
	}
	batch := make([]messages.ImportedMessage, len(body.Messages))
	for i, in := range body.Messages {
		msg := messages.ImportedMessage{Index: in.Index, OriginalAt: in.OriginalAt, Type: string(in.Type), Content: in.Content, ReplyToIndex: in.ReplyToIndex}
		if in.SenderKey != nil {
			msg.SenderKey = *in.SenderKey
		}
		if in.StickerId != nil {
			sticker, ok := parseID(w, "messages.sticker_id", *in.StickerId)
			if !ok {
				return
			}
			msg.StickerID = &sticker
		}
		if in.AttachmentIds != nil {
			msg.AttachmentIDs = append([]uuid.UUID(nil), *in.AttachmentIds...)
		}
		batch[i] = msg
	}
	next, err := m.o.Service.AddImportBatch(r.Context(), p.UserID, id, body.FromIndex, batch)
	if err != nil {
		serviceError(w, r, m.o.Log, "add imported messages", err)
		return
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, ImportProgress{NextIndex: next})
}

func (m *MessageServer) FinishChatImport(w http.ResponseWriter, r *http.Request, importID ImportId, _ FinishChatImportParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, "import_id", importID)
	if !ok {
		return
	}
	ci, err := m.o.Service.FinishImport(r.Context(), p.UserID, id)
	if err != nil {
		serviceError(w, r, m.o.Log, "finish import", err)
		return
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, presentChatImport(ci))
}

func (m *MessageServer) AssignImportSender(w http.ResponseWriter, r *http.Request, importID ImportId, senderKey ImportSenderKey, _ AssignImportSenderParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, "import_id", importID)
	if !ok {
		return
	}
	var body AssignImportSenderRequest
	if !decode(w, r, &body) {
		return
	}
	var assignTo *int64
	if body.UserId != nil {
		uid, ok := parseID(w, "user_id", *body.UserId)
		if !ok {
			return
		}
		assignTo = &uid
	}
	snd, err := m.o.Service.AssignImportSender(r.Context(), p.UserID, id, senderKey, assignTo)
	if err != nil {
		serviceError(w, r, m.o.Log, "assign import sender", err)
		return
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, presentImportSender(snd))
}

func (m *MessageServer) DecideImportSender(w http.ResponseWriter, r *http.Request, importID ImportId, senderKey ImportSenderKey, _ DecideImportSenderParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, "import_id", importID)
	if !ok {
		return
	}
	var body DecideImportSenderRequest
	if !decode(w, r, &body) {
		return
	}
	snd, err := m.o.Service.DecideImportSender(r.Context(), p.UserID, id, senderKey, string(body.Decision))
	if err != nil {
		serviceError(w, r, m.o.Log, "decide import sender", err)
		return
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, presentImportSender(snd))
}
