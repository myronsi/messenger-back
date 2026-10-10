package chats

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/events"
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
// the target's group_invites setting (an explicit exception decides first, as in v1). Blocks deny.
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
	switch exc[actor][postgres.SettingGroupInvites] {
	case postgres.EffectAllow:
		return true, false, nil
	case postgres.EffectDeny:
		return false, false, nil
	}
	settings, err := social.Privacy(ctx, []int64{target})
	if err != nil {
		return false, false, err
	}
	switch v := settings[target].GroupInvites; v {
	case postgres.WaitApproval:
		return false, true, nil
	case postgres.VisibleSharedChats:
		shared, err := social.SharedChatPartners(ctx, target, []int64{actor})
		return shared[actor], false, err
	default:
		return users.Visible(v, "", false), false, nil
	}
}

// group renders a group for one member.
func (s *Service) group(ctx context.Context, viewer, chatID int64) (GroupView, error) {
	views, err := s.groupViews(ctx, chatID, []int64{viewer})
	if err != nil {
		return GroupView{}, err
	}
	v, ok := views[viewer]
	if !ok {
		return GroupView{}, ErrNotFound
	}
	return v, nil
}

// groupViews renders the group for each of the viewers who are members.
func (s *Service) groupViews(ctx context.Context, chatID int64, viewers []int64) (map[int64]GroupView, error) {
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
		people, err := s.d.Directory.ForViewer(ctx, v, ids)
		if err != nil {
			return nil, unavailable(err)
		}
		g := GroupView{Chat: chat, OwnerID: owner, MyRole: role, Members: make([]Member, len(ps))}
		for i, p := range ps {
			g.Members[i] = Member{User: people[p.UserID], Role: p.Role, JoinedAt: p.JoinedAt}
		}
		out[v] = g
	}
	return out, nil
}

// changed tells the group's members about a change, after the request (rendering for every member costs a
// directory lookup each). added get group_created instead of group_updated.
func (s *Service) changed(ctx context.Context, chatID int64, added ...int64) {
	n, ok := s.d.Notifier.(GroupNotifier)
	if !ok {
		return
	}
	ctx = context.WithoutCancel(ctx)
	go func() {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		ps, err := s.d.Store.Chats().Participants(ctx, chatID)
		if err != nil {
			s.d.Log.WarnContext(ctx, "group members for an event", "error", err)
			return
		}
		ids := make([]int64, len(ps))
		for i, p := range ps {
			ids[i] = p.UserID
		}
		views, err := s.groupViews(ctx, chatID, ids)
		if err != nil {
			s.d.Log.WarnContext(ctx, "render group for an event", "error", err)
			return
		}
		fresh := map[int64]GroupView{}
		for _, uid := range added {
			if v, ok := views[uid]; ok {
				fresh[uid] = v
				delete(views, uid)
			}
		}
		if len(fresh) > 0 {
			n.GroupCreated(ctx, chatID, fresh)
		}
		n.GroupUpdated(ctx, chatID, views)
	}()
}

// membership finishes a membership change: the cache, unread counters and events.
func (s *Service) membership(ctx context.Context, chatID, actor int64, added, removed []int64) {
	if err := s.d.Members.Invalidate(ctx, chatID); err != nil {
		s.d.Log.WarnContext(ctx, "invalidate members", "error", err)
	}
	for _, uid := range added {
		s.emit(ctx, events.Event{Type: events.ChatMemberAdded, ChatID: chatID, UserID: uid, ActorID: actor})
	}
	if len(removed) > 0 {
		s.d.Notifier.ChatRemoved(ctx, chatID, removed)
		if err := s.d.Unread.Forget(ctx, chatID, removed...); err != nil {
			s.d.Log.WarnContext(ctx, "forget unread", "error", err)
		}
		for _, uid := range removed {
			s.emit(ctx, events.Event{Type: events.ChatMemberRemoved, ChatID: chatID, UserID: uid, ActorID: actor})
		}
	}
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
	s.membership(ctx, chat.ID, owner, append([]int64{owner}, direct...), nil)
	for _, uid := range invited {
		s.invite(ctx, chat.ID, owner, uid)
	}
	s.changed(ctx, chat.ID, append([]int64{owner}, direct...)...)
	return s.group(ctx, owner, chat.ID)
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
		return groupError(err)
	}
	s.removedChat(ctx, chatID, userID, members, keys)
	return nil
}

// removedChat cleans up after a deleted chat: sockets, counters, cache, the worker and files.
func (s *Service) removedChat(ctx context.Context, chatID, actor int64, members []int64, keys []string) {
	s.d.Notifier.ChatRemoved(ctx, chatID, members)
	if err := s.d.Unread.Forget(ctx, chatID, members...); err != nil {
		s.d.Log.WarnContext(ctx, "forget unread", "error", err)
	}
	if err := s.d.Members.Invalidate(ctx, chatID); err != nil {
		s.d.Log.WarnContext(ctx, "invalidate members", "error", err)
	}
	s.emit(ctx, events.Event{Type: events.ChatDeleted, ChatID: chatID, ActorID: actor, At: time.Now()})
	if s.d.DeleteFile != nil {
		for _, k := range keys {
			if err := s.d.DeleteFile(context.WithoutCancel(ctx), k); err != nil {
				s.d.Log.WarnContext(ctx, "delete file of a deleted chat", "error", err)
			}
		}
	}
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
		s.membership(ctx, chatID, userID, []int64{target}, nil)
		s.changed(ctx, chatID, target)
	default:
		return GroupView{}, ErrForbidden
	}
	return s.group(ctx, userID, chatID)
}

// RemoveMember removes a member (owner and admins; not the owner).
func (s *Service) RemoveMember(ctx context.Context, userID, chatID, target int64) error {
	if target == userID {
		return s.Leave(ctx, userID, chatID)
	}
	if err := s.d.Store.Chats().RemoveMemberAs(ctx, chatID, userID, target); err != nil {
		if errors.Is(err, postgres.ErrOwnerMustTransfer) {
			return fmt.Errorf("%w: the owner cannot be removed", ErrForbidden)
		}
		return groupError(err)
	}
	s.membership(ctx, chatID, userID, nil, []int64{target})
	s.changed(ctx, chatID)
	return nil
}

// Leave takes the user out of the group; the owner has to transfer ownership first.
func (s *Service) Leave(ctx context.Context, userID, chatID int64) error {
	if err := s.d.Store.Chats().RemoveMemberAs(ctx, chatID, userID, userID); err != nil {
		return groupError(err)
	}
	s.membership(ctx, chatID, userID, nil, []int64{userID})
	s.changed(ctx, chatID)
	return nil
}

// SetRole makes a member an admin or a member again.
func (s *Service) SetRole(ctx context.Context, userID, chatID, target int64, role postgres.Role) (GroupView, error) {
	if role != postgres.RoleAdmin && role != postgres.RoleMember {
		return GroupView{}, fmt.Errorf("%w: role must be admin or member", ErrInvalid)
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

// approveInvite accepts a group invitation: the user joins and the members are told.
func (s *Service) approveInvite(ctx context.Context, userID, requestID int64) (View, error) {
	req, err := s.d.Store.Approvals().ApproveInvite(ctx, requestID, userID)
	if err != nil {
		return View{}, requestError(err)
	}
	s.membership(ctx, *req.ChatID, req.RequesterID, []int64{userID}, nil)
	s.changed(ctx, *req.ChatID, userID)
	return s.Get(ctx, userID, *req.ChatID)
}
