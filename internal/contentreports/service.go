package contentreports

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/messages"
	"github.com/myronsi/messenger-back/internal/moderation"
	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/scylla"
	"github.com/myronsi/messenger-back/internal/users"
)

const (
	MaxNote   = 1000
	Retention = 6
	Illegal   = "illegal"
)

func Reason(r string) bool {
	return r == Illegal || moderation.Reasons[r]
}

type Messages interface {
	ReportWindow(ctx context.Context, userID, chatID, messageID int64) (messages.ReportWindow, error)
	IsMember(ctx context.Context, chatID, userID int64) (bool, error)
}

type Store interface {
	ContentReports() postgres.ContentReportRepository
	Users() postgres.UserRepository
	Attachments() postgres.AttachmentRepository
	Chats() postgres.ChatRepository
	Polls() postgres.PollRepository
}

type Metrics interface {
	ContentReport(kind, reason string)
}

type Deps struct {
	Store    Store
	Messages Messages
	Metrics  Metrics
	Log      *slog.Logger
	Now      func() time.Time
}

type Service struct {
	d Deps
}

func New(d Deps) *Service {
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Service{d: d}
}

type Copy struct {
	Chat     CopyChat      `json:"chat"`
	Messages []CopyMessage `json:"messages,omitempty"`
	Group    *CopyGroup    `json:"group,omitempty"`
}

type CopyChat struct {
	ID    string  `json:"id"`
	Type  string  `json:"type"`
	Title *string `json:"title,omitempty"`
}

type CopyMessage struct {
	ID                string        `json:"id"`
	SenderID          *string       `json:"sender_id"`
	SenderName        string        `json:"sender_name"`
	SenderUsername    string        `json:"sender_username,omitempty"`
	Type              string        `json:"type"`
	Text              *string       `json:"text"`
	Files             []string      `json:"files"`
	ForwardedFromName *string       `json:"forwarded_from_name,omitempty"`
	ReplyTo           *string       `json:"reply_to,omitempty"`
	SentAt            time.Time     `json:"sent_at"`
	EditedAt          *time.Time    `json:"edited_at,omitempty"`
	Reported          bool          `json:"reported"`
	Poll              *CopyPoll     `json:"poll,omitempty"`
	Location          *CopyLocation `json:"location,omitempty"`
	Contact           *CopyContact  `json:"contact,omitempty"`
}

type CopyPoll struct {
	Question        string           `json:"question"`
	Options         []CopyPollOption `json:"options"`
	MultipleAnswers bool             `json:"multiple_answers"`
	Anonymous       bool             `json:"anonymous"`
	Quiz            bool             `json:"quiz"`
	Closed          bool             `json:"closed"`
}

type CopyPollOption struct {
	Text  string `json:"text"`
	Votes int    `json:"votes"`
}

type CopyLocation struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Title     *string `json:"title,omitempty"`
}

type CopyContact struct {
	UserID   string `json:"user_id"`
	Name     string `json:"name"`
	Username string `json:"username,omitempty"`
}

type CopyGroup struct {
	Title              string  `json:"title"`
	Description        string  `json:"description"`
	AvatarAttachmentID *string `json:"avatar_attachment_id"`
	CreatedBy          *string `json:"created_by"`
	OwnerID            *string `json:"owner_id"`
	MemberCount        int     `json:"member_count"`
}

func id(v int64) string { return strconv.FormatInt(v, 10) }

func idPtr(v *int64) *string {
	if v == nil {
		return nil
	}
	s := id(*v)
	return &s
}

func unavailable(err error) error { return fmt.Errorf("%w: %w", moderation.ErrUnavailable, err) }

func fromMessages(err error) error {
	switch {
	case errors.Is(err, messages.ErrNotFound):
		return moderation.ErrNotFound
	case errors.Is(err, messages.ErrUnavailable):
		return unavailable(err)
	}
	return err
}

func cleanNote(reason string, note *string) (*string, error) {
	if !Reason(reason) {
		return nil, &moderation.FieldError{Field: "reason", Message: "unknown reason"}
	}
	var t string
	if note != nil {
		t = strings.TrimSpace(*note)
	}
	if t == "" {
		if reason == Illegal {
			return nil, &moderation.FieldError{Field: "note", Message: "explain why the content is illegal"}
		}
		return nil, nil
	}
	if utf8.RuneCountInString(t) > MaxNote {
		return nil, &moderation.FieldError{Field: "note", Message: "at most 1000 characters"}
	}
	if !utf8.ValidString(t) || strings.IndexFunc(t, func(r rune) bool { return unicode.IsControl(r) && r != '\n' }) >= 0 {
		return nil, &moderation.FieldError{Field: "note", Message: "no control characters"}
	}
	return &t, nil
}

func (s *Service) ReportMessage(ctx context.Context, reporter, chatID, messageID int64, reason string, note *string) error {
	note, err := cleanNote(reason, note)
	if err != nil {
		return err
	}
	w, err := s.d.Messages.ReportWindow(ctx, reporter, chatID, messageID)
	if err != nil {
		return fromMessages(err)
	}
	if w.Target.Type == scylla.TypeSystem {
		return &moderation.FieldError{Field: "message_id", Message: "system messages cannot be reported"}
	}
	if w.Target.SenderID != nil && *w.Target.SenderID == reporter {
		return &moderation.FieldError{Field: "message_id", Message: "you cannot report your own message"}
	}
	chat, err := s.d.Store.Chats().Get(ctx, chatID)
	if errors.Is(err, postgres.ErrNotFound) {
		return moderation.ErrNotFound
	}
	if err != nil {
		return unavailable(err)
	}
	all := make([]scylla.Message, 0, len(w.Before)+len(w.After)+1)
	all = append(all, w.Before...)
	all = append(all, w.Target)
	all = append(all, w.After...)
	copied, err := s.copyMessages(ctx, all, messageID)
	if err != nil {
		return err
	}
	c := Copy{Chat: CopyChat{ID: id(chat.ID), Type: string(chat.Type)}, Messages: copied}
	if chat.Type != postgres.ChatDirect {
		c.Chat.Title = chat.Name
	}
	return s.save(ctx, postgres.ContentReport{
		ReporterID: reporter, Kind: postgres.ContentReportMessage, ChatID: chatID, MessageID: messageID,
		TargetID: w.Target.SenderID, Reason: reason, Note: note,
	}, c)
}

func (s *Service) ReportGroup(ctx context.Context, reporter, chatID int64, reason string, note *string) error {
	note, err := cleanNote(reason, note)
	if err != nil {
		return err
	}
	member, err := s.d.Messages.IsMember(ctx, chatID, reporter)
	if err != nil {
		return unavailable(err)
	}
	if !member {
		return moderation.ErrNotFound
	}
	chat, err := s.d.Store.Chats().Get(ctx, chatID)
	if errors.Is(err, postgres.ErrNotFound) {
		return moderation.ErrNotFound
	}
	if err != nil {
		return unavailable(err)
	}
	if chat.Type != postgres.ChatGroup {
		return moderation.ErrNotFound
	}
	members, err := s.d.Store.Chats().Participants(ctx, chatID)
	if err != nil {
		return unavailable(err)
	}
	g := &CopyGroup{Description: chat.Description, CreatedBy: idPtr(chat.CreatedBy), MemberCount: len(members)}
	if chat.Name != nil {
		g.Title = *chat.Name
	}
	if chat.AvatarAttachmentID != nil {
		a := chat.AvatarAttachmentID.String()
		g.AvatarAttachmentID = &a
	}
	var owner *int64
	for _, p := range members {
		if p.Role == postgres.RoleOwner {
			o := p.UserID
			owner = &o
			break
		}
	}
	g.OwnerID = idPtr(owner)
	c := Copy{Chat: CopyChat{ID: id(chat.ID), Type: string(chat.Type), Title: chat.Name}, Group: g}
	return s.save(ctx, postgres.ContentReport{
		ReporterID: reporter, Kind: postgres.ContentReportGroup, ChatID: chatID, TargetID: owner, Reason: reason, Note: note,
	}, c)
}

func (s *Service) save(ctx context.Context, r postgres.ContentReport, c Copy) error {
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	now := s.d.Now()
	r.Content, r.CreatedAt = raw, now
	_, inserted, err := s.d.Store.ContentReports().Save(ctx, r, now.Add(-24*time.Hour), moderation.MaxReportsPerDay)
	if errors.Is(err, postgres.ErrLimit) {
		return moderation.ErrRateLimited
	}
	if errors.Is(err, postgres.ErrNotFound) {
		return moderation.ErrNotFound
	}
	if err != nil {
		return unavailable(err)
	}
	if inserted && s.d.Metrics != nil {
		s.d.Metrics.ContentReport(r.Kind, r.Reason)
	}
	return nil
}

func (s *Service) copyMessages(ctx context.Context, msgs []scylla.Message, reported int64) ([]CopyMessage, error) {
	var senderIDs []int64
	var files []uuid.UUID
	for i, m := range msgs {
		if m.SenderID == nil && m.Import != nil && m.Type != scylla.TypeSystem {
			msgs[i].SenderID = m.Import.ImportedBy
			m = msgs[i]
		}
		if m.SenderID != nil {
			senderIDs = append(senderIDs, *m.SenderID)
		}
		for _, a := range m.AttachmentIDs() {
			files = append(files, uuid.UUID(a))
		}
	}
	people, err := s.d.Store.Users().GetMany(ctx, append(senderIDs, messages.ContactUserIDs(msgs...)...))
	if err != nil {
		return nil, unavailable(err)
	}
	polls, err := s.d.Store.Polls().Polls(ctx, messages.PollIDs(msgs...))
	if err != nil {
		return nil, unavailable(err)
	}
	attachments := map[uuid.UUID]postgres.Attachment{}
	if len(files) > 0 {
		if attachments, err = s.d.Store.Attachments().GetMany(ctx, files); err != nil {
			return nil, unavailable(err)
		}
	}
	out := make([]CopyMessage, 0, len(msgs))
	for _, m := range msgs {
		c := CopyMessage{
			ID: id(m.ID), SenderID: idPtr(m.SenderID), Type: m.Type, Text: m.Content, Files: []string{},
			ReplyTo: idPtr(m.ReplyTo), SentAt: m.CreatedAt.UTC(), Reported: m.ID == reported,
		}
		if m.EditedAt != nil {
			t := m.EditedAt.UTC()
			c.EditedAt = &t
		}
		if m.SenderID != nil {
			if u, ok := people[*m.SenderID]; ok {
				c.SenderName, c.SenderUsername = u.DisplayName, u.Username
			} else {
				c.SenderName = users.DeletedName
			}
		}
		if m.Forwarded != nil {
			name := m.Forwarded.SenderName
			c.ForwardedFromName = &name
		}
		for _, a := range m.AttachmentIDs() {
			if f, ok := attachments[uuid.UUID(a)]; ok {
				c.Files = append(c.Files, f.Filename)
			}
		}
		copyKind(&c, m, people, polls, s.d.Now())
		out = append(out, c)
	}
	return out, nil
}

func (s *Service) Withdraw(ctx context.Context, reporter, chatID, messageID int64) error {
	if _, err := s.d.Store.ContentReports().Withdraw(ctx, reporter, chatID, messageID, s.d.Now()); err != nil {
		return unavailable(err)
	}
	return nil
}

func (s *Service) Purge(ctx context.Context) (int64, error) {
	return s.d.Store.ContentReports().Purge(ctx, s.d.Now().AddDate(0, -Retention, 0))
}
