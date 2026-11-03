package messages

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"

	"github.com/myronsi/messenger-back/internal/ids"
	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/scylla"
	"github.com/myronsi/messenger-back/internal/users"
)

// MaxForwardTargets is how many chats one forward can reach.
const MaxForwardTargets = 20

const (
	MaxForwardMessages = 20
	MaxForwardCopies   = 100
	MaxDeleteMessages  = 100
)

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
	Replies map[int64]scylla.Message
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
	d := Decorations{Replies: s.replyOrigins(ctx, userID, chatID, msgs)}
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

const replyLookups = 8

func (s *Service) replyOrigins(ctx context.Context, userID, chatID int64, msgs []scylla.Message) map[int64]scylla.Message {
	page := make(map[int64]scylla.Message, len(msgs))
	for _, m := range msgs {
		page[m.ID] = m
	}
	out := map[int64]scylla.Message{}
	var missing []int64
	for _, m := range msgs {
		if m.Deleted || m.ReplyTo == nil {
			continue
		}
		if o, ok := page[*m.ReplyTo]; ok {
			out[o.ID] = o
		} else if !slices.Contains(missing, *m.ReplyTo) {
			missing = append(missing, *m.ReplyTo)
		}
	}
	var mu sync.Mutex
	var g errgroup.Group
	g.SetLimit(replyLookups)
	for _, id := range missing {
		g.Go(func() error {
			o, err := s.d.Messages.Get(ctx, chatID, id)
			if err != nil {
				return nil
			}
			if hidden, err := s.d.Messages.Hidden(ctx, userID, chatID, id); err != nil || hidden {
				return nil
			}
			mu.Lock()
			out[id] = o
			mu.Unlock()
			return nil
		})
	}
	_ = g.Wait()
	return out
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
	if err := checkIDs("chat_ids", chatIDs, MaxForwardTargets); err != nil {
		return nil, err
	}
	from, err := s.LocateVisible(ctx, userID, messageID)
	if err != nil {
		return nil, err
	}
	return s.ForwardMany(ctx, userID, from, []int64{messageID}, chatIDs)
}

func checkIDs(field string, list []int64, maxLen int) error {
	if len(list) == 0 || len(list) > maxLen {
		return invalid("%s must name 1 to %d items", field, maxLen)
	}
	sorted := slices.Clone(list)
	slices.Sort(sorted)
	if len(slices.Compact(sorted)) != len(list) {
		return invalid("%s must not repeat", field)
	}
	return nil
}

type forwardSource struct {
	msg    scylla.Message
	origin *scylla.Forwarded
}

func (s *Service) forwardSources(ctx context.Context, userID, chatID int64, messageIDs []int64) ([]forwardSource, error) {
	ordered := slices.Clone(messageIDs)
	slices.Sort(ordered)
	out := make([]forwardSource, 0, len(ordered))
	for _, messageID := range ordered {
		ok, err := s.Visible(ctx, userID, chatID, messageID)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		if !ok {
			return nil, ErrNotFound
		}
		src, err := s.message(ctx, chatID, messageID)
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
		out = append(out, forwardSource{src, origin})
	}
	return out, nil
}

func (s *Service) ForwardMany(ctx context.Context, userID, chatID int64, messageIDs, chatIDs []int64) ([]scylla.Message, error) {
	if err := checkIDs("message_ids", messageIDs, MaxForwardMessages); err != nil {
		return nil, err
	}
	if err := checkIDs("chat_ids", chatIDs, MaxForwardTargets); err != nil {
		return nil, err
	}
	if len(messageIDs)*len(chatIDs) > MaxForwardCopies {
		return nil, invalid("one forward makes at most %d copies", MaxForwardCopies)
	}
	if err := s.requireMember(ctx, chatID, userID); err != nil {
		return nil, err
	}
	sources, err := s.forwardSources(ctx, userID, chatID, messageIDs)
	if err != nil {
		return nil, err
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
	out := make([]scylla.Message, 0, len(checked)*len(sources))
	for _, t := range checked {
		for _, src := range sources {
			m, err := s.forwardCopy(ctx, userID, t.chatID, t.members, src)
			if err != nil {
				return out, err
			}
			out = append(out, m)
		}
	}
	return out, nil
}

func (s *Service) forwardCopy(ctx context.Context, sender, chatID int64, members []int64, src forwardSource) (scylla.Message, error) {
	id, err := s.d.IDs.Next()
	if err != nil {
		return scylla.Message{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	m := scylla.Message{
		ChatID: chatID, ID: id, SenderID: &sender, Type: src.msg.Type, Content: src.msg.Content,
		AttachmentID: src.msg.AttachmentID, Forwarded: src.origin, CreatedAt: ids.Time(id),
	}
	if m.AttachmentID != nil {
		if err := s.d.Store.Attachments().Link(ctx, uuid.UUID(*m.AttachmentID), chatID, id); err != nil {
			return scylla.Message{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
	}
	if err := s.queueNotification(ctx, m, members); err != nil {
		return scylla.Message{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if err := s.d.Messages.Insert(ctx, m); err != nil {
		return scylla.Message{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	s.delivered(ctx, m, members, "")
	return m, nil
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
