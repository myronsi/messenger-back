package chats

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/myronsi/messenger-back/internal/messages"
	"github.com/myronsi/messenger-back/internal/moderation"
	"github.com/myronsi/messenger-back/internal/publicnames"
	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/users"
)

const (
	MaxActiveInviteLinks = 50
	MaxInviteLinkName    = 64
	MaxInviteLinkUses    = 100000
	MaxInviteLinkAhead   = 366 * 24 * time.Hour
)

var (
	ErrNameTaken = errors.New("name taken")
	ErrLimit     = errors.New("limit reached")
)

type GroupCard struct {
	Chat        postgres.Chat
	MemberCount int
}

type GroupPreview struct {
	Card     GroupCard
	MemberOf bool
}

type Ban struct {
	Ban  postgres.ChatBan
	User users.View
}

type InviteLinkInput struct {
	Name      *string
	ExpiresAt *time.Time
	MaxUses   *int32
}

func sharingError(err error) error {
	switch {
	case errors.Is(err, postgres.ErrUsernameTaken):
		return fmt.Errorf("%w: the name is taken", ErrNameTaken)
	case errors.Is(err, postgres.ErrLimit):
		return fmt.Errorf("%w: at most %d active invite links", ErrLimit, MaxActiveInviteLinks)
	case errors.Is(err, postgres.ErrBanned):
		return fmt.Errorf("%w: banned from the group", ErrForbidden)
	case errors.Is(err, postgres.ErrLinkInvalid):
		return ErrNotFound
	case errors.Is(err, postgres.ErrConflict):
		return fmt.Errorf("%w: the link is revoked", ErrConflict)
	}
	return groupError(err)
}

func cleanLinkName(name *string) (*string, error) {
	if name == nil {
		return nil, nil
	}
	n := strings.TrimSpace(*name)
	if n == "" {
		return nil, nil
	}
	if utf8.RuneCountInString(n) > MaxInviteLinkName {
		return nil, fmt.Errorf("%w: name is longer than %d characters", ErrInvalid, MaxInviteLinkName)
	}
	return &n, nil
}

func checkExpiry(at *time.Time, now time.Time) error {
	if at == nil {
		return nil
	}
	if !at.After(now) {
		return fmt.Errorf("%w: expires_at must be in the future", ErrInvalid)
	}
	if at.Sub(now) > MaxInviteLinkAhead {
		return fmt.Errorf("%w: expires_at is more than 366 days ahead", ErrInvalid)
	}
	return nil
}

func checkMaxUses(n *int32) error {
	if n != nil && (*n < 1 || *n > MaxInviteLinkUses) {
		return fmt.Errorf("%w: max_uses must be 1 to %d", ErrInvalid, MaxInviteLinkUses)
	}
	return nil
}

func (s *Service) CreateInviteLink(ctx context.Context, userID, chatID int64, in InviteLinkInput) (postgres.InviteLink, error) {
	if err := s.allow(ctx, userID, moderation.ShareGroup, 0); err != nil {
		return postgres.InviteLink{}, err
	}
	name, err := cleanLinkName(in.Name)
	if err != nil {
		return postgres.InviteLink{}, err
	}
	if err := checkExpiry(in.ExpiresAt, time.Now()); err != nil {
		return postgres.InviteLink{}, err
	}
	if err := checkMaxUses(in.MaxUses); err != nil {
		return postgres.InviteLink{}, err
	}
	l, err := s.d.Store.Sharing().CreateInviteLinkAs(ctx, chatID, userID, publicnames.NewToken(), name, in.ExpiresAt, in.MaxUses, MaxActiveInviteLinks)
	if err != nil {
		return postgres.InviteLink{}, sharingError(err)
	}
	return l, nil
}

func (s *Service) InviteLinks(ctx context.Context, userID, chatID int64) ([]postgres.InviteLink, error) {
	links, err := s.d.Store.Sharing().InviteLinksAs(ctx, chatID, userID)
	if err != nil {
		return nil, sharingError(err)
	}
	return links, nil
}

func (s *Service) UpdateInviteLink(ctx context.Context, userID, chatID, linkID int64, p postgres.InviteLinkPatch) (postgres.InviteLink, error) {
	if err := s.allow(ctx, userID, moderation.ShareGroup, 0); err != nil {
		return postgres.InviteLink{}, err
	}
	if p.SetName {
		name, err := cleanLinkName(p.Name)
		if err != nil {
			return postgres.InviteLink{}, err
		}
		p.Name = name
	}
	if p.SetExpiresAt {
		if err := checkExpiry(p.ExpiresAt, time.Now()); err != nil {
			return postgres.InviteLink{}, err
		}
	}
	if p.SetMaxUses {
		if err := checkMaxUses(p.MaxUses); err != nil {
			return postgres.InviteLink{}, err
		}
	}
	l, err := s.d.Store.Sharing().UpdateInviteLinkAs(ctx, chatID, userID, linkID, p)
	if err != nil {
		return postgres.InviteLink{}, sharingError(err)
	}
	return l, nil
}

func (s *Service) RevokeInviteLink(ctx context.Context, userID, chatID, linkID int64) error {
	if err := s.d.Store.Sharing().RevokeInviteLinkAs(ctx, chatID, userID, linkID); err != nil {
		return sharingError(err)
	}
	return nil
}

func (s *Service) card(ctx context.Context, chat postgres.Chat) (GroupCard, error) {
	n, err := s.d.Store.Sharing().MemberCount(ctx, chat.ID)
	if err != nil {
		return GroupCard{}, unavailable(err)
	}
	return GroupCard{Chat: chat, MemberCount: n}, nil
}

func (s *Service) isMember(ctx context.Context, chatID, userID int64) (bool, error) {
	_, err := s.d.Store.Chats().Participant(ctx, chatID, userID)
	if errors.Is(err, postgres.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, unavailable(err)
	}
	return true, nil
}

func (s *Service) InviteCard(ctx context.Context, token string) (GroupCard, error) {
	if !publicnames.ValidToken(token) {
		return GroupCard{}, ErrNotFound
	}
	l, err := s.d.Store.Sharing().ActiveInviteLink(ctx, token)
	if err != nil {
		return GroupCard{}, notFoundOr(err)
	}
	chat, err := s.d.Store.Chats().Get(ctx, l.ChatID)
	if err != nil {
		return GroupCard{}, notFoundOr(err)
	}
	return s.card(ctx, chat)
}

func (s *Service) PublicGroupCard(ctx context.Context, name string) (GroupCard, error) {
	if !publicnames.Valid(name) {
		return GroupCard{}, ErrNotFound
	}
	owner, err := s.d.Store.Sharing().PublicName(ctx, name)
	if err != nil {
		return GroupCard{}, notFoundOr(err)
	}
	if owner.ChatID == nil {
		return GroupCard{}, ErrNotFound
	}
	chat, err := s.d.Store.Chats().Get(ctx, *owner.ChatID)
	if err != nil {
		return GroupCard{}, notFoundOr(err)
	}
	return s.card(ctx, chat)
}

func (s *Service) preview(ctx context.Context, userID int64, card GroupCard, err error) (GroupPreview, error) {
	if err != nil {
		return GroupPreview{}, err
	}
	member, err := s.isMember(ctx, card.Chat.ID, userID)
	if err != nil {
		return GroupPreview{}, err
	}
	return GroupPreview{Card: card, MemberOf: member}, nil
}

func (s *Service) InvitePreview(ctx context.Context, userID int64, token string) (GroupPreview, error) {
	card, err := s.InviteCard(ctx, token)
	return s.preview(ctx, userID, card, err)
}

func (s *Service) PublicGroupPreview(ctx context.Context, userID int64, name string) (GroupPreview, error) {
	card, err := s.PublicGroupCard(ctx, name)
	return s.preview(ctx, userID, card, err)
}

func (s *Service) joined(ctx context.Context, userID, chatID int64, joined bool) (View, error) {
	if joined {
		_ = s.membership(ctx, chatID, userID, []int64{userID}, nil)
		s.changed(ctx, chatID, userID)
		s.logMembers(ctx, chatID, messages.SystemMemberJoined, userID)
	}
	return s.Get(ctx, userID, chatID)
}

func (s *Service) JoinByLink(ctx context.Context, userID int64, token string) (View, error) {
	if !publicnames.ValidToken(token) {
		return View{}, ErrNotFound
	}
	if err := s.allow(ctx, userID, moderation.JoinByLink, 0); err != nil {
		return View{}, err
	}
	chatID, joined, err := s.d.Store.Sharing().JoinByLink(ctx, userID, token)
	if err != nil {
		return View{}, sharingError(err)
	}
	return s.joined(ctx, userID, chatID, joined)
}

func (s *Service) JoinPublic(ctx context.Context, userID int64, name string) (View, error) {
	if !publicnames.Valid(name) {
		return View{}, ErrNotFound
	}
	if err := s.allow(ctx, userID, moderation.JoinByLink, 0); err != nil {
		return View{}, err
	}
	chatID, joined, err := s.d.Store.Sharing().JoinPublic(ctx, userID, name)
	if err != nil {
		return View{}, sharingError(err)
	}
	return s.joined(ctx, userID, chatID, joined)
}

func (s *Service) SetPublicName(ctx context.Context, userID, chatID int64, name *string) (GroupView, error) {
	if name != nil {
		if err := s.allow(ctx, userID, moderation.ShareGroup, 0); err != nil {
			return GroupView{}, err
		}
		if !publicnames.Valid(*name) {
			return GroupView{}, fmt.Errorf("%w: public_name must be 3 to 32 letters, digits or underscores", ErrInvalid)
		}
		if publicnames.Reserved(*name) {
			return GroupView{}, fmt.Errorf("%w: public_name is reserved", ErrInvalid)
		}
	}
	if _, err := s.d.Store.Sharing().SetPublicNameAs(ctx, chatID, userID, name); err != nil {
		return GroupView{}, sharingError(err)
	}
	s.changed(ctx, chatID)
	return s.group(ctx, userID, chatID)
}

func (s *Service) PublicNameAvailable(ctx context.Context, name string) (bool, error) {
	if !publicnames.Valid(name) || publicnames.Reserved(name) {
		return false, nil
	}
	_, err := s.d.Store.Sharing().PublicName(ctx, name)
	if errors.Is(err, postgres.ErrNotFound) {
		return true, nil
	}
	if err != nil {
		return false, unavailable(err)
	}
	return false, nil
}

func (s *Service) SearchPublicGroups(ctx context.Context, userID int64, q string, after string, limit int) ([]GroupPreview, string, error) {
	var afterID *int64
	if after != "" {
		id, err := parseIDCursor(after)
		if err != nil {
			return nil, "", ErrBadCursor
		}
		afterID = &id
	}
	limit = max(1, min(limit, 100))
	rows, err := s.d.Store.Sharing().SearchPublicGroups(ctx, q, afterID, limit+1)
	if err != nil {
		return nil, "", unavailable(err)
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		next = idCursor(rows[len(rows)-1].Chat.ID)
	}
	out := make([]GroupPreview, len(rows))
	for i, row := range rows {
		member, err := s.isMember(ctx, row.Chat.ID, userID)
		if err != nil {
			return nil, "", err
		}
		out[i] = GroupPreview{Card: GroupCard{Chat: row.Chat, MemberCount: row.MemberCount}, MemberOf: member}
	}
	return out, next, nil
}

func (s *Service) Bans(ctx context.Context, userID, chatID int64, after string, limit int) ([]Ban, string, error) {
	var cursor *postgres.ChatBan
	if after != "" {
		c, err := parseBanCursor(after)
		if err != nil {
			return nil, "", ErrBadCursor
		}
		cursor = &c
	}
	limit = max(1, min(limit, 100))
	rows, err := s.d.Store.Sharing().BansAs(ctx, chatID, userID, cursor, limit+1)
	if err != nil {
		return nil, "", sharingError(err)
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		next = banCursor(rows[len(rows)-1])
	}
	ids := make([]int64, len(rows))
	for i, b := range rows {
		ids[i] = b.UserID
	}
	views, err := s.d.Directory.ForViewer(ctx, userID, ids)
	if err != nil {
		return nil, "", unavailable(err)
	}
	out := make([]Ban, len(rows))
	for i, b := range rows {
		out[i] = Ban{Ban: b, User: views[b.UserID]}
	}
	return out, next, nil
}

func (s *Service) Unban(ctx context.Context, userID, chatID, target int64) error {
	if err := s.d.Store.Sharing().UnbanAs(ctx, chatID, userID, target); err != nil {
		return sharingError(err)
	}
	return nil
}

func (s *Service) UnreadMentions(ctx context.Context, userID, chatID int64) ([]int64, error) {
	if _, err := s.d.Store.Chats().Participant(ctx, chatID, userID); err != nil {
		return nil, notFoundOr(err)
	}
	ids, err := s.d.Store.Sharing().UnreadMentions(ctx, userID, chatID)
	if err != nil {
		return nil, unavailable(err)
	}
	return ids, nil
}

func parseIDCursor(s string) (int64, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return 0, badCursor()
	}
	id, err := strconv.ParseInt(string(b), 10, 64)
	if err != nil || id <= 0 {
		return 0, badCursor()
	}
	return id, nil
}

func idCursor(id int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(id, 10)))
}

func banCursor(b postgres.ChatBan) string {
	raw := strconv.FormatInt(b.CreatedAt.UnixMicro(), 10) + "." + strconv.FormatInt(b.UserID, 10)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func parseBanCursor(s string) (postgres.ChatBan, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return postgres.ChatBan{}, badCursor()
	}
	at, id, ok := strings.Cut(string(b), ".")
	if !ok {
		return postgres.ChatBan{}, badCursor()
	}
	us, err1 := strconv.ParseInt(at, 10, 64)
	uid, err2 := strconv.ParseInt(id, 10, 64)
	if err1 != nil || err2 != nil {
		return postgres.ChatBan{}, badCursor()
	}
	return postgres.ChatBan{CreatedAt: time.UnixMicro(us).UTC(), UserID: uid}, nil
}
