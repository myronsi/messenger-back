package postgres

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/myronsi/messenger-back/internal/store/postgres/sqlcdb"
)

var (
	ErrBanned      = errors.New("banned from the group")
	ErrLinkInvalid = errors.New("invite link does not work")
)

type PublicNameOwner struct {
	UserID *int64
	ChatID *int64
}

type InviteLink struct {
	ID        int64
	ChatID    int64
	Token     string
	Name      *string
	CreatedBy *int64
	CreatedAt time.Time
	ExpiresAt *time.Time
	MaxUses   *int32
	UseCount  int32
	RevokedAt *time.Time
}

func (l InviteLink) Active(now time.Time) bool {
	if l.RevokedAt != nil {
		return false
	}
	if l.ExpiresAt != nil && !l.ExpiresAt.After(now) {
		return false
	}
	return l.MaxUses == nil || l.UseCount < *l.MaxUses
}

type InviteLinkPatch struct {
	SetName      bool
	Name         *string
	SetExpiresAt bool
	ExpiresAt    *time.Time
	SetMaxUses   bool
	MaxUses      *int32
}

type ChatBan struct {
	ChatID    int64
	UserID    int64
	BannedBy  *int64
	CreatedAt time.Time
}

type PublicGroupRow struct {
	Chat        Chat
	MemberCount int
}

type JoinResult struct {
	Joined bool
}

type SharingRepository interface {
	PublicName(ctx context.Context, name string) (PublicNameOwner, error)
	MemberCount(ctx context.Context, chatID int64) (int, error)
	SetPublicNameAs(ctx context.Context, chatID, actorID int64, name *string) (Chat, error)
	SearchPublicGroups(ctx context.Context, q string, afterID *int64, limit int) ([]PublicGroupRow, error)

	CreateInviteLinkAs(ctx context.Context, chatID, actorID int64, token string, name *string, expiresAt *time.Time, maxUses *int32, maxActive int) (InviteLink, error)
	InviteLinksAs(ctx context.Context, chatID, actorID int64) ([]InviteLink, error)
	UpdateInviteLinkAs(ctx context.Context, chatID, actorID, linkID int64, p InviteLinkPatch) (InviteLink, error)
	RevokeInviteLinkAs(ctx context.Context, chatID, actorID, linkID int64) error
	ActiveInviteLink(ctx context.Context, token string) (InviteLink, error)

	JoinByLink(ctx context.Context, userID int64, token string) (int64, bool, error)
	JoinPublic(ctx context.Context, userID int64, name string) (int64, bool, error)

	BanAs(ctx context.Context, chatID, actorID, userID int64) error
	UnbanAs(ctx context.Context, chatID, actorID, userID int64) error
	BansAs(ctx context.Context, chatID, actorID int64, after *ChatBan, limit int) ([]ChatBan, error)
	Banned(ctx context.Context, chatID, userID int64) (bool, error)

	MembersByUsername(ctx context.Context, chatID int64, lowerNames []string) (map[string]int64, error)
	MemberIDs(ctx context.Context, chatID int64) ([]int64, error)
	AddUnreadMentions(ctx context.Context, chatID, messageID int64, userIDs []int64) error
	SetUnreadMentions(ctx context.Context, chatID, messageID int64, userIDs []int64) error
	RemoveUnreadMentions(ctx context.Context, chatID int64, messageIDs []int64) error
	UnreadMentions(ctx context.Context, userID, chatID int64) ([]int64, error)
	UnreadMentionCounts(ctx context.Context, userID int64, chatIDs []int64) (map[int64]int, error)
}

func (s *Store) Sharing() SharingRepository { return sharingRepo{s} }

type sharingRepo struct{ s *Store }

var _ SharingRepository = sharingRepo{}

func linkFrom(l sqlcdb.ChatInviteLink) InviteLink {
	return InviteLink{
		ID: l.ID, ChatID: l.ChatID, Token: l.Token, Name: l.Name, CreatedBy: l.CreatedBy, CreatedAt: l.CreatedAt,
		ExpiresAt: l.ExpiresAt, MaxUses: l.MaxUses, UseCount: l.UseCount, RevokedAt: l.RevokedAt,
	}
}

func (r sharingRepo) PublicName(ctx context.Context, name string) (PublicNameOwner, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	row, err := r.s.q.GetPublicName(ctx, name)
	if err != nil {
		return PublicNameOwner{}, mapError(err)
	}
	return PublicNameOwner{UserID: row.UserID, ChatID: row.ChatID}, nil
}

func (r sharingRepo) MemberCount(ctx context.Context, chatID int64) (int, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	n, err := r.s.q.CountChatMembers(ctx, chatID)
	return int(n), mapError(err)
}

func (r sharingRepo) SetPublicNameAs(ctx context.Context, chatID, actorID int64, name *string) (Chat, error) {
	var out Chat
	err := r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		if err := actingIn(ctx, q, chatID, actorID, managers); err != nil {
			return err
		}
		c, err := q.SetChatPublicName(ctx, sqlcdb.SetChatPublicNameParams{ID: chatID, PublicName: name})
		if err != nil {
			return err
		}
		out = chatFrom(c)
		return nil
	})
	return out, mapError(err)
}

func (r sharingRepo) SearchPublicGroups(ctx context.Context, q string, afterID *int64, limit int) ([]PublicGroupRow, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	rows, err := r.s.q.SearchPublicGroups(ctx, sqlcdb.SearchPublicGroupsParams{
		Prefix: likeEscape(strings.ToLower(strings.TrimSpace(q))), Contains: likeEscape(strings.ToLower(strings.TrimSpace(q))), AfterID: afterID, MaxRows: clampLimit(limit),
	})
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]PublicGroupRow, len(rows))
	for i, row := range rows {
		out[i] = PublicGroupRow{Chat: chatFrom(sqlcdb.Chat{
			ID: row.ID, Type: row.Type, Name: row.Name, Description: row.Description, AvatarUrl: row.AvatarUrl,
			DirectKey: row.DirectKey, CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
			AvatarAttachmentID: row.AvatarAttachmentID, LastMessageID: row.LastMessageID, LastActivityAt: row.LastActivityAt,
			ClearedBeforeMessageID: row.ClearedBeforeMessageID, BackgroundKind: row.BackgroundKind,
			BackgroundAttachmentID: row.BackgroundAttachmentID, BackgroundPreset: row.BackgroundPreset,
			BackgroundColors: row.BackgroundColors, PublicName: row.PublicName,
		}), MemberCount: int(row.MemberCount)}
	}
	return out, nil
}

func (r sharingRepo) CreateInviteLinkAs(ctx context.Context, chatID, actorID int64, token string, name *string, expiresAt *time.Time, maxUses *int32, maxActive int) (InviteLink, error) {
	var out InviteLink
	err := r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		if err := actingIn(ctx, q, chatID, actorID, managers); err != nil {
			return err
		}
		active, err := q.CountActiveInviteLinks(ctx, chatID)
		if err != nil {
			return err
		}
		if int(active) >= maxActive {
			return ErrLimit
		}
		l, err := q.CreateInviteLink(ctx, sqlcdb.CreateInviteLinkParams{
			ChatID: chatID, Token: token, Name: name, CreatedBy: &actorID, ExpiresAt: expiresAt, MaxUses: maxUses,
		})
		if err != nil {
			return err
		}
		out = linkFrom(l)
		return nil
	})
	return out, mapError(err)
}

func (r sharingRepo) InviteLinksAs(ctx context.Context, chatID, actorID int64) ([]InviteLink, error) {
	var out []InviteLink
	err := r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		if err := actingIn(ctx, q, chatID, actorID, managers); err != nil {
			return err
		}
		rows, err := q.ListInviteLinks(ctx, chatID)
		if err != nil {
			return err
		}
		out = make([]InviteLink, len(rows))
		for i, l := range rows {
			out[i] = linkFrom(l)
		}
		return nil
	})
	return out, mapError(err)
}

func (r sharingRepo) UpdateInviteLinkAs(ctx context.Context, chatID, actorID, linkID int64, p InviteLinkPatch) (InviteLink, error) {
	var out InviteLink
	err := r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		if err := actingIn(ctx, q, chatID, actorID, managers); err != nil {
			return err
		}
		cur, err := q.GetInviteLink(ctx, sqlcdb.GetInviteLinkParams{ID: linkID, ChatID: chatID})
		if err != nil {
			return err
		}
		if cur.RevokedAt != nil {
			return ErrConflict
		}
		l, err := q.UpdateInviteLink(ctx, sqlcdb.UpdateInviteLinkParams{
			ID: linkID, ChatID: chatID, SetName: p.SetName, Name: p.Name, SetExpiresAt: p.SetExpiresAt,
			ExpiresAt: p.ExpiresAt, SetMaxUses: p.SetMaxUses, MaxUses: p.MaxUses,
		})
		if err != nil {
			return err
		}
		out = linkFrom(l)
		return nil
	})
	return out, mapError(err)
}

func (r sharingRepo) RevokeInviteLinkAs(ctx context.Context, chatID, actorID, linkID int64) error {
	return mapError(r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		if err := actingIn(ctx, q, chatID, actorID, managers); err != nil {
			return err
		}
		n, err := q.RevokeInviteLink(ctx, sqlcdb.RevokeInviteLinkParams{ID: linkID, ChatID: chatID})
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		return nil
	}))
}

func (r sharingRepo) ActiveInviteLink(ctx context.Context, token string) (InviteLink, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	l, err := r.s.q.GetActiveInviteLink(ctx, token)
	if err != nil {
		return InviteLink{}, mapError(err)
	}
	return linkFrom(l), nil
}

func joinChat(ctx context.Context, q *sqlcdb.Queries, chatID, userID int64) (bool, error) {
	if err := lockGroup(ctx, q, chatID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, ErrNotGroup) {
			return false, ErrNotFound
		}
		return false, err
	}
	if _, err := q.LockParticipant(ctx, sqlcdb.LockParticipantParams{ChatID: chatID, UserID: userID}); err == nil {
		return false, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	banned, err := q.IsBannedFromChat(ctx, sqlcdb.IsBannedFromChatParams{ChatID: chatID, UserID: userID})
	if err != nil {
		return false, err
	}
	if banned {
		return false, ErrBanned
	}
	if _, err := q.AddParticipant(ctx, sqlcdb.AddParticipantParams{ChatID: chatID, UserID: userID, Role: string(RoleMember), Engaged: true}); err != nil {
		return false, err
	}
	if _, err := q.CloseGroupInvites(ctx, sqlcdb.CloseGroupInvitesParams{ChatID: &chatID, RecipientID: userID}); err != nil {
		return false, err
	}
	return true, nil
}

func (r sharingRepo) JoinByLink(ctx context.Context, userID int64, token string) (int64, bool, error) {
	var chatID int64
	var joined bool
	err := r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		chatID, joined = 0, false
		if _, err := q.LockUserShared(ctx, userID); err != nil {
			return err
		}
		l, err := q.GetActiveInviteLink(ctx, token)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrLinkInvalid
		}
		if err != nil {
			return err
		}
		chatID = l.ChatID
		joined, err = joinChat(ctx, q, l.ChatID, userID)
		if err != nil || !joined {
			return err
		}
		if _, err := q.UseInviteLink(ctx, l.ID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrLinkInvalid
			}
			return err
		}
		return nil
	})
	return chatID, joined, mapError(err)
}

func (r sharingRepo) JoinPublic(ctx context.Context, userID int64, name string) (int64, bool, error) {
	var chatID int64
	var joined bool
	err := r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		chatID, joined = 0, false
		if _, err := q.LockUserShared(ctx, userID); err != nil {
			return err
		}
		owner, err := q.GetPublicName(ctx, name)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && owner.ChatID == nil) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		chatID = *owner.ChatID
		joined, err = joinChat(ctx, q, chatID, userID)
		return err
	})
	return chatID, joined, mapError(err)
}

func (r sharingRepo) BanAs(ctx context.Context, chatID, actorID, userID int64) error {
	return mapError(r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		if err := actingIn(ctx, q, chatID, actorID, managers); err != nil {
			return err
		}
		return q.BanFromChat(ctx, sqlcdb.BanFromChatParams{ChatID: chatID, UserID: userID, BannedBy: &actorID})
	}))
}

func (r sharingRepo) UnbanAs(ctx context.Context, chatID, actorID, userID int64) error {
	return mapError(r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		if err := actingIn(ctx, q, chatID, actorID, managers); err != nil {
			return err
		}
		_, err := q.UnbanFromChat(ctx, sqlcdb.UnbanFromChatParams{ChatID: chatID, UserID: userID})
		return err
	}))
}

func (r sharingRepo) BansAs(ctx context.Context, chatID, actorID int64, after *ChatBan, limit int) ([]ChatBan, error) {
	var out []ChatBan
	err := r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		if err := actingIn(ctx, q, chatID, actorID, managers); err != nil {
			return err
		}
		p := sqlcdb.ListChatBansParams{ChatID: chatID, MaxRows: clampLimit(limit)}
		if after != nil {
			p.AfterCreatedAt, p.AfterUserID = &after.CreatedAt, &after.UserID
		}
		rows, err := q.ListChatBans(ctx, p)
		if err != nil {
			return err
		}
		out = make([]ChatBan, len(rows))
		for i, b := range rows {
			out[i] = ChatBan{ChatID: b.ChatID, UserID: b.UserID, BannedBy: b.BannedBy, CreatedAt: b.CreatedAt}
		}
		return nil
	})
	return out, mapError(err)
}

func (r sharingRepo) Banned(ctx context.Context, chatID, userID int64) (bool, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	b, err := r.s.q.IsBannedFromChat(ctx, sqlcdb.IsBannedFromChatParams{ChatID: chatID, UserID: userID})
	return b, mapError(err)
}

func (r sharingRepo) MembersByUsername(ctx context.Context, chatID int64, lowerNames []string) (map[string]int64, error) {
	out := map[string]int64{}
	if len(lowerNames) == 0 {
		return out, nil
	}
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	rows, err := r.s.q.GroupMembersByUsername(ctx, sqlcdb.GroupMembersByUsernameParams{ChatID: chatID, LowerUsernames: lowerNames})
	if err != nil {
		return nil, mapError(err)
	}
	for _, row := range rows {
		out[row.LowerUsername] = row.UserID
	}
	return out, nil
}

func (r sharingRepo) MemberIDs(ctx context.Context, chatID int64) ([]int64, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	ids, err := r.s.q.ListGroupMemberIDs(ctx, chatID)
	return ids, mapError(err)
}

func (r sharingRepo) AddUnreadMentions(ctx context.Context, chatID, messageID int64, userIDs []int64) error {
	if len(userIDs) == 0 {
		return nil
	}
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	return mapError(r.s.q.AddUnreadMentions(ctx, sqlcdb.AddUnreadMentionsParams{ChatID: chatID, MessageID: messageID, UserIds: userIDs}))
}

func (r sharingRepo) SetUnreadMentions(ctx context.Context, chatID, messageID int64, userIDs []int64) error {
	return mapError(r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		keep := userIDs
		if keep == nil {
			keep = []int64{}
		}
		if err := q.RemoveUnreadMentionsExcept(ctx, sqlcdb.RemoveUnreadMentionsExceptParams{ChatID: chatID, MessageID: messageID, Keep: keep}); err != nil {
			return err
		}
		if len(userIDs) == 0 {
			return nil
		}
		return q.AddUnreadMentions(ctx, sqlcdb.AddUnreadMentionsParams{ChatID: chatID, MessageID: messageID, UserIds: userIDs})
	}))
}

func (r sharingRepo) RemoveUnreadMentions(ctx context.Context, chatID int64, messageIDs []int64) error {
	if len(messageIDs) == 0 {
		return nil
	}
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	return mapError(r.s.q.RemoveUnreadMentionsOfMessages(ctx, sqlcdb.RemoveUnreadMentionsOfMessagesParams{ChatID: chatID, MessageIds: messageIDs}))
}

func (r sharingRepo) UnreadMentions(ctx context.Context, userID, chatID int64) ([]int64, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	ids, err := r.s.q.ListUnreadMentions(ctx, sqlcdb.ListUnreadMentionsParams{UserID: userID, ChatID: chatID})
	return ids, mapError(err)
}

func (r sharingRepo) UnreadMentionCounts(ctx context.Context, userID int64, chatIDs []int64) (map[int64]int, error) {
	out := map[int64]int{}
	if len(chatIDs) == 0 {
		return out, nil
	}
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	rows, err := r.s.q.CountUnreadMentions(ctx, sqlcdb.CountUnreadMentionsParams{UserID: userID, ChatIds: chatIDs})
	if err != nil {
		return nil, mapError(err)
	}
	for _, row := range rows {
		out[row.ChatID] = int(row.Mentions)
	}
	return out, nil
}
