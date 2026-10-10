// Package chats holds the chat list, direct chats, chat requests, pinning and read state.
package chats

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sync/singleflight"

	"github.com/myronsi/messenger-back/internal/events"
	"github.com/myronsi/messenger-back/internal/messages"
	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/scylla"
	"github.com/myronsi/messenger-back/internal/users"
)

// The errors are those of the message service, so both map to the same answers.
var (
	ErrNotFound    = messages.ErrNotFound
	ErrForbidden   = messages.ErrForbidden
	ErrBlocked     = messages.ErrBlocked
	ErrInvalid     = messages.ErrInvalid
	ErrUnavailable = messages.ErrUnavailable
	// ErrConflict: too many pinned chats, or a request that was answered already.
	ErrConflict = errors.New("conflict")
	// ErrBadCursor: a cursor that this service did not make.
	ErrBadCursor = errors.New("bad cursor")
)

// MaxPins is how many chats a user can pin.
const MaxPins = 10

// unreadCap matches the message service: clients show "999+".
const unreadCap = 999

// lastMessageWorkers bounds the parallel reads of last messages for one chat list page, and of unread counts
// when a user's counters are rebuilt.
const lastMessageWorkers = 16

// Store is the PostgreSQL side.
type Store interface {
	Chats() postgres.ChatRepository
	Social() postgres.SocialRepository
	Users() postgres.UserRepository
	Approvals() postgres.ApprovalRepository
	Attachments() postgres.AttachmentRepository
}

// MessageStore is the part of the message store the chat list reads.
type MessageStore interface {
	Page(ctx context.Context, q scylla.PageQuery) (scylla.Page, error)
	CountAfter(ctx context.Context, chatID, viewer, afterID int64, limit int) (int, error)
}

// Unread keeps the unread counters.
type Unread interface {
	Counts(ctx context.Context, userID int64, rebuild func(context.Context) (map[int64]int64, error)) (map[int64]int64, error)
	Get(ctx context.Context, userID int64) (counts map[int64]int64, built bool, err error)
	Forget(ctx context.Context, chatID int64, userIDs ...int64) error
}

// Members is the membership cache.
type Members interface {
	Invalidate(ctx context.Context, chatIDs ...int64) error
}

// Sender sends messages (messages.Service).
type Sender interface {
	Send(ctx context.Context, r messages.SendRequest) (messages.Sent, error)
	Read(ctx context.Context, userID, chatID, messageID int64) (int, error)
}

// Notifier delivers chat list changes to the users' sockets (realtime.Fanout).
type Notifier interface {
	// ChatCreated: a chat appeared in the users' lists (each gets the chat as they see it).
	ChatCreated(ctx context.Context, chatID int64, views map[int64]View)
	// ChatUpdated: something about the chat changed for the users (chat_list_update).
	ChatUpdated(ctx context.Context, chatID int64, views map[int64]View)
	// ChatRemoved: the chat is gone from the users' lists (chat_deleted).
	ChatRemoved(ctx context.Context, chatID int64, userIDs []int64)
	// RequestCreated: a new request in the recipient's inbox.
	RequestCreated(ctx context.Context, r RequestView)
}

// EventLog appends domain events.
type EventLog interface {
	Emit(ctx context.Context, e events.Event) error
}

// Deps are the collaborators of the service.
type Deps struct {
	Store     Store
	Messages  MessageStore
	Unread    Unread
	Members   Members
	Sender    Sender
	Directory *users.Directory
	Notifier  Notifier
	Events    EventLog
	// Storage deletes the files of deleted chats (keys from the chat repository). Optional.
	DeleteFile func(ctx context.Context, key string) error
	Log        *slog.Logger
}

// Service implements the chat use cases.
type Service struct {
	d Deps
	// rebuilds runs one rebuild of a user's unread counters at a time; concurrent readers share it.
	rebuilds singleflight.Group
}

// New returns the service.
func New(d Deps) *Service {
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	return &Service{d: d}
}

func badCursor() error { return fmt.Errorf("%w: the cursor is not one of ours", ErrBadCursor) }

// LastMessage is the newest message of a chat the viewer can see, with what rendering it needs.
type LastMessage struct {
	Message    scylla.Message
	Sender     *users.View
	Attachment *postgres.Attachment
	// ReadBy are the other members who read it (direct chats, with read receipts on).
	ReadBy []postgres.ReadMarker
}

// View is a chat as one user sees it in their list.
type View struct {
	Entry postgres.ChatEntry
	// Peer is the other user of a direct chat.
	Peer   *users.View
	Unread int64
	Last   *LastMessage
}

// RequestView is an approval request with the requester as the recipient sees them.
type RequestView struct {
	Request   postgres.ApprovalRequest
	Requester users.View
	GroupName *string
}

func unavailable(err error) error { return fmt.Errorf("%w: %w", ErrUnavailable, err) }

func notFoundOr(err error) error {
	if errors.Is(err, postgres.ErrNotFound) {
		return ErrNotFound
	}
	return unavailable(err)
}

// ---------------------------------------------------------------- cursors

// EncodeCursor makes the opaque cursor of a chat list position.
func EncodeCursor(c postgres.ChatCursor) string {
	p := "1"
	if !c.Unpinned {
		p = "0"
	}
	raw := p + "." + strconv.FormatInt(c.SortAt.UnixMicro(), 10) + "." + strconv.FormatInt(c.ChatID, 10)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// DecodeCursor reads a cursor of EncodeCursor.
func DecodeCursor(s string) (postgres.ChatCursor, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return postgres.ChatCursor{}, badCursor()
	}
	parts := strings.Split(string(b), ".")
	if len(parts) != 3 || (parts[0] != "0" && parts[0] != "1") {
		return postgres.ChatCursor{}, badCursor()
	}
	us, err1 := strconv.ParseInt(parts[1], 10, 64)
	id, err2 := strconv.ParseInt(parts[2], 10, 64)
	if err1 != nil || err2 != nil {
		return postgres.ChatCursor{}, badCursor()
	}
	return postgres.ChatCursor{Unpinned: parts[0] == "1", SortAt: time.UnixMicro(us), ChatID: id}, nil
}

// ---------------------------------------------------------------- the chat list

// List returns a page of the user's chats and the cursor of the next page ("" at the end).
func (s *Service) List(ctx context.Context, userID int64, after string, limit int) ([]View, string, error) {
	var cur *postgres.ChatCursor
	if after != "" {
		c, err := DecodeCursor(after)
		if err != nil {
			return nil, "", err
		}
		cur = &c
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	entries, err := s.d.Store.Chats().Entries(ctx, userID, cur, limit+1)
	if err != nil {
		return nil, "", unavailable(err)
	}
	next := ""
	if len(entries) > limit {
		entries = entries[:limit]
		next = EncodeCursor(entries[limit-1].CursorOf())
	}
	views, err := s.render(ctx, userID, entries)
	if err != nil {
		return nil, "", err
	}
	return views, next, nil
}

// Get returns one chat of the user.
func (s *Service) Get(ctx context.Context, userID, chatID int64) (View, error) {
	e, err := s.d.Store.Chats().Entry(ctx, userID, chatID)
	if err != nil {
		return View{}, notFoundOr(err)
	}
	views, err := s.render(ctx, userID, []postgres.ChatEntry{e})
	if err != nil {
		return View{}, err
	}
	return views[0], nil
}

// viewsFor renders one chat for each of the users (for chat_created and chat_list_update events).
func (s *Service) viewsFor(ctx context.Context, chatID int64, userIDs []int64) map[int64]View {
	out := make(map[int64]View, len(userIDs))
	for _, uid := range userIDs {
		v, err := s.Get(ctx, uid, chatID)
		if err != nil {
			s.d.Log.WarnContext(ctx, "render chat for an event", "error", err, "chat_id", chatID)
			continue
		}
		out[uid] = v
	}
	return out
}

// render completes the entries: peers, unread counts and last messages.
func (s *Service) render(ctx context.Context, viewer int64, entries []postgres.ChatEntry) ([]View, error) {
	views := make([]View, len(entries))
	var direct []int64
	for i, e := range entries {
		views[i].Entry = e
		if e.Chat.Type == postgres.ChatDirect {
			direct = append(direct, e.Chat.ID)
		}
	}
	// Peers and their read markers (direct chats only: a group's list entry carries no receipts).
	markers, err := s.d.Store.Chats().OtherMembers(ctx, viewer, direct)
	if err != nil {
		return nil, unavailable(err)
	}
	peerOf := map[int64]postgres.ReadMarker{}
	ids := []int64{}
	for _, m := range markers {
		peerOf[m.ChatID] = m
		ids = append(ids, m.UserID)
	}

	counts, err := s.unreadCounts(ctx, viewer, entries)
	if err != nil {
		s.d.Log.WarnContext(ctx, "unread counts", "error", err)
		counts = nil // the list is still useful without them
	}

	// Last messages, read in parallel.
	lasts := make([]*scylla.Message, len(entries))
	var wg sync.WaitGroup
	sem := make(chan struct{}, lastMessageWorkers)
	var mu sync.Mutex
	var firstErr error
	for i, e := range entries {
		if e.Chat.LastMessageID == nil {
			continue
		}
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			page, err := s.d.Messages.Page(ctx, scylla.PageQuery{ChatID: e.Chat.ID, Viewer: viewer, Limit: 1})
			if err != nil {
				mu.Lock()
				firstErr = err
				mu.Unlock()
				return
			}
			if len(page.Messages) > 0 {
				m := page.Messages[len(page.Messages)-1]
				lasts[i] = &m
			}
		})
	}
	wg.Wait()
	if firstErr != nil {
		s.d.Log.WarnContext(ctx, "last messages", "error", firstErr)
	}
	for _, m := range lasts {
		if m != nil && m.SenderID != nil {
			ids = append(ids, *m.SenderID)
		}
	}
	people, err := s.d.Directory.ForViewer(ctx, viewer, dedupe(ids))
	if err != nil {
		return nil, unavailable(err)
	}

	receipts := s.receiptsFor(ctx, viewer, entries, lasts, peerOf)
	for i := range views {
		e := views[i].Entry
		if m, ok := peerOf[e.Chat.ID]; ok {
			p := people[m.UserID]
			views[i].Peer = &p
		}
		// The counters only count up between reads; the contract stops at 999.
		views[i].Unread = min(counts[e.Chat.ID], unreadCap)
		m := lasts[i]
		if m == nil {
			continue
		}
		last := &LastMessage{Message: *m}
		if m.SenderID != nil {
			v := people[*m.SenderID]
			last.Sender = &v
		}
		if m.AttachmentID != nil && !m.Deleted {
			if a, err := s.d.Store.Attachments().Get(ctx, uuid.UUID(*m.AttachmentID)); err == nil {
				last.Attachment = &a
			}
		}
		if r, ok := receipts[e.Chat.ID]; ok {
			last.ReadBy = []postgres.ReadMarker{r}
		}
		views[i].Last = last
	}
	return views, nil
}

// receiptsFor returns, per direct chat, the peer's receipt for the viewer's own last message: when the peer
// read that far, shares read receipts and did not block the viewer. Two queries for the whole page.
func (s *Service) receiptsFor(ctx context.Context, viewer int64, entries []postgres.ChatEntry, lasts []*scylla.Message, peerOf map[int64]postgres.ReadMarker) map[int64]postgres.ReadMarker {
	candidates := map[int64]postgres.ReadMarker{}
	var peers []int64
	for i, e := range entries {
		m, pm := lasts[i], peerOf[e.Chat.ID]
		if m == nil || m.SenderID == nil || *m.SenderID != viewer || pm.MessageID == nil || pm.At == nil || *pm.MessageID < m.ID {
			continue
		}
		candidates[e.Chat.ID] = pm
		peers = append(peers, pm.UserID)
	}
	if len(candidates) == 0 {
		return nil
	}
	settings, err := s.d.Store.Social().Privacy(ctx, peers)
	if err != nil {
		return nil
	}
	blocks, err := s.d.Store.Social().Blocks(ctx, peers, []int64{viewer})
	if err != nil {
		return nil
	}
	out := map[int64]postgres.ReadMarker{}
	for chatID, pm := range candidates {
		if settings[pm.UserID].ReadReceiptsEnabled && !blocks[[2]int64{pm.UserID, viewer}] {
			out[chatID] = pm
		}
	}
	return out
}

// unreadCounts returns the viewer's unread counts. One chat reads its own counter (or counts that chat) instead
// of rebuilding every counter of the user.
func (s *Service) unreadCounts(ctx context.Context, viewer int64, entries []postgres.ChatEntry) (map[int64]int64, error) {
	if len(entries) == 1 {
		counts, built, err := s.d.Unread.Get(ctx, viewer)
		if err != nil {
			return nil, err
		}
		if built {
			return counts, nil
		}
		n, err := s.countOne(ctx, viewer, entries[0])
		if err != nil {
			return nil, err
		}
		return map[int64]int64{entries[0].Chat.ID: n}, nil
	}
	return s.d.Unread.Counts(ctx, viewer, func(ctx context.Context) (map[int64]int64, error) {
		// Concurrent requests of the same user share one rebuild.
		v, err, _ := s.rebuilds.Do(strconv.FormatInt(viewer, 10), func() (any, error) { return s.rebuildUnread(ctx, viewer) })
		if err != nil {
			return nil, err
		}
		return v.(map[int64]int64), nil
	})
}

// countOne counts the unread messages of one chat from the message store.
func (s *Service) countOne(ctx context.Context, userID int64, e postgres.ChatEntry) (int64, error) {
	if e.Chat.LastMessageID == nil {
		return 0, nil
	}
	from := int64(0)
	if e.LastReadMessageID != nil {
		from = *e.LastReadMessageID
	}
	if from >= *e.Chat.LastMessageID {
		return 0, nil
	}
	n, err := s.d.Messages.CountAfter(ctx, e.Chat.ID, userID, from, unreadCap)
	return int64(n), err
}

// rebuildUnread counts the unread messages of every chat of the user from the message store, a few chats at
// a time.
func (s *Service) rebuildUnread(ctx context.Context, userID int64) (map[int64]int64, error) {
	out := map[int64]int64{}
	var mu sync.Mutex
	var firstErr error
	var cur *postgres.ChatCursor
	for {
		entries, err := s.d.Store.Chats().Entries(ctx, userID, cur, 500)
		if err != nil {
			return nil, err
		}
		var wg sync.WaitGroup
		sem := make(chan struct{}, lastMessageWorkers)
		for _, e := range entries {
			wg.Go(func() {
				sem <- struct{}{}
				defer func() { <-sem }()
				n, err := s.countOne(ctx, userID, e)
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					firstErr = err
				} else if n > 0 {
					out[e.Chat.ID] = n
				}
			})
		}
		wg.Wait()
		if firstErr != nil {
			return nil, firstErr
		}
		if len(entries) < 500 {
			return out, nil
		}
		c := entries[len(entries)-1].CursorOf()
		cur = &c
	}
}

func dedupe(ids []int64) []int64 {
	seen := make(map[int64]bool, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// ---------------------------------------------------------------- direct chats

// Created is the result of CreateDirect: the chat, or the request that waits for the other user.
type Created struct {
	Chat    *View
	New     bool
	Request *RequestView
}

// permission decides whether requester may open a direct chat with recipient: allowed, approval needed or
// denied. Blocks deny; otherwise the recipient's direct_messages setting decides.
func (s *Service) permission(ctx context.Context, requester, recipient int64) (allowed, approval bool, err error) {
	social := s.d.Store.Social()
	blocked, err := social.BlockedEither(ctx, requester, recipient)
	if err != nil || blocked {
		return false, false, err
	}
	settings, err := social.Privacy(ctx, []int64{recipient})
	if err != nil {
		return false, false, err
	}
	switch settings[recipient].DirectMessages {
	case postgres.VisibleEveryone:
		return true, false, nil
	case postgres.VisibleSharedChats:
		shared, err := social.SharedChatPartners(ctx, recipient, []int64{requester})
		return shared[requester], false, err
	case postgres.WaitApproval:
		return false, true, nil
	}
	return false, false, nil
}

// CreateDirect opens a direct chat with another user, honouring their direct_messages setting. An existing
// chat is returned as it is (New false); when the other user wants to approve first, a request is made.
func (s *Service) CreateDirect(ctx context.Context, requester, otherID int64, initial *string) (Created, error) {
	if otherID == requester {
		return Created{}, fmt.Errorf("%w: a direct chat needs another user", ErrInvalid)
	}
	if initial != nil {
		t := strings.TrimSpace(*initial)
		if t == "" {
			initial = nil
		} else if len([]rune(t)) > messages.MaxContent {
			return Created{}, fmt.Errorf("%w: initial_message is too long", ErrInvalid)
		} else {
			initial = &t
		}
	}
	if _, err := s.d.Store.Users().Get(ctx, otherID); err != nil {
		return Created{}, notFoundOr(err)
	}
	if existing, err := s.existingDirect(ctx, requester, otherID); err != nil {
		return Created{}, err
	} else if existing != nil {
		blocked, err := s.d.Store.Social().BlockedEither(ctx, requester, otherID)
		if err != nil {
			return Created{}, unavailable(err)
		}
		if blocked {
			return Created{}, ErrBlocked
		}
		v, err := s.Get(ctx, requester, *existing)
		if err != nil {
			return Created{}, err
		}
		return Created{Chat: &v}, nil
	}
	allowed, approval, err := s.permission(ctx, requester, otherID)
	if err != nil {
		return Created{}, unavailable(err)
	}
	if approval {
		req, isNew, err := s.d.Store.Approvals().RequestDirect(ctx, requester, otherID, initial)
		if errors.Is(err, postgres.ErrConflict) {
			// The other user opened the chat meanwhile (approved an earlier request of ours, or asked us).
			existing, err := s.existingDirect(ctx, requester, otherID)
			if err != nil || existing == nil {
				return Created{}, unavailable(fmt.Errorf("chat vanished: %w", err))
			}
			v, err := s.Get(ctx, requester, *existing)
			if err != nil {
				return Created{}, err
			}
			return Created{Chat: &v}, nil
		}
		if err != nil {
			return Created{}, unavailable(err)
		}
		// The requester gets the request with themselves as they see themselves; the recipient's inbox shows
		// the requester as the recipient sees them (contact name, privacy).
		mine, err := s.requestView(ctx, requester, req)
		if err != nil {
			return Created{}, err
		}
		if isNew {
			if theirs, err := s.requestView(ctx, otherID, req); err == nil {
				s.d.Notifier.RequestCreated(ctx, theirs)
			}
		}
		return Created{Request: &mine}, nil
	}
	if !allowed {
		return Created{}, ErrForbidden
	}
	chat, created, closed, err := s.d.Store.Chats().OpenDirect(ctx, requester, otherID)
	if err != nil {
		return Created{}, unavailable(err)
	}
	first := []firstMessage{}
	if initial != nil {
		first = append(first, firstMessage{from: requester, text: *initial, tempID: "initial"})
	}
	return s.opened(ctx, chat, created, requester, otherID, append(first, fromRequests(closed)...))
}

// firstMessage is a message that opens a chat: the creator's initial message or the message of a request.
type firstMessage struct {
	from   int64
	text   string
	tempID string
}

func fromRequests(reqs []postgres.ApprovalRequest) []firstMessage {
	var out []firstMessage
	for _, r := range reqs {
		if r.MessageText != nil && strings.TrimSpace(*r.MessageText) != "" {
			out = append(out, firstMessage{from: r.RequesterID, text: *r.MessageText, tempID: "request:" + strconv.FormatInt(r.ID, 10)})
		}
	}
	return out
}

// opened finishes opening a direct chat: events for a new chat (before its first messages, so clients know the
// chat when the messages arrive), the first messages, and the view of `from`. The messages are sent even when
// the chat existed already (a concurrent opening won): a client_temp_id per message makes that idempotent.
func (s *Service) opened(ctx context.Context, chat postgres.Chat, created bool, from, to int64, first []firstMessage) (Created, error) {
	if created {
		if err := s.d.Members.Invalidate(ctx, chat.ID); err != nil {
			s.d.Log.WarnContext(ctx, "invalidate members", "error", err)
		}
		s.emit(ctx, events.Event{Type: events.ChatMemberAdded, ChatID: chat.ID, UserID: from, ActorID: from})
		s.emit(ctx, events.Event{Type: events.ChatMemberAdded, ChatID: chat.ID, UserID: to, ActorID: from})
		s.d.Notifier.ChatCreated(ctx, chat.ID, s.viewsFor(ctx, chat.ID, []int64{from, to}))
	}
	for _, m := range first {
		text := m.text
		if _, err := s.d.Sender.Send(ctx, messages.SendRequest{
			ChatID: chat.ID, SenderID: m.from, ClientTempID: m.tempID + ":" + strconv.FormatInt(chat.ID, 10),
			Type: scylla.TypeText, Content: &text,
		}); err != nil {
			s.d.Log.WarnContext(ctx, "first message of a new chat", "error", err, "chat_id", chat.ID)
		}
	}
	v, err := s.Get(ctx, from, chat.ID)
	if err != nil {
		return Created{}, err
	}
	return Created{Chat: &v, New: created}, nil
}

// existingDirect returns the pair's chat if there is one.
func (s *Service) existingDirect(ctx context.Context, a, b int64) (*int64, error) {
	c, err := s.d.Store.Chats().DirectBetween(ctx, a, b)
	if errors.Is(err, postgres.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, unavailable(err)
	}
	return &c.ID, nil
}

// Delete deletes a direct chat for both users (as v1 did); groups are deleted or left with the group
// endpoints.
func (s *Service) Delete(ctx context.Context, userID, chatID int64) error {
	e, err := s.d.Store.Chats().Entry(ctx, userID, chatID)
	if err != nil {
		return notFoundOr(err)
	}
	if e.Chat.Type != postgres.ChatDirect {
		return fmt.Errorf("%w: delete or leave a group with the group endpoints", ErrInvalid)
	}
	ps, err := s.d.Store.Chats().Participants(ctx, chatID)
	if err != nil {
		return unavailable(err)
	}
	members := make([]int64, len(ps))
	for i, p := range ps {
		members[i] = p.UserID
	}
	keys, err := s.d.Store.Chats().Delete(ctx, chatID)
	if err != nil {
		return notFoundOr(err)
	}
	s.d.Notifier.ChatRemoved(ctx, chatID, members)
	if err := s.d.Unread.Forget(ctx, chatID, members...); err != nil {
		s.d.Log.WarnContext(ctx, "forget unread", "error", err)
	}
	if err := s.d.Members.Invalidate(ctx, chatID); err != nil {
		s.d.Log.WarnContext(ctx, "invalidate members", "error", err)
	}
	s.emit(ctx, events.Event{Type: events.ChatDeleted, ChatID: chatID, ActorID: userID, At: time.Now()})
	if s.d.DeleteFile != nil {
		for _, k := range keys {
			if err := s.d.DeleteFile(context.WithoutCancel(ctx), k); err != nil {
				s.d.Log.WarnContext(ctx, "delete file of a deleted chat", "error", err)
			}
		}
	}
	return nil
}

// ---------------------------------------------------------------- pins and reading

// Pin pins the chat for the user (at most MaxPins).
func (s *Service) Pin(ctx context.Context, userID, chatID int64) error {
	err := s.d.Store.Chats().Pin(ctx, userID, chatID, MaxPins)
	switch {
	case errors.Is(err, postgres.ErrConflict):
		return fmt.Errorf("%w: at most %d chats can be pinned", ErrConflict, MaxPins)
	case err != nil:
		return notFoundOr(err)
	}
	s.d.Notifier.ChatUpdated(ctx, chatID, s.viewsFor(ctx, chatID, []int64{userID}))
	return nil
}

// Unpin unpins the chat for the user.
func (s *Service) Unpin(ctx context.Context, userID, chatID int64) error {
	if _, err := s.d.Store.Chats().Entry(ctx, userID, chatID); err != nil {
		return notFoundOr(err)
	}
	if err := s.d.Store.Chats().Unpin(ctx, userID, chatID); err != nil {
		return unavailable(err)
	}
	s.d.Notifier.ChatUpdated(ctx, chatID, s.viewsFor(ctx, chatID, []int64{userID}))
	return nil
}

// MarkRead marks the chat as read up to the message and returns the unread count left.
func (s *Service) MarkRead(ctx context.Context, userID, chatID, messageID int64) (int, error) {
	return s.d.Sender.Read(ctx, userID, chatID, messageID)
}

// ---------------------------------------------------------------- requests

func (s *Service) requestView(ctx context.Context, viewer int64, r postgres.ApprovalRequest) (RequestView, error) {
	people, err := s.d.Directory.ForViewer(ctx, viewer, []int64{r.RequesterID})
	if err != nil {
		return RequestView{}, unavailable(err)
	}
	rv := RequestView{Request: r, Requester: people[r.RequesterID]}
	if r.ChatID != nil {
		if c, err := s.d.Store.Chats().Get(ctx, *r.ChatID); err == nil {
			rv.GroupName = c.Name
		}
	}
	return rv, nil
}

// Requests is the user's inbox of pending requests, newest first; the cursor is the last request id.
func (s *Service) Requests(ctx context.Context, userID int64, after string, limit int) ([]RequestView, string, error) {
	before := int64(0)
	if after != "" {
		id, err := strconv.ParseInt(after, 10, 64)
		if err != nil || id <= 0 {
			return nil, "", badCursor()
		}
		before = id
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	reqs, err := s.d.Store.Approvals().Pending(ctx, userID, before, limit+1)
	if err != nil {
		return nil, "", unavailable(err)
	}
	next := ""
	if len(reqs) > limit {
		reqs = reqs[:limit]
		next = strconv.FormatInt(reqs[limit-1].ID, 10)
	}
	ids := make([]int64, len(reqs))
	for i, r := range reqs {
		ids[i] = r.RequesterID
	}
	people, err := s.d.Directory.ForViewer(ctx, userID, dedupe(ids))
	if err != nil {
		return nil, "", unavailable(err)
	}
	out := make([]RequestView, len(reqs))
	for i, r := range reqs {
		out[i] = RequestView{Request: r, Requester: people[r.RequesterID]}
	}
	return out, next, nil
}

func requestError(err error) error {
	if errors.Is(err, postgres.ErrConflict) {
		return fmt.Errorf("%w: the request was answered already", ErrConflict)
	}
	return notFoundOr(err)
}

// Approve accepts a direct-message request: the chat opens with the request's message as its first message.
func (s *Service) Approve(ctx context.Context, userID, requestID int64) (View, error) {
	req, chat, created, closed, err := s.d.Store.Approvals().ApproveDirect(ctx, requestID, userID)
	if err != nil {
		return View{}, requestError(err)
	}
	// The first messages are the requests' own: the requester asked with them.
	if _, err := s.opened(ctx, chat, created, req.RequesterID, req.RecipientID, fromRequests(append([]postgres.ApprovalRequest{req}, closed...))); err != nil {
		return View{}, err
	}
	// The answer is the chat as the recipient sees it.
	return s.Get(ctx, userID, chat.ID)
}

// Reject turns a request down; the requester is not told.
func (s *Service) Reject(ctx context.Context, userID, requestID int64) error {
	if _, err := s.d.Store.Approvals().Reject(ctx, requestID, userID); err != nil {
		return requestError(err)
	}
	return nil
}

func (s *Service) emit(ctx context.Context, e events.Event) {
	if s.d.Events == nil {
		return
	}
	if err := s.d.Events.Emit(context.WithoutCancel(ctx), e); err != nil {
		s.d.Log.WarnContext(ctx, "emit event", "type", e.Type, "error", err)
	}
}
