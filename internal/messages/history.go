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
	"github.com/myronsi/messenger-back/internal/users"
)

// MaxForwardTargets is how many chats one forward can reach.
const MaxForwardTargets = 20

// LocateVisible returns the chat of a message the user can see: they are in its chat, it is not deleted for
// everyone and not hidden for them (ErrNotFound otherwise).
func (s *Service) LocateVisible(ctx context.Context, userID, messageID int64) (int64, error) {
	chatID, err := s.Locate(ctx, userID, messageID)
	if err != nil {
		return 0, err
	}
	ok, err := s.Visible(ctx, userID, chatID, messageID)
	if err != nil {
		return 0, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if !ok {
		return 0, ErrNotFound
	}
	return chatID, nil
}

// Locate returns the chat of a message in a chat the user is in (ErrNotFound otherwise). The message may be
// hidden for them; LocateVisible also checks that.
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
	if errors.Is(err, scylla.ErrNotFound) {
		return scylla.Page{}, ErrNotFound // around a message that is not in this chat
	}
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

// Decorate loads the reactions of the messages and, in a direct chat, the read marker of the other user when
// they share read receipts and did not block the viewer. Groups carry no read_by: their member lists can be
// long, and every page would load all of them.
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
	chat, err := s.d.Store.Chats().Get(ctx, chatID)
	if err != nil {
		return Decorations{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if chat.Type != postgres.ChatDirect {
		return d, nil
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
	blocks, err := s.d.Store.Social().Blocks(ctx, readers, []int64{userID})
	if err != nil {
		return Decorations{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	for _, m := range markers {
		if m.MessageID != nil && m.At != nil && settings[m.UserID].ReadReceiptsEnabled && !blocks[[2]int64{m.UserID, userID}] {
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
// keeps the type, text and file and names where it came from (the original of a forwarded message). When a
// store fails midway, the copies written so far are returned with the error.
func (s *Service) Forward(ctx context.Context, userID, messageID int64, chatIDs []int64) ([]scylla.Message, error) {
	if len(chatIDs) == 0 || len(chatIDs) > MaxForwardTargets {
		return nil, invalid("chat_ids must name 1 to %d chats", MaxForwardTargets)
	}
	targets := slices.Clone(chatIDs)
	slices.Sort(targets)
	if len(slices.Compact(targets)) != len(chatIDs) {
		return nil, invalid("chat_ids must not repeat")
	}
	from, err := s.LocateVisible(ctx, userID, messageID)
	if err != nil {
		return nil, err
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
		name, err := s.senderName(ctx, src.SenderID)
		if err != nil {
			return nil, err
		}
		origin = &scylla.Forwarded{MessageID: src.ID, SenderID: src.SenderID, SenderName: name}
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

// senderName is the display name a forwarded copy keeps for the original sender (it is stored with the copy,
// so a lookup that failed must not turn into "Deleted account").
func (s *Service) senderName(ctx context.Context, senderID *int64) (string, error) {
	if senderID == nil {
		return "", nil
	}
	u, err := s.d.Store.Users().Get(ctx, *senderID)
	if errors.Is(err, postgres.ErrNotFound) {
		return users.DeletedName, nil
	}
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return u.DisplayName, nil
}

// MediaKinds are the attachment kinds of each media list.
var MediaKinds = map[string][]string{
	"image": {"image"},
	"audio": {"audio", "voice"},
}

// MediaItem is a message of a media list with its file.
type MediaItem struct {
	Message    scylla.Message
	Attachment postgres.Attachment
}

// mediaScan bounds the links one media page looks at, so links of many messages hidden for the user cannot
// make a request read the whole chat; the page then ends early and the cursor continues the scan.
const mediaScan = 5

// Media lists the messages of a chat with attachments of the kinds, newest first, before a message id (0:
// from the newest), as the user sees them. next is the cursor of the following page (0 at the end); it is the
// position of the scan, so a page can hold fewer items than limit.
func (s *Service) Media(ctx context.Context, userID, chatID int64, kinds []string, before int64, limit int) (items []MediaItem, next int64, err error) {
	if err := s.requireMember(ctx, chatID, userID); err != nil {
		return nil, 0, err
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	links, err := s.d.Store.Attachments().ListLinked(ctx, chatID, kinds, before, limit*mediaScan+1)
	if err != nil {
		return nil, 0, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	more := len(links) > limit*mediaScan
	if more {
		links = links[:limit*mediaScan]
	}
	for i, l := range links {
		if len(items) == limit {
			return items, links[i-1].MessageID, nil
		}
		m, err := s.message(ctx, chatID, l.MessageID)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, 0, err
		}
		if m.Deleted {
			continue
		}
		hidden, err := s.d.Messages.Hidden(ctx, userID, chatID, l.MessageID)
		if err != nil {
			return nil, 0, fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		if !hidden {
			items = append(items, MediaItem{Message: m, Attachment: l.Attachment})
		}
	}
	if more && len(links) > 0 {
		return items, links[len(links)-1].MessageID, nil
	}
	return items, 0, nil
}
