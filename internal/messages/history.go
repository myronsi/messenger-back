package messages

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/ids"
	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/scylla"
)

// MaxForwardTargets is how many chats one forward can reach.
const MaxForwardTargets = 20

// Locate returns the chat of a message the user can see (ErrNotFound otherwise, also when it is hidden for
// them or they are not in its chat).
func (s *Service) Locate(ctx context.Context, userID, messageID int64) (int64, error) {
	chatID, err := s.d.Messages.Locate(ctx, messageID)
	if errors.Is(err, scylla.ErrNotFound) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if err := s.requireMember(ctx, chatID, userID); err != nil {
		return 0, err
	}
	return chatID, nil
}

// History returns a page of a chat as the user sees it (messages hidden for them are left out).
func (s *Service) History(ctx context.Context, userID int64, q scylla.PageQuery) (scylla.Page, error) {
	n := 0
	for _, v := range []int64{q.Before, q.After, q.Around} {
		if v != 0 {
			n++
		}
	}
	if n > 1 {
		return scylla.Page{}, invalid("send at most one of before, after and around")
	}
	if err := s.requireMember(ctx, q.ChatID, userID); err != nil {
		return scylla.Page{}, err
	}
	q.Viewer = userID
	p, err := s.d.Messages.Page(ctx, q)
	if err != nil {
		return scylla.Page{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return p, nil
}

// Decorations is what rendering a page of messages for one viewer needs beyond the messages.
type Decorations struct {
	Reactions map[int64][]scylla.Reaction
	// Readers are the other members who share read receipts, with how far they read.
	Readers []postgres.ReadMarker
}

// Decorate loads the reactions of the messages and the read markers of the chat's other members who share
// read receipts.
func (s *Service) Decorate(ctx context.Context, userID, chatID int64, msgs []scylla.Message) (Decorations, error) {
	idList := make([]int64, 0, len(msgs))
	for _, m := range msgs {
		if !m.Deleted {
			idList = append(idList, m.ID)
		}
	}
	var d Decorations
	if len(idList) > 0 {
		r, err := s.d.Messages.Reactions(ctx, chatID, idList)
		if err != nil {
			return Decorations{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		d.Reactions = r
	}
	markers, err := s.d.Store.Chats().OtherMembers(ctx, userID, []int64{chatID})
	if err != nil {
		return Decorations{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	readers := make([]int64, 0, len(markers))
	for _, m := range markers {
		if m.MessageID != nil && m.At != nil {
			readers = append(readers, m.UserID)
		}
	}
	settings, err := s.d.Store.Social().Privacy(ctx, readers)
	if err != nil {
		return Decorations{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	for _, m := range markers {
		if m.MessageID != nil && m.At != nil && settings[m.UserID].ReadReceiptsEnabled {
			d.Readers = append(d.Readers, m)
		}
	}
	return d, nil
}

// ReadBy picks the readers of one message: those whose marker reached it, except its sender.
func (d Decorations) ReadBy(m scylla.Message) []postgres.ReadMarker {
	var out []postgres.ReadMarker
	for _, r := range d.Readers {
		if *r.MessageID >= m.ID && (m.SenderID == nil || *m.SenderID != r.UserID) {
			out = append(out, r)
		}
	}
	return out
}

// Forward copies a message the user can see into other chats they are in, one new message per chat. The copy
// keeps the type, text and file and names where it came from (the original of a forwarded message).
func (s *Service) Forward(ctx context.Context, userID, messageID int64, chatIDs []int64) ([]scylla.Message, error) {
	if len(chatIDs) == 0 || len(chatIDs) > MaxForwardTargets {
		return nil, invalid("chat_ids must name 1 to %d chats", MaxForwardTargets)
	}
	targets := slices.Clone(chatIDs)
	slices.Sort(targets)
	if len(slices.Compact(targets)) != len(chatIDs) {
		return nil, invalid("chat_ids must not repeat")
	}
	from, err := s.Locate(ctx, userID, messageID)
	if err != nil {
		return nil, err
	}
	if ok, err := s.Visible(ctx, userID, from, messageID); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	} else if !ok {
		return nil, ErrNotFound
	}
	src, err := s.message(ctx, from, messageID)
	if err != nil {
		return nil, err
	}
	if src.Type == scylla.TypeSystem || src.Deleted {
		return nil, invalid("this message cannot be forwarded")
	}
	origin := src.Forwarded
	if origin == nil {
		origin = &scylla.Forwarded{MessageID: src.ID, SenderID: src.SenderID, SenderName: s.senderName(ctx, src.SenderID)}
	}
	// Every target is checked before anything is written, so a refused target forwards nothing.
	type target struct {
		chatID  int64
		members []int64
	}
	var checked []target
	for _, chatID := range chatIDs {
		if err := s.requireMember(ctx, chatID, userID); err != nil {
			return nil, err
		}
		if err := s.requireNotBlocked(ctx, userID, chatID); err != nil {
			return nil, err
		}
		members, err := s.Members(ctx, chatID)
		if err != nil {
			return nil, err
		}
		checked = append(checked, target{chatID, members})
	}
	out := make([]scylla.Message, 0, len(checked))
	sender := userID
	for _, t := range checked {
		id, err := s.d.IDs.Next()
		if err != nil {
			return out, fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		m := scylla.Message{
			ChatID: t.chatID, ID: id, SenderID: &sender, Type: src.Type, Content: src.Content,
			AttachmentID: src.AttachmentID, Forwarded: origin, CreatedAt: ids.Time(id),
		}
		if m.AttachmentID != nil {
			if err := s.d.Store.Attachments().Link(ctx, uuid.UUID(*m.AttachmentID), t.chatID, id); err != nil {
				return out, fmt.Errorf("%w: %w", ErrUnavailable, err)
			}
		}
		if err := s.d.Messages.Insert(ctx, m); err != nil {
			return out, fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		s.delivered(ctx, m, t.members, "")
		out = append(out, m)
	}
	return out, nil
}

// senderName is the display name a forwarded copy shows for the original sender.
func (s *Service) senderName(ctx context.Context, senderID *int64) string {
	if senderID == nil {
		return ""
	}
	u, err := s.d.Store.Users().Get(ctx, *senderID)
	if err != nil {
		return "Deleted account"
	}
	return u.DisplayName
}

// MediaKinds are the attachment kinds of each media list.
var MediaKinds = map[string][]string{
	"image": {"image"},
	"audio": {"audio", "voice"},
}

// Media lists the messages of a chat with attachments of the kinds, newest first, before a message id (0:
// from the newest), as the user sees them. more tells whether older ones exist.
func (s *Service) Media(ctx context.Context, userID, chatID int64, kinds []string, before int64, limit int) (msgs []scylla.Message, more bool, err error) {
	if err := s.requireMember(ctx, chatID, userID); err != nil {
		return nil, false, err
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	cursor := before
	for len(msgs) < limit {
		// Links of messages that are deleted or hidden for the user are skipped, so read a little ahead.
		links, err := s.d.Store.Attachments().ListLinked(ctx, chatID, kinds, cursor, limit+1)
		if err != nil {
			return nil, false, fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		for _, l := range links {
			cursor = l.MessageID
			ok, err := s.Visible(ctx, userID, chatID, l.MessageID)
			if err != nil {
				return nil, false, fmt.Errorf("%w: %w", ErrUnavailable, err)
			}
			if !ok {
				continue
			}
			if len(msgs) == limit {
				return msgs, true, nil
			}
			m, err := s.message(ctx, chatID, l.MessageID)
			if err != nil {
				return nil, false, err
			}
			msgs = append(msgs, m)
		}
		if len(links) <= limit {
			return msgs, false, nil
		}
	}
	return msgs, true, nil
}
