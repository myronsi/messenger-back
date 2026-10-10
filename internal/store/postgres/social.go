package postgres

import (
	"context"
	"time"

	"github.com/myronsi/messenger-back/internal/store/postgres/sqlcdb"
)

// Visibility values of the privacy settings. Not every setting accepts every value (see the CHECK
// constraints of user_privacy_settings).
const (
	VisibleEveryone       = "everyone"
	VisibleSharedChats    = "shared_chats"
	VisibleEveryoneExcept = "everyone_except"
	VisibleNobodyExcept   = "nobody_except"
	VisibleNobody         = "nobody"
	WaitApproval          = "wait_approval"
)

// Setting keys that take per-user exceptions.
const (
	SettingAvatar       = "avatar_visibility"
	SettingProfile      = "profile_visibility"
	SettingPresence     = "presence_visibility"
	SettingGroupInvites = "group_invites"
)

// Exception effects.
const (
	EffectAllow = "allow"
	EffectDeny  = "deny"
)

// PrivacySettings is what a user lets others see and do.
type PrivacySettings struct {
	UserID              int64
	AvatarVisibility    string
	ProfileVisibility   string
	PresenceVisibility  string
	ReadReceiptsEnabled bool
	DirectMessages      string
	GroupInvites        string
	SearchVisibility    string
}

// DefaultPrivacy is what a user without a settings row gets (the column defaults).
func DefaultPrivacy(userID int64) PrivacySettings {
	return PrivacySettings{
		UserID: userID, AvatarVisibility: VisibleEveryone, ProfileVisibility: VisibleEveryone,
		PresenceVisibility: VisibleEveryone, ReadReceiptsEnabled: true, DirectMessages: VisibleEveryone,
		GroupInvites: VisibleEveryone, SearchVisibility: VisibleEveryone,
	}
}

// PrivacyException overrides a setting for one other user.
type PrivacyException struct {
	OwnerID    int64
	SettingKey string
	TargetID   int64
	Effect     string
	CreatedAt  time.Time
}

// Block is one user blocking another.
type Block struct {
	BlockerID int64
	BlockedID int64
	CreatedAt time.Time
}

// SocialRepository stores privacy settings and exceptions, blocks and contact names.
type SocialRepository interface {
	// Privacy returns the settings of the users; users without a row get DefaultPrivacy.
	Privacy(ctx context.Context, userIDs []int64) (map[int64]PrivacySettings, error)
	UpdatePrivacy(ctx context.Context, p PrivacySettings) (PrivacySettings, error)
	// Exceptions lists every exception the owner made.
	Exceptions(ctx context.Context, ownerID int64) ([]PrivacyException, error)
	// ExceptionsOf returns the owner's exceptions for each of the targets: target -> setting -> effect.
	ExceptionsOf(ctx context.Context, ownerID int64, targetIDs []int64) (map[int64]map[string]string, error)
	// ExceptionsFor returns what each owner decided for one target: owner -> setting -> effect.
	ExceptionsFor(ctx context.Context, ownerIDs []int64, targetID int64) (map[int64]map[string]string, error)
	SetException(ctx context.Context, e PrivacyException) error
	// ReplaceExceptions makes targets the complete list of the owner's exceptions with this setting and effect
	// (a target that had the other effect switches).
	ReplaceExceptions(ctx context.Context, ownerID int64, settingKey, effect string, targetIDs []int64) error
	DeleteException(ctx context.Context, ownerID int64, settingKey string, targetID int64) error

	Block(ctx context.Context, blockerID, blockedID int64) error
	Unblock(ctx context.Context, blockerID, blockedID int64) error
	Blocked(ctx context.Context, blockerID int64) ([]Block, error)
	// BlockedPage pages through the blocked users, newest first, after the given block (nil: from the start).
	BlockedPage(ctx context.Context, blockerID int64, after *Block, limit int) ([]Block, error)
	// BlockedEither reports whether one of the two users blocked the other.
	BlockedEither(ctx context.Context, a, b int64) (bool, error)

	SetContactName(ctx context.Context, ownerID, targetID int64, name string) error
	DeleteContactName(ctx context.Context, ownerID, targetID int64) error
	// ContactNames returns the owner's names for the targets.
	ContactNames(ctx context.Context, ownerID int64, targetIDs []int64) (map[int64]string, error)
	// ContactNamesFor returns the names that each owner gave the target.
	ContactNamesFor(ctx context.Context, ownerIDs []int64, targetID int64) (map[int64]string, error)

	// SharedChatPartners returns which of the users share at least one chat with the viewer.
	SharedChatPartners(ctx context.Context, viewerID int64, userIDs []int64) (map[int64]bool, error)
	// ChatIDs returns the chats of the user.
	ChatIDs(ctx context.Context, userID int64) ([]int64, error)
}

type socialRepo struct{ s *Store }

var _ SocialRepository = socialRepo{}

func privacyFrom(p sqlcdb.UserPrivacySetting) PrivacySettings {
	return PrivacySettings{
		UserID: p.UserID, AvatarVisibility: p.AvatarVisibility, ProfileVisibility: p.ProfileVisibility,
		PresenceVisibility: p.PresenceVisibility, ReadReceiptsEnabled: p.ReadReceiptsEnabled,
		DirectMessages: p.DirectMessages, GroupInvites: p.GroupInvites, SearchVisibility: p.SearchVisibility,
	}
}

func (r socialRepo) Privacy(ctx context.Context, userIDs []int64) (map[int64]PrivacySettings, error) {
	out := make(map[int64]PrivacySettings, len(userIDs))
	if len(userIDs) == 0 {
		return out, nil
	}
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	rows, err := r.s.q.ListPrivacySettings(ctx, userIDs)
	if err != nil {
		return nil, mapError(err)
	}
	for _, p := range rows {
		out[p.UserID] = privacyFrom(p)
	}
	for _, id := range userIDs {
		if _, ok := out[id]; !ok {
			out[id] = DefaultPrivacy(id)
		}
	}
	return out, nil
}

func (r socialRepo) UpdatePrivacy(ctx context.Context, p PrivacySettings) (PrivacySettings, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	row, err := r.s.q.UpdatePrivacySettings(ctx, sqlcdb.UpdatePrivacySettingsParams{
		UserID: p.UserID, AvatarVisibility: p.AvatarVisibility, ProfileVisibility: p.ProfileVisibility,
		PresenceVisibility: p.PresenceVisibility, ReadReceiptsEnabled: p.ReadReceiptsEnabled,
		DirectMessages: p.DirectMessages, GroupInvites: p.GroupInvites, SearchVisibility: p.SearchVisibility,
	})
	if err != nil {
		return PrivacySettings{}, mapError(err)
	}
	return privacyFrom(row), nil
}

func (r socialRepo) Exceptions(ctx context.Context, ownerID int64) ([]PrivacyException, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	rows, err := r.s.q.ListPrivacyExceptions(ctx, ownerID)
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]PrivacyException, len(rows))
	for i, e := range rows {
		out[i] = PrivacyException{OwnerID: e.OwnerID, SettingKey: e.SettingKey, TargetID: e.TargetUserID, Effect: e.Effect, CreatedAt: e.CreatedAt}
	}
	return out, nil
}

func groupExceptions(rows []sqlcdb.UserPrivacyException, key func(sqlcdb.UserPrivacyException) int64) map[int64]map[string]string {
	out := make(map[int64]map[string]string)
	for _, e := range rows {
		k := key(e)
		if out[k] == nil {
			out[k] = make(map[string]string)
		}
		out[k][e.SettingKey] = e.Effect
	}
	return out
}

func (r socialRepo) ExceptionsOf(ctx context.Context, ownerID int64, targetIDs []int64) (map[int64]map[string]string, error) {
	if len(targetIDs) == 0 {
		return map[int64]map[string]string{}, nil
	}
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	rows, err := r.s.q.ListPrivacyExceptionsOfOwnerFor(ctx, sqlcdb.ListPrivacyExceptionsOfOwnerForParams{OwnerID: ownerID, TargetIds: targetIDs})
	if err != nil {
		return nil, mapError(err)
	}
	return groupExceptions(rows, func(e sqlcdb.UserPrivacyException) int64 { return e.TargetUserID }), nil
}

func (r socialRepo) ExceptionsFor(ctx context.Context, ownerIDs []int64, targetID int64) (map[int64]map[string]string, error) {
	if len(ownerIDs) == 0 {
		return map[int64]map[string]string{}, nil
	}
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	rows, err := r.s.q.ListPrivacyExceptionsForTarget(ctx, sqlcdb.ListPrivacyExceptionsForTargetParams{OwnerIds: ownerIDs, TargetUserID: targetID})
	if err != nil {
		return nil, mapError(err)
	}
	return groupExceptions(rows, func(e sqlcdb.UserPrivacyException) int64 { return e.OwnerID }), nil
}

func (r socialRepo) SetException(ctx context.Context, e PrivacyException) error {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	return mapError(r.s.q.SetPrivacyException(ctx, sqlcdb.SetPrivacyExceptionParams{
		OwnerID: e.OwnerID, SettingKey: e.SettingKey, TargetUserID: e.TargetID, Effect: e.Effect,
	}))
}

func (r socialRepo) DeleteException(ctx context.Context, ownerID int64, settingKey string, targetID int64) error {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	n, err := r.s.q.DeletePrivacyException(ctx, sqlcdb.DeletePrivacyExceptionParams{OwnerID: ownerID, SettingKey: settingKey, TargetUserID: targetID})
	return affected(n, err)
}

func (r socialRepo) Block(ctx context.Context, blockerID, blockedID int64) error {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	return mapError(r.s.q.BlockUser(ctx, sqlcdb.BlockUserParams{BlockerID: blockerID, BlockedID: blockedID}))
}

func (r socialRepo) Unblock(ctx context.Context, blockerID, blockedID int64) error {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	n, err := r.s.q.UnblockUser(ctx, sqlcdb.UnblockUserParams{BlockerID: blockerID, BlockedID: blockedID})
	return affected(n, err)
}

func (r socialRepo) Blocked(ctx context.Context, blockerID int64) ([]Block, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	rows, err := r.s.q.ListBlocked(ctx, blockerID)
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]Block, len(rows))
	for i, b := range rows {
		out[i] = Block{BlockerID: b.BlockerID, BlockedID: b.BlockedID, CreatedAt: b.CreatedAt}
	}
	return out, nil
}

func (r socialRepo) BlockedEither(ctx context.Context, a, b int64) (bool, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	ok, err := r.s.q.BlockedEither(ctx, sqlcdb.BlockedEitherParams{A: a, B: b})
	return ok, mapError(err)
}

func (r socialRepo) SetContactName(ctx context.Context, ownerID, targetID int64, name string) error {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	return mapError(r.s.q.SetContactName(ctx, sqlcdb.SetContactNameParams{OwnerID: ownerID, TargetID: targetID, DisplayName: name}))
}

func (r socialRepo) DeleteContactName(ctx context.Context, ownerID, targetID int64) error {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	n, err := r.s.q.DeleteContactName(ctx, sqlcdb.DeleteContactNameParams{OwnerID: ownerID, TargetID: targetID})
	return affected(n, err)
}

func (r socialRepo) ContactNames(ctx context.Context, ownerID int64, targetIDs []int64) (map[int64]string, error) {
	out := make(map[int64]string)
	if len(targetIDs) == 0 {
		return out, nil
	}
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	rows, err := r.s.q.ListContactNames(ctx, sqlcdb.ListContactNamesParams{OwnerID: ownerID, TargetIds: targetIDs})
	if err != nil {
		return nil, mapError(err)
	}
	for _, c := range rows {
		out[c.TargetID] = c.DisplayName
	}
	return out, nil
}

func (r socialRepo) ContactNamesFor(ctx context.Context, ownerIDs []int64, targetID int64) (map[int64]string, error) {
	out := make(map[int64]string)
	if len(ownerIDs) == 0 {
		return out, nil
	}
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	rows, err := r.s.q.ListContactNamesForTarget(ctx, sqlcdb.ListContactNamesForTargetParams{OwnerIds: ownerIDs, TargetID: targetID})
	if err != nil {
		return nil, mapError(err)
	}
	for _, c := range rows {
		out[c.OwnerID] = c.DisplayName
	}
	return out, nil
}

func (r socialRepo) SharedChatPartners(ctx context.Context, viewerID int64, userIDs []int64) (map[int64]bool, error) {
	out := make(map[int64]bool)
	if len(userIDs) == 0 {
		return out, nil
	}
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	rows, err := r.s.q.SharedChatPartners(ctx, sqlcdb.SharedChatPartnersParams{ViewerID: viewerID, UserIds: userIDs})
	if err != nil {
		return nil, mapError(err)
	}
	for _, id := range rows {
		out[id] = true
	}
	return out, nil
}

func (r socialRepo) ChatIDs(ctx context.Context, userID int64) ([]int64, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	ids, err := r.s.q.ListChatIDsOfUser(ctx, userID)
	return ids, mapError(err)
}

func (r socialRepo) ReplaceExceptions(ctx context.Context, ownerID int64, settingKey, effect string, targetIDs []int64) error {
	return r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		if err := q.DeletePrivacyExceptionsByEffect(ctx, sqlcdb.DeletePrivacyExceptionsByEffectParams{OwnerID: ownerID, SettingKey: settingKey, Effect: effect}); err != nil {
			return err
		}
		for _, t := range targetIDs {
			if err := q.SetPrivacyException(ctx, sqlcdb.SetPrivacyExceptionParams{OwnerID: ownerID, SettingKey: settingKey, TargetUserID: t, Effect: effect}); err != nil {
				return err
			}
		}
		return nil
	})
}

func (r socialRepo) BlockedPage(ctx context.Context, blockerID int64, after *Block, limit int) ([]Block, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	p := sqlcdb.ListBlockedPageParams{BlockerID: blockerID, MaxRows: clampLimit(limit)}
	if after != nil {
		p.AfterCreated, p.AfterID = &after.CreatedAt, &after.BlockedID
	}
	rows, err := r.s.q.ListBlockedPage(ctx, p)
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]Block, len(rows))
	for i, b := range rows {
		out[i] = Block{BlockerID: blockerID, BlockedID: b.BlockedID, CreatedAt: b.CreatedAt}
	}
	return out, nil
}
