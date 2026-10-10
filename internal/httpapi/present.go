package httpapi

import (
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/scylla"
	"github.com/myronsi/messenger-back/internal/users"
)

// This file turns domain values into the contract's types. REST handlers and the realtime gateway share it,
// so a message looks the same whichever way it reaches a client.

// FormatID renders an id the way the contract sends ids: a decimal string.
func FormatID(id int64) Id { return strconv.FormatInt(id, 10) }

func formatIDPtr(id *int64) *Id {
	if id == nil {
		return nil
	}
	s := FormatID(*id)
	return &s
}

// UTC normalizes times for the wire.
func utc(t time.Time) time.Time { return t.UTC() }

// PresentUser renders a user as the viewer may see them.
func PresentUser(v users.View) User {
	u := User{
		Id: FormatID(v.ID), Username: v.Username, DisplayName: v.DisplayName, ContactName: v.ContactName,
		AvatarUrl: v.AvatarURL, Bio: v.Bio, IsOnline: v.IsOnline, IsDeleted: v.IsDeleted,
	}
	if v.LastSeen != nil {
		t := utc(*v.LastSeen)
		u.LastSeen = &t
	}
	return u
}

// AttachmentKindOf derives the kind shown to clients from the stored type and the message type.
func AttachmentKindOf(mime, messageType string) AttachmentKind {
	switch {
	case messageType == scylla.TypeVoice:
		return AttachmentKindVoice
	case strings.HasPrefix(mime, "image/"):
		return AttachmentKindImage
	case strings.HasPrefix(mime, "audio/"):
		return AttachmentKindAudio
	case strings.HasPrefix(mime, "video/"):
		return AttachmentKindVideo
	}
	return AttachmentKindFile
}

// PresentAttachment renders attachment metadata; the URL always points at the authenticated endpoint.
func PresentAttachment(a postgres.Attachment, messageType, basePath string) Attachment {
	id := a.ID.String()
	url := basePath + "/attachments/" + id + "/content"
	out := Attachment{
		Id: id, Kind: AttachmentKindOf(a.MimeType, messageType), ContentType: a.MimeType,
		Filename: "attachment", Size: int(a.Size), Url: &url,
	}
	if a.Width != nil {
		w := int(*a.Width)
		out.Width = &w
	}
	if a.Height != nil {
		h := int(*a.Height)
		out.Height = &h
	}
	if a.Duration != nil {
		ms := int(math.Round(*a.Duration * 1000))
		out.DurationMs = &ms
	}
	if len(a.Waveform) > 0 {
		wf := make([]int, len(a.Waveform))
		for i, v := range a.Waveform {
			wf[i] = int(v)
		}
		out.Waveform = &wf
	}
	return out
}

// PresentReaction renders one reaction.
func PresentReaction(r scylla.Reaction) Reaction {
	return Reaction{Emoji: r.Emoji, UserId: FormatID(r.UserID)}
}

// MessageParts is everything a message is rendered from besides the stored row.
type MessageParts struct {
	// Sender as the recipient sees them; nil for system messages.
	Sender     *users.View
	Attachment *postgres.Attachment
	Reactions  []scylla.Reaction
	ReadBy     []ReadReceipt
	// ClientTempID is set on the sender's own copy of a new message.
	ClientTempID string
	BasePath     string
}

// PresentMessage renders a stored message for one recipient.
func PresentMessage(m scylla.Message, p MessageParts) Message {
	out := Message{
		Id: FormatID(m.ID), ChatId: FormatID(m.ChatID), Type: MessageType(m.Type),
		Content: m.Content, ReplyTo: formatIDPtr(m.ReplyTo), CreatedAt: utc(m.CreatedAt),
		IsDeleted: m.Deleted, Reactions: make([]Reaction, 0, len(p.Reactions)), ReadBy: p.ReadBy,
	}
	if out.ReadBy == nil {
		out.ReadBy = []ReadReceipt{}
	}
	if p.Sender != nil {
		u := PresentUser(*p.Sender)
		out.Sender = &u
	}
	if m.EditedAt != nil {
		t := utc(*m.EditedAt)
		out.EditedAt = &t
	}
	if m.Deleted {
		out.Content = nil
	} else if p.Attachment != nil {
		a := PresentAttachment(*p.Attachment, m.Type, p.BasePath)
		out.Attachment = &a
	}
	if f := m.Forwarded; f != nil {
		out.ForwardedFrom = &ForwardedFrom{MessageId: FormatID(f.MessageID), SenderId: formatIDPtr(f.SenderID), SenderName: f.SenderName}
	}
	for _, r := range p.Reactions {
		out.Reactions = append(out.Reactions, PresentReaction(r))
	}
	if p.ClientTempID != "" {
		id := p.ClientTempID
		out.ClientTempId = &id
	}
	return out
}
