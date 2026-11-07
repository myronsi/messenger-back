package chats

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/events"
	"github.com/myronsi/messenger-back/internal/messages"
	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/users"
)

// Group limits of the contract.
const (
	MaxGroupName        = 64
	MaxGroupDescription = 500
	MaxGroupInitial     = 200
)

// Member is a group member as one viewer sees them.
type Member struct {
	User     users.View
	Role     postgres.Role
	JoinedAt time.Time
}

// GroupView is a group as one member sees it.
type GroupView struct {
	Chat    postgres.Chat
	OwnerID int64
	MyRole  postgres.Role
	Members []Member
}

// GroupNotifier delivers group changes (realtime.Fanout).
type GroupNotifier interface {
	// GroupCreated: the users are now in the group (new group, added, invitation accepted).
	GroupCreated(ctx context.Context, chatID int64, views map[int64]GroupView)
	// GroupUpdated: something about the group changed for its members.
	GroupUpdated(ctx context.Context, chatID int64, views map[int64]GroupView)
}

func groupError(err error) error {
	switch {
	case errors.Is(err, postgres.ErrForbidden), errors.Is(err, postgres.ErrNotOwner):
		return ErrForbidden
	case errors.Is(err, postgres.ErrOwnerMustTransfer):
		return fmt.Errorf("%w: the owner has to transfer ownership first", ErrConflict)
	case errors.Is(err, postgres.ErrAlreadyParticipant):
		return fmt.Errorf("%w: already a member", ErrConflict)
	case errors.Is(err, postgres.ErrInvalid):
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return notFoundOr(err)
}

func cleanName(name string) (string, error) {
	n := strings.TrimSpace(name)
	if n == "" || utf8.RuneCountInString(n) > MaxGroupName || !utf8.ValidString(n) {
		return "", fmt.Errorf("%w: name must be 1 to %d characters", ErrInvalid, MaxGroupName)
	}
	return n, nil
}

func cleanDescription(d *string) (*string, error) {
	if d == nil {
		return nil, nil
	}
	t := strings.TrimSpace(*d)
	if utf8.RuneCountInString(t) > MaxGroupDescription || !utf8.ValidString(t) {
		return nil, fmt.Errorf("%w: description must be at most %d characters", ErrInvalid, MaxGroupDescription)
	}
	if t == "" {
		return nil, nil
	}
	return &t, nil
}

// invitePermission decides whether actor may add target to a group: allowed, approval needed or denied, by
// the target's group_invites setting, as v1 did: an allow exception lets the actor in; otherwise the setting
// decides, a deny exception counting as "not allowed" (so wait_approval still asks). Blocks deny.
func (s *Service) invitePermission(ctx context.Context, actor, target int64) (allowed, approval bool, err error) {
	social := s.d.Store.Social()
	blocked, err := social.BlockedEither(ctx, actor, target)
	if err != nil || blocked {
		return false, false, err
	}
	exc, err := social.ExceptionsOf(ctx, target, []int64{actor})
	if err != nil {
		return false, false, err
	}
	effect := exc[actor][postgres.SettingGroupInvites]
	if effect == postgres.EffectAllow {
		return true, false, nil
	}
	settings, err := social.Privacy(ctx, []int64{target})
	if err != nil {
		return false, false, err
	}
	switch v := settings[target].GroupInvites; {
	case v == postgres.WaitApproval:
		return false, true, nil
	case effect == postgres.EffectDeny:
		return false, false, nil
	case v == postgres.VisibleSharedChats:
		shared, err := social.SharedChatViewers(ctx, target, []int64{actor})
		return shared[actor], false, err
	default:
		return users.Visible(v, "", false), false, nil
	}
}

// group renders a group for one member.
func (s *Service) group(ctx context.Context, viewer, chatID int64) (GroupView, error) {
	views, err := s.groupViews(ctx, chatID, []int64{viewer}, true)
	if err != nil {
		return GroupView{}, err
	}
	v, ok := views[viewer]
	if !ok {
		return GroupView{}, ErrNotFound
	}
	return v, nil
}

// groupViews renders the group for each of the viewers who are members, with the member list when withMembers
// (rendered for each viewer: one directory lookup per viewer).
func (s *Service) groupViews(ctx context.Context, chatID int64, viewers []int64, withMembers bool) (map[int64]GroupView, error) {
	chat, err := s.d.Store.Chats().Get(ctx, chatID)
	if err != nil {
		return nil, notFoundOr(err)
	}
	if chat.Type != postgres.ChatGroup {
		return nil, ErrNotFound
	}
	ps, err := s.d.Store.Chats().Participants(ctx, chatID)
	if err != nil {
		return nil, unavailable(err)
	}
	ids := make([]int64, len(ps))
	roles := make(map[int64]postgres.Role, len(ps))
	owner := int64(0)
	for i, p := range ps {
		ids[i], roles[p.UserID] = p.UserID, p.Role
		if p.Role == postgres.RoleOwner {
			owner = p.UserID
		}
	}
	out := make(map[int64]GroupView, len(viewers))
	for _, v := range viewers {
		role, member := roles[v]
		if !member {
			continue
		}
		g := GroupView{Chat: chat, OwnerID: owner, MyRole: role, Members: []Member{}}
		if withMembers {
			people, err := s.d.Directory.ForViewer(ctx, v, ids)
			if err != nil {
				return nil, unavailable(err)
			}
			g.Members = make([]Member, len(ps))
			for i, p := range ps {
				g.Members[i] = Member{User: people[p.UserID], Role: p.Role, JoinedAt: p.JoinedAt}
			}
		}
		out[v] = g
	}
	return out, nil
}

// Group events: at most groupRenders groups are rendered at once, and a group's member list goes into its
// events only up to eventMembers members (each member sees the list rendered for them, so the cost grows with
// the square of the size); bigger groups send events without members and clients load them with GET.
const (
	groupRenders = 4
	eventMembers = 100
)

// groupQueue coalesces the events of each group: while one run renders a group, further changes only mark it
// dirty, and one more run follows with the state of that moment. Runs of one group never overlap, so members
// never get an older state after a newer one.
type groupQueue struct {
	mu      sync.Mutex
	pending map[int64]*pendingGroup
	slots   chan struct{}
}

type pendingGroup struct {
	dirty bool
	added map[int64]bool
}

// changed tells the group's members about a change, in the background. added get group_created (if they are
// still members by then) instead of group_updated.
func (s *Service) changed(ctx context.Context, chatID int64, added ...int64) {
	n, ok := s.d.Notifier.(GroupNotifier)
	if !ok {
		return
	}
	q := &s.groups
	q.mu.Lock()
	p, running := q.pending[chatID]
	if !running {
		p = &pendingGroup{added: map[int64]bool{}}
		q.pending[chatID] = p
	}
	for _, uid := range added {
		p.added[uid] = true
	}
	p.dirty = true
	q.mu.Unlock()
	if running {
		return // the run in progress goes once more
	}
	ctx = context.WithoutCancel(ctx)
	go func() {
		for {
			q.mu.Lock()
			if !p.dirty {
				delete(q.pending, chatID)
				q.mu.Unlock()
				return
			}
			fresh := p.added
			p.added, p.dirty = map[int64]bool{}, false
			q.mu.Unlock()

			q.slots <- struct{}{}
			s.publishGroup(ctx, n, chatID, fresh)
			<-q.slots
		}
	}()
}

func (s *Service) publishGroup(ctx context.Context, n GroupNotifier, chatID int64, added map[int64]bool) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ps, err := s.d.Store.Chats().Participants(ctx, chatID)
	if errors.Is(err, postgres.ErrNotFound) || (err == nil && len(ps) == 0) {
		return // deleted meanwhile
	}
	if err != nil {
		s.d.Log.WarnContext(ctx, "group members for an event", "error", err)
		return
	}
	ids := make([]int64, len(ps))
	for i, p := range ps {
		ids[i] = p.UserID
	}
	views, err := s.groupViews(ctx, chatID, ids, len(ps) <= eventMembers)
	if err != nil {
		s.d.Log.WarnContext(ctx, "render group for an event", "error", err)
		return
	}
	fresh := map[int64]GroupView{}
	for uid := range added {
		if v, ok := views[uid]; ok {
			fresh[uid] = v
			delete(views, uid)
		}
	}
	if len(fresh) > 0 {
		n.GroupCreated(ctx, chatID, fresh)
	}
	n.GroupUpdated(ctx, chatID, views)
}

// membership finishes a membership change: the cache, unread counters and events.
func (s *Service) membership(ctx context.Context, chatID, actor int64, added, removed []int64) error {
	// The change is committed: the cleanup must happen even if the client went away.
	ctx = context.WithoutCancel(ctx)
	stale := s.invalidateMembers(ctx, chatID)
	if len(removed) == 0 {
		stale = nil
	}
	for _, uid := range added {
		s.emit(ctx, events.Event{Type: events.ChatMemberAdded, ChatID: chatID, UserID: uid, ActorID: actor})
	}
	if len(removed) > 0 {
		if s.d.Calls != nil {
			s.d.Calls.MembersRemoved(ctx, chatID, removed)
		}
		s.d.Notifier.ChatRemoved(ctx, chatID, removed)
		if err := s.d.Unread.Forget(ctx, chatID, removed...); err != nil {
			s.d.Log.WarnContext(ctx, "forget unread", "error", err)
		}
		for _, uid := range removed {
			s.emit(ctx, events.Event{Type: events.ChatMemberRemoved, ChatID: chatID, UserID: uid, ActorID: actor})
		}
	}
	return stale
}

func (s *Service) settled(ctx context.Context, chatID int64, result error) error {
	if err := s.invalidateMembers(context.WithoutCancel(ctx), chatID); err != nil {
		return err
	}
	return result
}

func (s *Service) ifGone(ctx context.Context, chatID int64, result error) error {
	if !errors.Is(result, ErrNotFound) {
		return result
	}
	if _, err := s.d.Store.Chats().Get(ctx, chatID); errors.Is(err, postgres.ErrNotFound) {
		return s.settled(ctx, chatID, result)
	}
	return result
}

func (s *Service) invalidateMembers(ctx context.Context, chatID int64) error {
	var err error
	for attempt := range 3 {
		if err = s.d.Members.Invalidate(ctx, chatID); err == nil {
			return nil
		}
		time.Sleep(time.Duration(attempt+1) * 25 * time.Millisecond)
	}
	s.d.Log.ErrorContext(ctx, "members cache not invalidated", "chat_id", chatID, "error", err)
	return unavailable(err)
}

// ListGroups is the chat list of the user's groups.
func (s *Service) ListGroups(ctx context.Context, userID int64, after string, limit int) ([]View, string, error) {
	return s.list(ctx, userID, postgres.ChatGroup, after, limit)
}

// CreateGroup creates a group with the creator as owner. Members who approve invitations first get an
// invitation instead; anyone who does not allow invitations from the creator makes the whole request fail.
func (s *Service) CreateGroup(ctx context.Context, owner int64, name string, description *string, memberIDs []int64) (GroupView, error) {
	n, err := cleanName(name)
	if err != nil {
		return GroupView{}, err
	}
	desc, err := cleanDescription(description)
	if err != nil {
		return GroupView{}, err
	}
	if len(memberIDs) > MaxGroupInitial {
		return GroupView{}, fmt.Errorf("%w: at most %d members at once", ErrInvalid, MaxGroupInitial)
	}
	sorted := slices.Clone(memberIDs)
	slices.Sort(sorted)
	if len(slices.Compact(sorted)) != len(memberIDs) {
		return GroupView{}, fmt.Errorf("%w: member_ids must not repeat", ErrInvalid)
	}
	found, err := s.d.Store.Users().GetMany(ctx, memberIDs)
	if err != nil {
		return GroupView{}, unavailable(err)
	}
	var direct, invited []int64
	for _, uid := range memberIDs {
		if uid == owner {
			continue
		}
		if _, ok := found[uid]; !ok {
			return GroupView{}, fmt.Errorf("%w: user %d does not exist", ErrInvalid, uid)
		}
		allowed, approval, err := s.invitePermission(ctx, owner, uid)
		if err != nil {
			return GroupView{}, unavailable(err)
		}
		switch {
		case approval:
			invited = append(invited, uid)
		case allowed:
			direct = append(direct, uid)
		default:
			return GroupView{}, fmt.Errorf("%w: user %d does not allow group invitations from you", ErrForbidden, uid)
		}
	}
	d := ""
	if desc != nil {
		d = *desc
	}
	chat, err := s.d.Store.Chats().CreateGroup(ctx, postgres.NewGroup{OwnerID: owner, Name: n, Description: d, MemberIDs: direct})
	if err != nil {
		return GroupView{}, unavailable(err)
	}
	_ = s.membership(ctx, chat.ID, owner, append([]int64{owner}, direct...), nil)
	for _, uid := range invited {
		s.invite(ctx, chat.ID, owner, uid)
	}
	s.changed(ctx, chat.ID, append([]int64{owner}, direct...)...)
	s.logMembers(ctx, chat.ID, messages.SystemGroupCreated, owner)
	return s.group(ctx, owner, chat.ID)
}

func (s *Service) logMembers(ctx context.Context, chatID int64, kind string, actor int64, targets ...int64) {
	found, err := s.d.Store.Users().GetMany(ctx, append([]int64{actor}, targets...))
	if err != nil {
		s.d.Log.WarnContext(ctx, "member log names", "error", err, "chat_id", chatID)
	}
	name := func(id int64) string {
		if u, ok := found[id]; ok {
			if u.DisplayName != "" {
				return u.DisplayName
			}
			return u.Username
		}
		return ""
	}
	entries := []messages.SystemEvent{}
	if len(targets) == 0 {
		entries = append(entries, messages.SystemEvent{Kind: kind, ActorID: &actor, ActorName: name(actor)})
	}
	for _, target := range targets {
		entries = append(entries, messages.SystemEvent{Kind: kind, ActorID: &actor, ActorName: name(actor), TargetID: &target, TargetName: name(target)})
	}
	for _, e := range entries {
		if _, err := s.d.Sender.SendSystem(ctx, chatID, e); err != nil {
			s.d.Log.WarnContext(ctx, "member log", "error", err, "chat_id", chatID, "kind", kind)
		}
	}
}

// invite makes an invitation and tells the invitee. Failures are logged: the group exists either way.
func (s *Service) invite(ctx context.Context, chatID, actor, userID int64) {
	req, isNew, err := s.d.Store.Chats().InviteAs(ctx, chatID, actor, userID)
	if err != nil {
		s.d.Log.WarnContext(ctx, "group invitation", "error", err, "chat_id", chatID)
		return
	}
	if isNew {
		if rv, err := s.requestView(ctx, userID, req); err == nil {
			s.d.Notifier.RequestCreated(ctx, rv)
		}
	}
}

// Group returns the group as the member sees it.
func (s *Service) Group(ctx context.Context, userID, chatID int64) (GroupView, error) {
	return s.group(ctx, userID, chatID)
}

// UpdateGroup changes the name and, when setDescription, the description (nil clears it).
func (s *Service) UpdateGroup(ctx context.Context, userID, chatID int64, name *string, setDescription bool, description *string) (GroupView, error) {
	if name != nil {
		n, err := cleanName(*name)
		if err != nil {
			return GroupView{}, err
		}
		name = &n
	}
	if setDescription {
		d, err := cleanDescription(description)
		if err != nil {
			return GroupView{}, err
		}
		description = d
	}
	if name == nil && !setDescription {
		return GroupView{}, fmt.Errorf("%w: send name or description", ErrInvalid)
	}
	if _, err := s.d.Store.Chats().UpdateGroup(ctx, chatID, userID, name, setDescription, description); err != nil {
		return GroupView{}, groupError(err)
	}
	s.changed(ctx, chatID)
	return s.group(ctx, userID, chatID)
}

// DeleteGroup deletes the group for everyone (the owner only).
func (s *Service) DeleteGroup(ctx context.Context, userID, chatID int64) error {
	keys, members, err := s.d.Store.Chats().DeleteGroupAs(ctx, chatID, userID)
	if err != nil {
		return s.ifGone(ctx, chatID, groupError(err))
	}
	return s.removedChat(ctx, chatID, userID, members, keys)
}

// removedChat cleans up after a deleted chat: sockets, counters, cache, the worker and files.
func (s *Service) removedChat(ctx context.Context, chatID, actor int64, members []int64, keys []string) error {
	ctx = context.WithoutCancel(ctx)
	s.d.Notifier.ChatRemoved(ctx, chatID, members)
	if s.d.Calls != nil {
		s.d.Calls.ChatsDeleted(ctx)
	}
	if err := s.d.Unread.Forget(ctx, chatID, members...); err != nil {
		s.d.Log.WarnContext(ctx, "forget unread", "error", err)
	}
	stale := s.invalidateMembers(ctx, chatID)
	s.emit(ctx, events.Event{Type: events.ChatDeleted, ChatID: chatID, ActorID: actor, At: time.Now()})
	if s.d.DeleteFile != nil {
		for _, k := range keys {
			if err := s.d.DeleteFile(context.WithoutCancel(ctx), k); err != nil {
				s.d.Log.WarnContext(ctx, "delete file of a deleted chat", "error", err)
			}
		}
	}
	return stale
}

// SetGroupAvatar sets the group's avatar to an image the caller uploaded (purpose avatar).
func (s *Service) SetGroupAvatar(ctx context.Context, userID, chatID int64, attachmentID uuid.UUID) (GroupView, error) {
	a, err := s.d.Store.Attachments().Get(ctx, attachmentID)
	if errors.Is(err, postgres.ErrNotFound) || (err == nil && (a.UploaderID == nil || *a.UploaderID != userID || a.Purpose != postgres.PurposeAvatar || a.Kind != "image")) {
		return GroupView{}, fmt.Errorf("%w: attachment_id is not one of your avatar uploads", ErrInvalid)
	}
	if err != nil {
		return GroupView{}, unavailable(err)
	}
	if err := s.d.Store.Chats().SetGroupAvatarAs(ctx, chatID, userID, &attachmentID); err != nil {
		return GroupView{}, groupError(err)
	}
	s.changed(ctx, chatID)
	return s.group(ctx, userID, chatID)
}

// AddMember adds a user to the group, or invites them when they approve invitations first (the group then
// lists them nowhere until they accept).
func (s *Service) AddMember(ctx context.Context, userID, chatID, target int64) (GroupView, error) {
	// Outsiders learn nothing about the group, not even through the invitee's settings.
	if err := s.requireGroupMember(ctx, userID, chatID); err != nil {
		return GroupView{}, err
	}
	if _, err := s.d.Store.Users().Get(ctx, target); err != nil {
		return GroupView{}, notFoundOr(err)
	}
	allowed, approval, err := s.invitePermission(ctx, userID, target)
	if err != nil {
		return GroupView{}, unavailable(err)
	}
	switch {
	case approval:
		req, isNew, err := s.d.Store.Chats().InviteAs(ctx, chatID, userID, target)
		if err != nil {
			return GroupView{}, groupError(err)
		}
		if isNew {
			if rv, err := s.requestView(ctx, target, req); err == nil {
				s.d.Notifier.RequestCreated(ctx, rv)
			}
		}
	case allowed:
		if err := s.d.Store.Chats().AddMemberAs(ctx, chatID, userID, target); err != nil {
			return GroupView{}, groupError(err)
		}
		_ = s.membership(ctx, chatID, userID, []int64{target}, nil)
		s.changed(ctx, chatID, target)
		s.logMembers(ctx, chatID, messages.SystemMemberAdded, userID, target)
	default:
		return GroupView{}, ErrForbidden
	}
	return s.group(ctx, userID, chatID)
}

// RemoveMember removes a member (owner and admins; not the owner).
func (s *Service) RemoveMember(ctx context.Context, userID, chatID, target int64) error {
	if target == userID {
		err := s.Leave(ctx, userID, chatID)
		if errors.Is(err, ErrConflict) {
			return fmt.Errorf("%w: the owner cannot be removed", ErrForbidden)
		}
		return err
	}
	if err := s.d.Store.Chats().RemoveMemberAs(ctx, chatID, userID, target); err != nil {
		if errors.Is(err, postgres.ErrOwnerMustTransfer) {
			return fmt.Errorf("%w: the owner cannot be removed", ErrForbidden)
		}
		err = groupError(err)
		if errors.Is(err, ErrNotFound) {
			if p, perr := s.d.Store.Chats().Participant(ctx, chatID, userID); perr == nil && (p.Role == postgres.RoleOwner || p.Role == postgres.RoleAdmin) {
				return s.settled(ctx, chatID, err)
			}
		}
		return err
	}
	err := s.membership(ctx, chatID, userID, nil, []int64{target})
	s.changed(ctx, chatID)
	s.logMembers(ctx, chatID, messages.SystemMemberRemoved, userID, target)
	return err
}

// Leave takes the user out of the group; the owner has to transfer ownership first.
func (s *Service) Leave(ctx context.Context, userID, chatID int64) error {
	if err := s.d.Store.Chats().RemoveMemberAs(ctx, chatID, userID, userID); err != nil {
		err = groupError(err)
		if errors.Is(err, ErrNotFound) {
			return s.settled(ctx, chatID, err)
		}
		return err
	}
	err := s.membership(ctx, chatID, userID, nil, []int64{userID})
	s.changed(ctx, chatID)
	s.logMembers(ctx, chatID, messages.SystemMemberLeft, userID)
	return err
}

// SetRole makes a member an admin, a moderator or a member again.
func (s *Service) SetRole(ctx context.Context, userID, chatID, target int64, role postgres.Role) (GroupView, error) {
	if role != postgres.RoleAdmin && role != postgres.RoleModerator && role != postgres.RoleMember {
		return GroupView{}, fmt.Errorf("%w: role must be admin, moderator or member", ErrInvalid)
	}
	if err := s.d.Store.Chats().SetRoleAs(ctx, chatID, userID, target, role); err != nil {
		if errors.Is(err, postgres.ErrOwnerMustTransfer) {
			return GroupView{}, fmt.Errorf("%w: transfer ownership to change the owner's role", ErrInvalid)
		}
		return GroupView{}, groupError(err)
	}
	s.changed(ctx, chatID)
	return s.group(ctx, userID, chatID)
}

// TransferOwnership makes another member the owner; the previous owner becomes an admin.
func (s *Service) TransferOwnership(ctx context.Context, userID, chatID, target int64) (GroupView, error) {
	if err := s.requireGroupMember(ctx, userID, chatID); err != nil {
		return GroupView{}, err
	}
	if target == userID {
		return GroupView{}, fmt.Errorf("%w: you are the owner already", ErrInvalid)
	}
	if err := s.d.Store.Chats().TransferOwnership(ctx, chatID, userID, target); err != nil {
		if errors.Is(err, postgres.ErrNotGroup) {
			return GroupView{}, ErrNotFound
		}
		return GroupView{}, groupError(err)
	}
	s.changed(ctx, chatID)
	return s.group(ctx, userID, chatID)
}

// approveInvite accepts a group invitation: the user joins and the members are told (unless they were in the
// group already).
func (s *Service) approveInvite(ctx context.Context, userID, requestID int64) (View, error) {
	req, joined, err := s.d.Store.Approvals().ApproveInvite(ctx, requestID, userID)
	if errors.Is(err, postgres.ErrForbidden) {
		return View{}, fmt.Errorf("%w: whoever invited you can no longer add members", ErrConflict)
	}
	if err != nil {
		return View{}, requestError(err)
	}
	if joined {
		_ = s.membership(ctx, *req.ChatID, req.RequesterID, []int64{userID}, nil)
		s.changed(ctx, *req.ChatID, userID)
		s.logMembers(ctx, *req.ChatID, messages.SystemMemberJoined, userID)
	}
	return s.Get(ctx, userID, *req.ChatID)
}

const roleLogLimit = 100

type RoleEntry struct {
	Event  postgres.RoleEvent
	Actor  *users.View
	Target users.View
}

func (s *Service) RoleLog(ctx context.Context, userID, chatID int64) ([]RoleEntry, error) {
	if err := s.requireGroupMember(ctx, userID, chatID); err != nil {
		return nil, err
	}
	evs, err := s.d.Store.Chats().RoleEvents(ctx, chatID, roleLogLimit)
	if err != nil {
		return nil, unavailable(err)
	}
	ids := []int64{}
	for _, e := range evs {
		ids = append(ids, e.TargetID)
		if e.ActorID != nil {
			ids = append(ids, *e.ActorID)
		}
	}
	people, err := s.d.Directory.ForViewer(ctx, userID, dedupe(ids))
	if err != nil {
		return nil, unavailable(err)
	}
	out := make([]RoleEntry, len(evs))
	for i, e := range evs {
		out[i] = RoleEntry{Event: e, Target: people[e.TargetID]}
		if e.ActorID != nil {
			actor := people[*e.ActorID]
			out[i].Actor = &actor
		}
	}
	return out, nil
}

// requireGroupMember fails with ErrNotFound unless the user is in the group.
func (s *Service) requireGroupMember(ctx context.Context, userID, chatID int64) error {
	if _, err := s.d.Store.Chats().Participant(ctx, chatID, userID); err != nil {
		return notFoundOr(err)
	}
	return nil
}
