package users

import (
	"context"
	"strconv"
	"time"

	"github.com/myronsi/messenger-back/internal/store/postgres"
)

// DeletedName is shown for a user whose account is gone.
const DeletedName = "Deleted account"

// deletedUsername satisfies the username pattern of the contract.
const deletedUsername = "deleted"

// View is a user as one viewer may see them: fields the user's privacy settings hide are nil or zero.
type View struct {
	ID          int64
	Username    string
	DisplayName string
	// ContactName is the viewer's own name for the user.
	ContactName *string
	// AvatarURL is the same-origin path of the avatar, never the storage location.
	AvatarURL *string
	Bio       *string
	IsOnline  bool
	LastSeen  *time.Time
	IsDeleted bool
	Findable  bool
}

// Visible reports whether a field guarded by a privacy setting with value setting is visible to a viewer,
// given the owner's exception for that viewer ("" for none) and whether the two share a chat.
// An exception wins over the setting: allow always shows, deny always hides. The owner always sees everything.
func Visible(setting, exception string, sharesChat bool) bool {
	switch exception {
	case postgres.EffectAllow:
		return true
	case postgres.EffectDeny:
		return false
	}
	switch setting {
	case postgres.VisibleEveryone:
		return true
	case postgres.VisibleSharedChats:
		return sharesChat
	case postgres.VisibleEveryoneExcept:
		return true
	}
	return false // nobody, and anything unknown
}

func Findable(setting, exception string) bool {
	switch exception {
	case postgres.EffectAllow:
		return true
	case postgres.EffectDeny:
		return false
	}
	return setting != postgres.VisibleNobody
}

// Online tells which users have a connection right now.
type Online interface {
	Online(ctx context.Context, userIDs ...int64) (map[int64]bool, error)
}

// Store is what the directory reads.
type Store interface {
	Users() postgres.UserRepository
	Social() postgres.SocialRepository
}

// Directory renders users as seen by other users.
type Directory struct {
	store    Store
	online   Online
	basePath string
}

// NewDirectory returns a directory; basePath is the API base path for avatar URLs (/api/v2).
func NewDirectory(store Store, online Online, basePath string) *Directory {
	return &Directory{store: store, online: online, basePath: basePath}
}

func AvatarPath(basePath string, u postgres.User) string {
	path := basePath + "/users/" + strconv.FormatInt(u.ID, 10) + "/avatar"
	if u.AvatarAttachmentID != nil {
		path += "?rev=" + u.AvatarAttachmentID.String()
	}
	return path
}

// AvatarURL is the same-origin path of a user's avatar.
func (d *Directory) AvatarURL(u postgres.User) string {
	return AvatarPath(d.basePath, u)
}

func (d *Directory) deleted(id int64) View {
	return View{ID: id, Username: deletedUsername, DisplayName: DeletedName, IsDeleted: true}
}

// facts is what decides one (subject, viewer) pair.
type facts struct {
	settings   postgres.PrivacySettings
	exceptions map[string]string
	shares     bool
	contact    *string
	online     bool
	// blocked: the subject blocked the viewer, who then sees nothing that privacy settings guard.
	blocked bool
}

func (d *Directory) render(u postgres.User, viewer int64, f facts) View {
	v := View{ID: u.ID, Username: u.Username, DisplayName: u.DisplayName, ContactName: f.contact}
	self := u.ID == viewer
	v.Findable = self || f.shares || Findable(f.settings.SearchVisibility, f.exceptions[postgres.SettingSearch])
	allowed := func(setting, key string) bool {
		return self || (!f.blocked && Visible(setting, f.exceptions[key], f.shares))
	}
	if u.HasAvatar() && allowed(f.settings.AvatarVisibility, postgres.SettingAvatar) {
		url := d.AvatarURL(u)
		v.AvatarURL = &url
	}
	if allowed(f.settings.ProfileVisibility, postgres.SettingProfile) {
		v.Bio = u.Bio
	}
	showOnline := allowed(f.settings.PresenceVisibility, postgres.SettingPresence)
	if showOnline {
		v.IsOnline = f.online
	}
	if allowed(f.settings.LastSeenVisibility, postgres.SettingLastSeen) && !v.IsOnline {
		seen := u.LastSeenAt
		v.LastSeen = &seen
	}
	return v
}

// ForViewer renders the users as one viewer sees them. Unknown ids come back as deleted accounts.
func (d *Directory) ForViewer(ctx context.Context, viewer int64, ids []int64) (map[int64]View, error) {
	out := make(map[int64]View, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	users, err := d.store.Users().GetMany(ctx, ids)
	if err != nil {
		return nil, err
	}
	social := d.store.Social()
	settings, err := social.Privacy(ctx, ids)
	if err != nil {
		return nil, err
	}
	exceptions, err := social.ExceptionsFor(ctx, ids, viewer)
	if err != nil {
		return nil, err
	}
	shared, err := social.SharedChatPartners(ctx, viewer, ids)
	if err != nil {
		return nil, err
	}
	names, err := social.ContactNames(ctx, viewer, ids)
	if err != nil {
		return nil, err
	}
	blocks, err := social.Blocks(ctx, ids, []int64{viewer})
	if err != nil {
		return nil, err
	}
	online := d.onlineOf(ctx, ids)
	for _, id := range ids {
		u, ok := users[id]
		if !ok {
			out[id] = d.deleted(id)
			continue
		}
		f := facts{settings: settings[id], exceptions: exceptions[id], shares: shared[id], online: online[id], blocked: blocks[[2]int64{id, viewer}]}
		if n, ok := names[id]; ok {
			f.contact = &n
		}
		out[id] = d.render(u, viewer, f)
	}
	return out, nil
}

// ToViewers renders one user as each of the viewers sees them, for example the sender of a message as seen by
// every member of the chat. sharesChat tells that every viewer shares a chat with the subject (they are in
// the same chat), which saves a query.
func (d *Directory) ToViewers(ctx context.Context, subject int64, viewers []int64, sharesChat bool) (map[int64]View, error) {
	out := make(map[int64]View, len(viewers))
	if len(viewers) == 0 {
		return out, nil
	}
	users, err := d.store.Users().GetMany(ctx, []int64{subject})
	if err != nil {
		return nil, err
	}
	u, ok := users[subject]
	if !ok {
		for _, v := range viewers {
			out[v] = d.deleted(subject)
		}
		return out, nil
	}
	social := d.store.Social()
	settings, err := social.Privacy(ctx, []int64{subject})
	if err != nil {
		return nil, err
	}
	exceptions, err := social.ExceptionsOf(ctx, subject, viewers)
	if err != nil {
		return nil, err
	}
	shared := map[int64]bool{}
	if !sharesChat {
		if shared, err = social.SharedChatViewers(ctx, subject, viewers); err != nil {
			return nil, err
		}
	}
	names, err := social.ContactNamesFor(ctx, viewers, subject)
	if err != nil {
		return nil, err
	}
	blocks, err := social.Blocks(ctx, []int64{subject}, viewers)
	if err != nil {
		return nil, err
	}
	online := d.onlineOf(ctx, []int64{subject})[subject]
	for _, v := range viewers {
		f := facts{settings: settings[subject], exceptions: exceptions[v], shares: sharesChat || shared[v], online: online, blocked: blocks[[2]int64{subject, v}]}
		if n, ok := names[v]; ok {
			f.contact = &n
		}
		out[v] = d.render(u, v, f)
	}
	return out, nil
}

// onlineOf asks presence; when Redis is unavailable everybody is shown offline rather than failing the call.
func (d *Directory) onlineOf(ctx context.Context, ids []int64) map[int64]bool {
	if d.online == nil {
		return nil
	}
	m, err := d.online.Online(ctx, ids...)
	if err != nil {
		return nil
	}
	return m
}

type PresenceAccess struct {
	Online   bool
	LastSeen bool
}

// PresenceVisibleTo returns what each viewer may see of the presence of subject.
func (d *Directory) PresenceVisibleTo(ctx context.Context, subject int64, viewers []int64, sharesChat bool) (map[int64]PresenceAccess, error) {
	out := make(map[int64]PresenceAccess, len(viewers))
	if len(viewers) == 0 {
		return out, nil
	}
	social := d.store.Social()
	settings, err := social.Privacy(ctx, []int64{subject})
	if err != nil {
		return nil, err
	}
	exceptions, err := social.ExceptionsOf(ctx, subject, viewers)
	if err != nil {
		return nil, err
	}
	shared := map[int64]bool{}
	if !sharesChat {
		if shared, err = social.SharedChatViewers(ctx, subject, viewers); err != nil {
			return nil, err
		}
	}
	blocks, err := social.Blocks(ctx, []int64{subject}, viewers)
	if err != nil {
		return nil, err
	}
	s := settings[subject]
	for _, v := range viewers {
		visible := func(setting, key string) bool {
			return v == subject || (!blocks[[2]int64{subject, v}] && Visible(setting, exceptions[v][key], sharesChat || shared[v]))
		}
		out[v] = PresenceAccess{
			Online:   visible(s.PresenceVisibility, postgres.SettingPresence),
			LastSeen: visible(s.LastSeenVisibility, postgres.SettingLastSeen),
		}
	}
	return out, nil
}
