package realtime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Close codes of the protocol (api/websocket.md) besides CloseServiceRestart.
const (
	// CloseInvalidTicket: the ticket was unknown, expired or used.
	CloseInvalidTicket = 4401
	// CloseClientOutdated: the client's contract version is no longer served (sent after hello).
	CloseClientOutdated = 4426
)

// MaxClientEvent bounds one client frame: a 4096-character message plus the envelope, with room for
// four-byte characters.
const MaxClientEvent = 32 << 10

// serverEvent is the envelope of every event sent to clients.
type serverEvent struct {
	Type         string  `json:"type"`
	EventID      string  `json:"event_id"`
	ChatID       *string `json:"chat_id"`
	Data         any     `json:"data"`
	ClientTempID *string `json:"client_temp_id,omitempty"`
}

func idString(id int64) string { return strconv.FormatInt(id, 10) }

func chatRef(chatID int64) *string {
	if chatID == 0 {
		return nil
	}
	s := idString(chatID)
	return &s
}

// clientEvent is a decoded and validated client event.
type clientEvent struct {
	Type         string
	ClientTempID string
	ChatID       int64
	// One of these is set, matching Type.
	Message  *messageData
	Target   *targetData // resend, read
	Edit     *editData
	Delete   *deleteData
	Reaction *reactionData
	Typing   *typingData
}

type messageData struct {
	Type         string     `json:"type"`
	Content      *string    `json:"content"`
	AttachmentID *uuid.UUID `json:"-"`
	ReplyTo      *int64     `json:"-"`
}

type targetData struct{ MessageID int64 }

type editData struct {
	MessageID int64
	Content   string
}

type deleteData struct {
	MessageID int64
	Scope     string
}

type reactionData struct {
	MessageID int64
	Emoji     string
}

type typingData struct{ IsTyping bool }

// protocolError is a malformed client event; Code is a contract error code.
type protocolError struct {
	Code         string
	Msg          string
	ClientTempID string
	ChatID       int64
}

func (e *protocolError) Error() string { return e.Msg }

// decodeStrict decodes raw into v and refuses unknown fields and trailing data, like the schemas'
// additionalProperties: false.
func decodeStrict(raw json.RawMessage, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("trailing data")
	}
	return nil
}

// parseID reads a positive decimal id as the contract sends them (strings).
func parseID(s *string, field string) (int64, error) {
	if s == nil {
		return 0, fmt.Errorf("%s is required", field)
	}
	if len(*s) == 0 || len(*s) > 19 || (*s)[0] == '0' {
		return 0, fmt.Errorf("%s must be a decimal id", field)
	}
	id, err := strconv.ParseInt(*s, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("%s must be a decimal id", field)
	}
	return id, nil
}

func optionalID(raw json.RawMessage, field string) (*int64, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("%s must be a decimal id or null", field)
	}
	id, err := parseID(&s, field)
	return &id, err
}

// decodeClientEvent parses one frame. It accepts exactly what api/websocket/client/*.schema.json allows
// (a test runs every example through it); anything else is a protocolError.
func decodeClientEvent(frame []byte) (clientEvent, error) {
	var env struct {
		Type         string          `json:"type"`
		ClientTempID *string         `json:"client_temp_id"`
		ChatID       *string         `json:"chat_id"`
		Data         json.RawMessage `json:"data"`
	}
	if !utf8.Valid(frame) {
		return clientEvent{}, &protocolError{Code: "invalid_request", Msg: "the event is not valid UTF-8"}
	}
	if err := decodeStrict(frame, &env); err != nil {
		return clientEvent{}, &protocolError{Code: "invalid_request", Msg: "the event is not a valid envelope"}
	}
	ev := clientEvent{Type: env.Type}
	fail := func(format string, args ...any) error {
		e := &protocolError{Code: "validation_failed", Msg: fmt.Sprintf(format, args...), ChatID: ev.ChatID}
		if env.ClientTempID != nil {
			e.ClientTempID = *env.ClientTempID
		}
		return e
	}
	if env.Type == "typing" {
		if env.ClientTempID != nil {
			return clientEvent{}, fail("typing carries no client_temp_id")
		}
	} else {
		if env.ClientTempID == nil || len(*env.ClientTempID) == 0 || utf8.RuneCountInString(*env.ClientTempID) > 64 {
			return clientEvent{}, fail("client_temp_id must be 1 to 64 characters")
		}
		ev.ClientTempID = *env.ClientTempID
	}
	chatID, err := parseID(env.ChatID, "chat_id")
	if err != nil {
		return clientEvent{}, fail("%s", err)
	}
	ev.ChatID = chatID
	if len(env.Data) == 0 || env.Data[0] != '{' {
		return clientEvent{}, fail("data must be an object")
	}

	switch env.Type {
	case "message":
		var d struct {
			Type         string          `json:"type"`
			Content      *string         `json:"content"`
			AttachmentID *string         `json:"attachment_id"`
			ReplyTo      json.RawMessage `json:"reply_to"`
		}
		if err := decodeStrict(env.Data, &d); err != nil {
			return clientEvent{}, fail("data does not match the message event")
		}
		m := &messageData{Type: d.Type, Content: d.Content}
		if m.ReplyTo, err = optionalID(d.ReplyTo, "reply_to"); err != nil {
			return clientEvent{}, fail("%s", err)
		}
		switch d.Type {
		case "text":
			if d.AttachmentID != nil || d.Content == nil || *d.Content == "" {
				return clientEvent{}, fail("a text message has content and no attachment_id")
			}
		case "file", "voice":
			if d.AttachmentID == nil {
				return clientEvent{}, fail("a %s message needs attachment_id", d.Type)
			}
			a, err := uuid.Parse(*d.AttachmentID)
			if err != nil {
				return clientEvent{}, fail("attachment_id is not an attachment id")
			}
			m.AttachmentID = &a
		default:
			return clientEvent{}, fail("data.type must be text, file or voice")
		}
		if d.Content != nil && utf8.RuneCountInString(*d.Content) > 4096 {
			return clientEvent{}, fail("content must be at most 4096 characters")
		}
		ev.Message = m
	case "resend", "read":
		var d struct {
			MessageID *string `json:"message_id"`
		}
		if err := decodeStrict(env.Data, &d); err != nil {
			return clientEvent{}, fail("data does not match the %s event", env.Type)
		}
		id, err := parseID(d.MessageID, "message_id")
		if err != nil {
			return clientEvent{}, fail("%s", err)
		}
		ev.Target = &targetData{MessageID: id}
	case "edit":
		var d struct {
			MessageID *string `json:"message_id"`
			Content   *string `json:"content"`
		}
		if err := decodeStrict(env.Data, &d); err != nil {
			return clientEvent{}, fail("data does not match the edit event")
		}
		id, err := parseID(d.MessageID, "message_id")
		if err != nil {
			return clientEvent{}, fail("%s", err)
		}
		if d.Content == nil || *d.Content == "" || utf8.RuneCountInString(*d.Content) > 4096 {
			return clientEvent{}, fail("content must be 1 to 4096 characters")
		}
		ev.Edit = &editData{MessageID: id, Content: *d.Content}
	case "delete":
		var d struct {
			MessageID *string `json:"message_id"`
			Scope     string  `json:"scope"`
		}
		if err := decodeStrict(env.Data, &d); err != nil {
			return clientEvent{}, fail("data does not match the delete event")
		}
		id, err := parseID(d.MessageID, "message_id")
		if err != nil {
			return clientEvent{}, fail("%s", err)
		}
		if d.Scope != "me" && d.Scope != "everyone" {
			return clientEvent{}, fail("scope must be me or everyone")
		}
		ev.Delete = &deleteData{MessageID: id, Scope: d.Scope}
	case "reaction_add", "reaction_remove":
		var d struct {
			MessageID *string `json:"message_id"`
			Emoji     *string `json:"emoji"`
		}
		if err := decodeStrict(env.Data, &d); err != nil {
			return clientEvent{}, fail("data does not match the %s event", env.Type)
		}
		id, err := parseID(d.MessageID, "message_id")
		if err != nil {
			return clientEvent{}, fail("%s", err)
		}
		if d.Emoji == nil || *d.Emoji == "" || utf8.RuneCountInString(*d.Emoji) > 32 {
			return clientEvent{}, fail("emoji must be 1 to 32 characters")
		}
		ev.Reaction = &reactionData{MessageID: id, Emoji: *d.Emoji}
	case "typing":
		var d struct {
			IsTyping *bool `json:"is_typing"`
		}
		if err := decodeStrict(env.Data, &d); err != nil || d.IsTyping == nil {
			return clientEvent{}, fail("data does not match the typing event")
		}
		ev.Typing = &typingData{IsTyping: *d.IsTyping}
	default:
		return clientEvent{}, fail("unknown event type")
	}
	return ev, nil
}
