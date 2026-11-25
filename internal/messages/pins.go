package messages

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/myronsi/messenger-back/internal/emoji"
	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/scylla"
)

const (
	MaxSharedPins   = 500
	MaxPersonalPins = 500
	pinSnippetRunes = 60

	SystemMessagePinned   = "message_pinned"
	SystemMessageUnpinned = "message_unpinned"

	PinActionPinned   = "pinned"
	PinActionUnpinned = "unpinned"
)

var ErrLimit = errors.New("limit reached")

type Pin struct {
	Message             scylla.Message
	Scope               string
	PinnedBy            int64
	PinnedAt            time.Time
	CanUnpinForEveryone bool
}

type PinChange struct {
	ChatID    int64
	MessageID int64
	Action    string
	Scope     string
	ActorID   int64
	At        time.Time
	Count     *int
}

type PinPage struct {
	Pins []Pin
	Next *postgres.PinCursor
}

type pinContext struct {
	chat    postgres.Chat
	members []int64
	roles   map[int64]postgres.Role
	request bool
}

func validPinScope(scope string) error {
	if scope != ScopeMe && scope != ScopeEveryone {
		return invalid("scope must be me or everyone")
	}
	return nil
}

func (s *Service) Pin(ctx context.Context, userID, chatID, messageID int64, scope string) (Pin, error) {
	if err := validPinScope(scope); err != nil {
		return Pin{}, err
	}
	if err := s.requireMember(ctx, chatID, userID); err != nil {
		return Pin{}, err
	}
	m, err := s.message(ctx, chatID, messageID)
	if err != nil {
		return Pin{}, err
	}
	if m.Deleted {
		return Pin{}, ErrNotFound
	}
	if err := s.requireNotHidden(ctx, userID, m); err != nil {
		return Pin{}, err
	}
	if m.Type == scylla.TypeSystem {
		return Pin{}, invalid("system messages cannot be pinned")
	}
	pc, err := s.pinContext(ctx, chatID)
	if err != nil {
		return Pin{}, err
	}
	if scope == ScopeEveryone {
		if err := s.requireMayPinForEveryone(ctx, userID, pc); err != nil {
			return Pin{}, err
		}
	}
	pins := s.d.Store.MessagePins()
	var p postgres.MessagePin
	var created, unhid bool
	if scope == ScopeEveryone {
		p, created, unhid, err = pins.PinForEveryone(ctx, chatID, messageID, userID, MaxSharedPins)
	} else {
		p, unhid, err = pins.PinForMe(ctx, userID, chatID, messageID, MaxPersonalPins)
	}
	if err != nil {
		return Pin{}, pinStoreError(err, scope)
	}
	switch {
	case created:
		seeing := s.withoutHidden(ctx, chatID, messageID, pc.members)
		for _, member := range pc.members {
			if !slices.Contains(seeing, member) {
				s.hidePins(ctx, member, chatID, []int64{messageID})
			}
		}
		s.pinChanged(ctx, PinChange{ChatID: chatID, MessageID: messageID, Action: PinActionPinned, Scope: ScopeEveryone, ActorID: userID, At: p.PinnedAt}, seeing)
		s.logPin(ctx, chatID, userID, SystemMessagePinned, m)
	case unhid:
		s.pinChanged(ctx, PinChange{ChatID: chatID, MessageID: messageID, Action: PinActionPinned, Scope: ScopeMe, ActorID: userID, At: p.PinnedAt}, []int64{userID})
	}
	return Pin{Message: m, Scope: p.Scope, PinnedBy: p.PinnedBy, PinnedAt: p.PinnedAt, CanUnpinForEveryone: p.Scope == ScopeEveryone && mayUnpin(pc, userID, p.PinnedBy)}, nil
}

func (s *Service) Unpin(ctx context.Context, userID, chatID, messageID int64, scope string) error {
	if err := validPinScope(scope); err != nil {
		return err
	}
	if err := s.requireMember(ctx, chatID, userID); err != nil {
		return err
	}
	pins := s.d.Store.MessagePins()
	if scope == ScopeMe {
		changed, err := pins.UnpinForMe(ctx, userID, chatID, messageID)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		if changed {
			s.pinChanged(ctx, PinChange{ChatID: chatID, MessageID: messageID, Action: PinActionUnpinned, Scope: ScopeMe, ActorID: userID, At: s.now().UTC()}, []int64{userID})
		}
		return nil
	}
	shared, err := pins.SharedPin(ctx, chatID, messageID)
	if errors.Is(err, postgres.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	pc, err := s.pinContext(ctx, chatID)
	if err != nil {
		return err
	}
	if !mayUnpin(pc, userID, shared.PinnedBy) {
		return ErrForbidden
	}
	_, removed, err := pins.UnpinForEveryone(ctx, chatID, messageID)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if !removed {
		return nil
	}
	s.pinChanged(ctx, PinChange{ChatID: chatID, MessageID: messageID, Action: PinActionUnpinned, Scope: ScopeEveryone, ActorID: userID, At: s.now().UTC()}, pc.members)
	if m, err := s.message(ctx, chatID, messageID); err == nil && !m.Deleted {
		s.logPin(ctx, chatID, userID, SystemMessageUnpinned, m)
	}
	return nil
}

func (s *Service) Pins(ctx context.Context, userID, chatID int64, after *postgres.PinCursor, limit int) (PinPage, error) {
	if err := s.requireMember(ctx, chatID, userID); err != nil {
		return PinPage{}, err
	}
	rows, err := s.d.Store.MessagePins().VisiblePins(ctx, userID, chatID, after, limit+1)
	if err != nil {
		return PinPage{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	var page PinPage
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		page.Next = &postgres.PinCursor{PinnedAt: last.PinnedAt, MessageID: last.MessageID}
	}
	if len(rows) == 0 {
		return page, nil
	}
	ids := make([]int64, len(rows))
	for i, r := range rows {
		ids[i] = r.MessageID
	}
	found, err := s.d.Messages.GetMany(ctx, chatID, ids)
	if err != nil {
		return PinPage{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	pc, err := s.pinContext(ctx, chatID)
	if err != nil {
		return PinPage{}, err
	}
	page.Pins = make([]Pin, 0, len(rows))
	for _, r := range rows {
		m, ok := found[r.MessageID]
		if !ok || m.Deleted {
			continue
		}
		page.Pins = append(page.Pins, Pin{
			Message: m, Scope: r.Scope, PinnedBy: r.PinnedBy, PinnedAt: r.PinnedAt,
			CanUnpinForEveryone: r.Scope == ScopeEveryone && mayUnpin(pc, userID, r.PinnedBy),
		})
	}
	return page, nil
}

func (s *Service) PinCounts(ctx context.Context, userID int64, chatIDs []int64) map[int64]int {
	counts, err := s.d.Store.MessagePins().VisiblePinCounts(ctx, userID, chatIDs)
	if err != nil {
		s.d.Log.WarnContext(ctx, "pin counts", "error", err)
		return map[int64]int{}
	}
	return counts
}

func (s *Service) pinContext(ctx context.Context, chatID int64) (pinContext, error) {
	chat, err := s.d.Store.Chats().Get(ctx, chatID)
	if errors.Is(err, postgres.ErrNotFound) {
		return pinContext{}, ErrNotFound
	}
	if err != nil {
		return pinContext{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	ps, err := s.d.Store.Chats().Participants(ctx, chatID)
	if err != nil {
		return pinContext{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	pc := pinContext{chat: chat, members: make([]int64, len(ps)), roles: make(map[int64]postgres.Role, len(ps))}
	for i, p := range ps {
		pc.members[i] = p.UserID
		pc.roles[p.UserID] = p.Role
		pc.request = pc.request || p.IsRequest
	}
	return pc, nil
}

func (s *Service) requireMayPinForEveryone(ctx context.Context, userID int64, pc pinContext) error {
	if pc.chat.Type != postgres.ChatDirect {
		if roleRank(pc.roles[userID]) == 0 {
			return ErrForbidden
		}
		return nil
	}
	if len(pc.members) < 2 || pc.request {
		return ErrForbidden
	}
	if err := s.checkNotBlocked(ctx, userID, pc.members); err != nil {
		if errors.Is(err, ErrBlocked) {
			return ErrForbidden
		}
		return err
	}
	return nil
}

func mayUnpin(pc pinContext, userID, pinnedBy int64) bool {
	if pc.chat.Type == postgres.ChatDirect {
		return true
	}
	rank := roleRank(pc.roles[userID])
	return rank > 0 && rank >= roleRank(pc.roles[pinnedBy])
}

func roleRank(r postgres.Role) int {
	switch r {
	case postgres.RoleOwner:
		return 3
	case postgres.RoleAdmin:
		return 2
	case postgres.RoleModerator:
		return 1
	}
	return 0
}

func pinStoreError(err error, scope string) error {
	if errors.Is(err, postgres.ErrLimit) {
		if scope == ScopeEveryone {
			return fmt.Errorf("%w: at most %d messages can be pinned for everyone in a chat", ErrLimit, MaxSharedPins)
		}
		return fmt.Errorf("%w: at most %d messages can be pinned for you in a chat", ErrLimit, MaxPersonalPins)
	}
	if errors.Is(err, postgres.ErrNotFound) {
		return ErrNotFound
	}
	return fmt.Errorf("%w: %w", ErrUnavailable, err)
}

func (s *Service) pinChanged(ctx context.Context, c PinChange, recipients []int64) {
	if c.Scope == ScopeMe && len(recipients) == 1 {
		if counts, err := s.d.Store.MessagePins().VisiblePinCounts(ctx, recipients[0], []int64{c.ChatID}); err == nil {
			n := counts[c.ChatID]
			c.Count = &n
		}
	}
	s.d.Notifier.PinChanged(ctx, c, recipients)
	change := postgres.SyncChange{ChatID: c.ChatID, Kind: postgres.SyncPins, MessageID: c.MessageID}
	if c.Scope == ScopeMe && len(recipients) == 1 {
		change.UserID = recipients[0]
	}
	s.recordChange(ctx, change)
}

func (s *Service) logPin(ctx context.Context, chatID, actorID int64, kind string, m scylla.Message) {
	name := ""
	if found, err := s.d.Store.Users().GetMany(ctx, []int64{actorID}); err == nil {
		if u, ok := found[actorID]; ok {
			name = u.DisplayName
			if name == "" {
				name = u.Username
			}
		}
	}
	id := m.ID
	e := SystemEvent{Kind: kind, ActorID: &actorID, ActorName: name, MessageID: &id, Snippet: pinSnippet(m), MessageType: m.Type}
	if _, err := s.SendSystem(ctx, chatID, e); err != nil {
		s.d.Log.WarnContext(ctx, "pin log", "error", err, "chat_id", chatID, "kind", kind)
	}
}

func snippetSource(m scylla.Message) string {
	switch m.Type {
	case scylla.TypeContact:
		return ""
	case scylla.TypeLocation:
		if l, ok := ParseLocation(m); ok && l.Title != nil {
			return *l.Title
		}
		return ""
	}
	if m.Content == nil {
		return ""
	}
	if m.Type == scylla.TypePoll {
		return strings.TrimPrefix(*m.Content, pollSummary(""))
	}
	return *m.Content
}

func pinSnippet(m scylla.Message) string {
	source := snippetSource(m)
	if source == "" {
		return ""
	}
	text := strings.Join(strings.Fields(emoji.Plain(source)), " ")
	if utf8.RuneCountInString(text) <= pinSnippetRunes {
		return text
	}
	return string([]rune(text)[:pinSnippetRunes]) + "…"
}

func (s *Service) forgetPins(ctx context.Context, chatID int64, messageIDs []int64) {
	if _, err := s.d.Store.MessagePins().RemoveMessagePins(context.WithoutCancel(ctx), chatID, messageIDs); err != nil {
		s.d.Log.WarnContext(ctx, "remove pins", "error", err, "chat_id", chatID)
	}
}

func (s *Service) hidePins(ctx context.Context, userID, chatID int64, messageIDs []int64) {
	if err := s.d.Store.MessagePins().HideMessagePins(context.WithoutCancel(ctx), userID, chatID, messageIDs); err != nil {
		s.d.Log.WarnContext(ctx, "hide pins", "error", err, "chat_id", chatID)
	}
}
