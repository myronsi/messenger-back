package httpapi

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/media"
	"github.com/myronsi/messenger-back/internal/messages"
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

// KindVoiceString is the stored kind of a voice message upload.
const KindVoiceString = "voice"

// PresentMe renders the caller's own account.
func PresentMe(u postgres.User, basePath string) Me {
	id := FormatID(u.ID)
	me := Me{Id: id, Username: u.Username, DisplayName: u.DisplayName, Bio: u.Bio, CreatedAt: utc(u.CreatedAt), IsOnline: true}
	if !u.LastSeenAt.IsZero() {
		seen := utc(u.LastSeenAt)
		me.LastSeen = &seen
	}
	if u.HasAvatar() {
		path := users.AvatarPath(basePath, u)
		me.AvatarUrl = &path
	}
	return me
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
	url := basePath + "/attachments/" + a.ID.String() + "/content"
	out := Attachment{
		Id: a.ID, Kind: AttachmentKindOf(a.MimeType, messageType), ContentType: a.MimeType,
		Filename: a.Filename, Size: int(a.Size), Url: &url,
	}
	if a.Kind == KindVoiceString {
		out.Kind = AttachmentKindVoice
	}
	if a.ThumbnailKey != nil || out.Kind == AttachmentKindVideo {
		thumb := url + "?variant=thumbnail"
		out.ThumbnailUrl = &thumb
	}
	if media.HasDisplay(a) {
		display := url + "?variant=display"
		out.DisplayUrl = &display
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
	Album      []postgres.Attachment
	Reactions  []scylla.Reaction
	ReadBy     []ReadReceipt
	// ClientTempID is set on the sender's own copy of a new message.
	ClientTempID string
	BasePath     string
	Reply        *ReplyOrigin
	Drawings     map[uuid.UUID][]byte
}

type ReplyOrigin struct {
	Message    scylla.Message
	Sender     *users.View
	Attachment *postgres.Attachment
	Drawing    []byte
}

func GroupDrawings(found []postgres.LinkDrawing) map[int64]map[uuid.UUID][]byte {
	out := make(map[int64]map[uuid.UUID][]byte)
	for _, d := range found {
		if out[d.MessageID] == nil {
			out[d.MessageID] = make(map[uuid.UUID][]byte)
		}
		out[d.MessageID][d.AttachmentID] = d.Drawing
	}
	return out
}

func withDrawing(a Attachment, raw []byte) Attachment {
	if len(raw) == 0 {
		return a
	}
	var d Drawing
	if err := json.Unmarshal(raw, &d); err == nil {
		a.Drawing = &d
	}
	return a
}

const replyExcerpt = 100

func PresentReplyPreview(o ReplyOrigin, basePath string) ReplyPreview {
	m := o.Message
	out := ReplyPreview{Id: FormatID(m.ID), Type: MessageType(m.Type), IsDeleted: m.Deleted}
	if o.Sender != nil {
		u := PresentUser(*o.Sender)
		out.Sender = &u
	}
	if m.Deleted || m.Type == scylla.TypeSystem {
		return out
	}
	if m.Content != nil && *m.Content != "" {
		text := []rune(*m.Content)
		excerpt := string(text[:min(len(text), replyExcerpt)])
		out.Content = &excerpt
	}
	if o.Attachment != nil {
		a := withDrawing(PresentAttachment(*o.Attachment, m.Type, basePath), o.Drawing)
		out.Attachment = &a
	}
	return out
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
	if m.Deleted || m.Type == scylla.TypeFile && m.Content != nil && *m.Content == "" {
		out.Content = nil
	}
	if !m.Deleted && p.Attachment != nil {
		a := withDrawing(PresentAttachment(*p.Attachment, m.Type, p.BasePath), p.Drawings[p.Attachment.ID])
		out.Attachment = &a
	}
	if !m.Deleted && len(p.Album) > 0 {
		album := make([]Attachment, 0, len(p.Album))
		for _, a := range p.Album {
			album = append(album, withDrawing(PresentAttachment(a, m.Type, p.BasePath), p.Drawings[a.ID]))
		}
		out.Attachment = &album[0]
		if len(album) > 1 {
			out.Attachments = &album
		}
	}
	if p.Reply != nil && m.ReplyTo != nil && !m.Deleted {
		r := PresentReplyPreview(*p.Reply, p.BasePath)
		out.ReplyPreview = &r
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
	if e, ok := messages.ParseSystemEvent(m); ok {
		out.Content = nil
		out.SystemEvent = presentSystemEvent(e)
	}
	return out
}

func presentSystemEvent(e messages.SystemEvent) *SystemEvent {
	out := &SystemEvent{Kind: SystemEventKind(e.Kind), ActorId: formatIDPtr(e.ActorID), ActorName: e.ActorName}
	if e.TargetID != nil {
		out.TargetId = formatIDPtr(e.TargetID)
		name := e.TargetName
		out.TargetName = &name
	}
	if c := e.Call; c != nil {
		out.Call = &CallSummary{CallId: FormatID(c.CallID), Kind: CallSummaryKind(c.Kind), Outcome: CallEndReason(c.Outcome), DurationSeconds: c.DurationSeconds}
	}
	return out
}
