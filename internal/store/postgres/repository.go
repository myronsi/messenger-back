package postgres

import (
	"context"
	"encoding/json"
	"net/netip"
	"time"

	"github.com/google/uuid"
)

// User is an account. The password hash is not part of it; see UserRepository.Credentials.
type User struct {
	ID          int64
	Username    string
	DisplayName string
	// AvatarURL is the v1 avatar path; AvatarAttachmentID the v2 avatar (an attachment with purpose avatar).
	AvatarURL          *string
	AvatarAttachmentID *uuid.UUID
	Bio                *string
	LastSeenAt         time.Time
	CreatedAt          time.Time
}

// HasAvatar reports whether the user has an avatar of either generation.
func (u User) HasAvatar() bool { return u.AvatarAttachmentID != nil || u.AvatarURL != nil }

// Credentials is what login needs.
type Credentials struct {
	UserID       int64
	PasswordHash string
}

// Profile is the editable part of an account.
type Profile struct {
	DisplayName string
	Bio         *string
	AvatarURL   *string
}

// ProfilePatch changes some profile fields: a nil DisplayName stays, Bio is set (possibly to nil) when SetBio.
type ProfilePatch struct {
	DisplayName *string
	SetBio      bool
	Bio         *string
}

// DeletedAccount reports what a deletion left behind for the caller to clean up.
type DeletedAccount struct {
	// AttachmentKeys are the object-storage keys of the files that belonged to chats removed with the
	// account. The rows are gone; delete the objects.
	AttachmentKeys []string
	// DeletedChats are the chats removed with the account (its direct chats, groups nobody else was in), each
	// with the other members it had.
	DeletedChats map[int64][]int64
	// LeftGroups are the groups the user was removed from (they pass to the next owner).
	LeftGroups []int64
	// Sessions are the sessions deleted with the account (to drop from caches and close their sockets).
	Sessions []uuid.UUID
}

// UserRepository stores accounts.
type UserRepository interface {
	// Create fails with ErrUsernameTaken (case-insensitive) or ErrInvalid.
	Create(ctx context.Context, username, displayName, passwordHash string) (User, error)
	Get(ctx context.Context, id int64) (User, error)
	// GetMany loads several users in one query, for example the senders of a page of messages. Unknown ids
	// are missing from the result.
	GetMany(ctx context.Context, ids []int64) (map[int64]User, error)
	GetByUsername(ctx context.Context, username string) (User, error)
	Credentials(ctx context.Context, username string) (Credentials, error)
	UpdateProfile(ctx context.Context, id int64, p Profile) (User, error)
	// PatchProfile changes only the given fields.
	PatchProfile(ctx context.Context, id int64, p ProfilePatch) (User, error)
	SetPasswordHash(ctx context.Context, id int64, passwordHash string) error
	// Register creates the account together with its settings rows in one transaction.
	Register(ctx context.Context, username, displayName, passwordHash string) (User, error)
	CredentialsByID(ctx context.Context, id int64) (Credentials, error)
	// RehashPassword replaces oldHash by newHash only if oldHash is still the stored hash; false otherwise.
	RehashPassword(ctx context.Context, id int64, oldHash, newHash string) (bool, error)
	// ChangePassword sets the new hash if oldHash is still the stored one (ErrConflict otherwise) and
	// revokes every session except keepID in the same transaction. It returns the revoked ids.
	ChangePassword(ctx context.Context, id int64, oldHash, newHash string, keepID uuid.UUID) ([]uuid.UUID, error)
	TouchLastSeen(ctx context.Context, id int64) error
	// Search finds users for the viewer: a username prefix or a part of the display name (case-insensitive),
	// without users who opted out of search or blocked the viewer, ordered by username after `after`.
	Search(ctx context.Context, viewerID int64, query, after string, limit int) ([]User, error)
	// DeleteAccount removes the account in one transaction: its direct chats, the groups nobody else is
	// in, and everything that references the user. Groups it owned pass to the next admin, moderator or
	// longest member. Files it uploaded to chats that survive stay, without an uploader.
	DeleteAccount(ctx context.Context, id int64) (DeletedAccount, error)
}

// ChatType is the kind of a chat.
type ChatType string

// The two chat types.
const (
	ChatDirect ChatType = "direct"
	ChatGroup  ChatType = "group"
)

// Role is a participant's role. Direct chats only have members.
type Role string

// Roles of a participant.
const (
	RoleOwner     Role = "owner"
	RoleAdmin     Role = "admin"
	RoleModerator Role = "moderator"
	RoleMember    Role = "member"
)

// Chat is a direct chat or a group. Name is set for groups only.
type Chat struct {
	ID          int64
	Type        ChatType
	Name        *string
	Description string
	AvatarURL   *string
	// AvatarAttachmentID is the v2 group avatar.
	AvatarAttachmentID *uuid.UUID
	CreatedBy          *int64
	CreatedAt          time.Time
	UpdatedAt          time.Time
	// LastMessageID is the newest message (nil before the first one); LastActivityAt orders the chat list.
	LastMessageID  *int64
	LastActivityAt time.Time
}

// ChatEntry is a chat in one user's list: the chat and the user's own relation to it.
type ChatEntry struct {
	Chat              Chat
	Role              Role
	LastReadMessageID *int64
	PinnedAt          *time.Time
	// SortAt is the pin time of a pinned chat and the last activity of any other.
	SortAt time.Time
}

// ChatCursor continues a chat list after the entry it was made from.
type ChatCursor struct {
	Unpinned bool
	SortAt   time.Time
	ChatID   int64
}

// CursorOf returns the cursor that continues after the entry.
func (e ChatEntry) CursorOf() ChatCursor {
	return ChatCursor{Unpinned: e.PinnedAt == nil, SortAt: e.SortAt, ChatID: e.Chat.ID}
}

// ReadMarker is how far a member has read a chat.
type ReadMarker struct {
	ChatID    int64
	UserID    int64
	MessageID *int64
	At        *time.Time
}

// Approval request types and statuses.
const (
	RequestDirectMessage = "direct_message"
	RequestGroupInvite   = "group_invite"
	RequestPending       = "pending"
	RequestApproved      = "approved"
	RequestRejected      = "rejected"
)

// ApprovalRequest asks a user to accept a direct chat or a group invitation.
type ApprovalRequest struct {
	ID          int64
	Type        string
	RequesterID int64
	RecipientID int64
	Status      string
	MessageText *string
	ChatID      *int64
	CreatedAt   time.Time
	RespondedAt *time.Time
}

// Participant is a user's membership of a chat.
type Participant struct {
	ChatID            int64
	UserID            int64
	Role              Role
	LastReadMessageID *int64
	LastReadAt        *time.Time
	JoinedAt          time.Time
}

// NewGroup describes a group to create.
type NewGroup struct {
	OwnerID     int64
	Name        string
	Description string
	AvatarURL   *string
	// MemberIDs are added as members next to the owner; duplicates and the owner itself are ignored.
	MemberIDs []int64
}

// ChatRepository stores chats and who is in them.
type ChatRepository interface {
	// CreateDirect returns the chat between two users, creating it when it does not exist yet. The pair
	// is unordered: (a, b) and (b, a) give the same chat. Chatting with yourself is ErrInvalid.
	CreateDirect(ctx context.Context, creatorID, otherID int64) (chat Chat, created bool, err error)
	// CreateGroup creates the group and all its participants in one transaction.
	CreateGroup(ctx context.Context, g NewGroup) (Chat, error)
	Get(ctx context.Context, id int64) (Chat, error)
	// ListForUser returns the chats of a user, most recently changed first. limit is capped at 500.
	ListForUser(ctx context.Context, userID int64, limit int) ([]Chat, error)
	Participants(ctx context.Context, chatID int64) ([]Participant, error)
	Participant(ctx context.Context, chatID, userID int64) (Participant, error)
	// AddMember adds a member to a group (ErrNotGroup, ErrAlreadyParticipant).
	AddMember(ctx context.Context, chatID, userID int64) error
	// RemoveMember removes a participant from a group. The owner must transfer ownership first.
	RemoveMember(ctx context.Context, chatID, userID int64) error
	// SetRole sets admin, moderator or member. Ownership moves with TransferOwnership only.
	SetRole(ctx context.Context, chatID, userID int64, role Role) error
	// TransferOwnership makes toID the owner; the previous owner becomes an admin.
	TransferOwnership(ctx context.Context, chatID, fromID, toID int64) error
	// MarkRead moves the read marker of a participant forward (never back) and reports whether it moved.
	// ErrNotFound: the user is not in the chat.
	MarkRead(ctx context.Context, chatID, userID, messageID int64) (advanced bool, err error)
	// Delete removes the chat and returns the object-storage keys of its attachments.
	Delete(ctx context.Context, chatID int64) (attachmentKeys []string, err error)

	// OpenDirect is CreateDirect that also closes the pair's pending direct-message requests and returns them.
	OpenDirect(ctx context.Context, creatorID, otherID int64) (chat Chat, created bool, closed []ApprovalRequest, err error)
	// DirectBetween returns the direct chat of two users (ErrNotFound when they have none).
	DirectBetween(ctx context.Context, a, b int64) (Chat, error)
	// TouchActivity records a new message: the chat moves up in its members' lists.
	TouchActivity(ctx context.Context, chatID, messageID int64, at time.Time) error
	// Entries pages through a user's chats: pinned first, then by last activity (after: nil from the start).
	Entries(ctx context.Context, userID int64, after *ChatCursor, limit int) ([]ChatEntry, error)
	// Entry is one chat of the user's list; ErrNotFound when the user is not in it.
	Entry(ctx context.Context, userID, chatID int64) (ChatEntry, error)
	// OtherMembers returns the read markers of the other members of the chats.
	OtherMembers(ctx context.Context, userID int64, chatIDs []int64) ([]ReadMarker, error)
	// Pin pins a chat of the user; ErrConflict when maxPins chats are pinned already, ErrNotFound when the
	// user is not in the chat. Pinning a pinned chat changes nothing.
	Pin(ctx context.Context, userID, chatID int64, maxPins int) error
	// Unpin unpins the chat (unpinned already is fine).
	Unpin(ctx context.Context, userID, chatID int64) error

	// EntriesOfType is Entries of one chat type ("" for all).
	EntriesOfType(ctx context.Context, userID int64, t ChatType, after *ChatCursor, limit int) ([]ChatEntry, error)

	// Group operations of an actor: the actor's role is checked under the group's lock, in the transaction
	// of the change. ErrNotFound: no such group or the actor is not in it; ErrForbidden: the actor's role does
	// not allow it (managing a group needs owner or admin; deleting it the owner).
	UpdateGroup(ctx context.Context, chatID, actorID int64, name *string, setDescription bool, description *string) (Chat, error)
	// AddMemberAs adds a member (ErrAlreadyParticipant).
	AddMemberAs(ctx context.Context, chatID, actorID, userID int64) error
	// RemoveMemberAs removes a member; actorID == userID is leaving, which needs no role. The owner cannot be
	// removed and cannot leave (ErrOwnerMustTransfer). The member's pin of the group goes too.
	RemoveMemberAs(ctx context.Context, chatID, actorID, userID int64) error
	// SetRoleAs makes a member an admin, moderator or member; the owner's role moves with TransferOwnership.
	SetRoleAs(ctx context.Context, chatID, actorID, userID int64, role Role) error
	// DeleteGroupAs deletes the group (the owner only) and returns its files' keys and its members.
	DeleteGroupAs(ctx context.Context, chatID, actorID int64) (keys []string, members []int64, err error)
	// SetGroupAvatarAs sets (or with nil clears) the group's avatar.
	SetGroupAvatarAs(ctx context.Context, chatID, actorID int64, attachmentID *uuid.UUID) error
	// InviteAs makes a group invitation for a user who approves invitations (a pending one is reused).
	InviteAs(ctx context.Context, chatID, actorID, userID int64) (req ApprovalRequest, created bool, err error)
	// PendingInvitees are the users invited to the group who did not answer yet.
	PendingInvitees(ctx context.Context, chatID int64) ([]int64, error)
}

// ApprovalRepository keeps the approval requests.
type ApprovalRepository interface {
	// RequestDirect asks the recipient for a direct chat; a pending request of the pair is returned as it is
	// (created false). ErrConflict: the pair has a chat (opened meanwhile).
	RequestDirect(ctx context.Context, requesterID, recipientID int64, message *string) (req ApprovalRequest, created bool, err error)
	Get(ctx context.Context, id int64) (ApprovalRequest, error)
	// Pending is the recipient's inbox, newest first, before the request id (0: from the start).
	Pending(ctx context.Context, recipientID, beforeID int64, limit int) ([]ApprovalRequest, error)
	// ApproveDirect accepts a pending direct-message request of the recipient and returns the chat it opens
	// (created false when the pair had one) and the other requests of the pair it closed. ErrNotFound: no
	// such direct-message request for this recipient; ErrConflict: it was answered already.
	ApproveDirect(ctx context.Context, id, recipientID int64) (req ApprovalRequest, chat Chat, created bool, closed []ApprovalRequest, err error)
	// ApproveInvite accepts a pending group invitation of the recipient: they join the group.
	ApproveInvite(ctx context.Context, id, recipientID int64) (ApprovalRequest, error)
	// Reject turns down a pending request of the recipient (ErrNotFound, ErrConflict as above).
	Reject(ctx context.Context, id, recipientID int64) (ApprovalRequest, error)
}

// Attachment purposes and kinds (see media).
const (
	PurposeMessage = "message"
	PurposeAvatar  = "avatar"
)

// Attachment is the metadata of an uploaded file.
type Attachment struct {
	ID         uuid.UUID
	UploaderID *int64
	// ChatID is set for v1 attachments, which belonged to one chat from the upload on. v2 uploads are linked
	// to messages instead (AttachmentLink).
	ChatID       *int64
	Purpose      string
	Kind         string
	Filename     string
	StorageKey   string
	ThumbnailKey *string
	MimeType     string
	Size         int64
	Width        *int32
	Height       *int32
	// Duration is in seconds (audio and video).
	Duration  *float64
	Waveform  []int16
	CreatedAt time.Time
}

// NewAttachment describes a file that was stored under StorageKey.
type NewAttachment struct {
	UploaderID   *int64
	ChatID       *int64
	Purpose      string
	Kind         string
	Filename     string
	StorageKey   string
	ThumbnailKey *string
	MimeType     string
	Size         int64
	Width        *int32
	Height       *int32
	Duration     *float64
	Waveform     []int16
}

// AttachmentLink is a message that uses an attachment.
type AttachmentLink struct {
	ChatID    int64
	MessageID int64
}

// LinkedAttachment is an attachment as it appears in a chat's media list.
type LinkedAttachment struct {
	Attachment
	MessageID int64
	LinkedAt  time.Time
}

// UnreferencedAttachment is an upload nothing uses.
type UnreferencedAttachment struct {
	ID           uuid.UUID
	StorageKey   string
	ThumbnailKey *string
}

// AvatarVersion is an entry of a user's avatar history.
type AvatarVersion struct {
	ID           int64
	AttachmentID uuid.UUID
	IsCurrent    bool
	CreatedAt    time.Time
}

// AttachmentRepository stores attachment metadata, the links of attachments to messages and avatars.
type AttachmentRepository interface {
	Create(ctx context.Context, a NewAttachment) (Attachment, error)
	Get(ctx context.Context, id uuid.UUID) (Attachment, error)
	// ListByChat returns the newest v1 attachments of a chat. limit is capped at 500.
	ListByChat(ctx context.Context, chatID int64, limit int) ([]Attachment, error)
	// Delete removes the row and returns the storage keys (file and thumbnail), so the caller can delete the objects.
	Delete(ctx context.Context, id uuid.UUID) (keys []string, err error)
	// Link records that a message uses the attachment (idempotent).
	Link(ctx context.Context, attachmentID uuid.UUID, chatID, messageID int64) error
	// LinksForViewer returns the messages using the attachment in chats the viewer is a member of (at most 50).
	LinksForViewer(ctx context.Context, attachmentID uuid.UUID, viewerID int64) ([]AttachmentLink, error)
	// ListLinked returns a chat's attachments of the kinds, newest first, linked before `before` (zero: now).
	ListLinked(ctx context.Context, chatID int64, kinds []string, beforeMessageID int64, limit int) ([]LinkedAttachment, error)
	// Unreferenced lists uploads older than the time that nothing uses.
	Unreferenced(ctx context.Context, olderThan time.Time, limit int) ([]UnreferencedAttachment, error)
	// DeleteIfUnreferenced deletes the row when it is still unused and returns its storage keys; deleted is false
	// when something started using it meanwhile.
	DeleteIfUnreferenced(ctx context.Context, id uuid.UUID) (keys []string, deleted bool, err error)
	// SetUserAvatar makes the attachment the user's avatar and the current entry of the history; nil removes
	// the avatar.
	SetUserAvatar(ctx context.Context, userID int64, attachmentID *uuid.UUID) error
	// AvatarHistory returns the user's avatars, newest first, with ids below beforeID (0: from the newest).
	AvatarHistory(ctx context.Context, userID, beforeID int64, limit int) ([]AvatarVersion, error)
	// SetChatAvatar sets or (nil) removes the avatar of a group.
	SetChatAvatar(ctx context.Context, chatID int64, attachmentID *uuid.UUID) error
	// ChatsWithAvatar returns the groups that use the attachment as their avatar.
	ChatsWithAvatar(ctx context.Context, attachmentID uuid.UUID) ([]int64, error)
}

// SecurityEvent is one entry of the security log of an account.
type SecurityEvent struct {
	ID        int64
	UserID    int64
	Type      string
	IP        *netip.Addr
	UserAgent *string
	Details   json.RawMessage
	CreatedAt time.Time
}

// NewSecurityEvent describes an event to record.
type NewSecurityEvent struct {
	UserID    int64
	Type      string
	IP        *netip.Addr
	UserAgent *string
	// Details is a JSON object; leave empty for none. Never put secrets, tokens or message content in it.
	Details json.RawMessage
}

// SecurityEventRepository stores the security log.
type SecurityEventRepository interface {
	Record(ctx context.Context, e NewSecurityEvent) (SecurityEvent, error)
	// List returns the newest events first. Pass the ID of the last event of the previous page as
	// beforeID to continue; limit is capped at 500.
	List(ctx context.Context, userID int64, beforeID *int64, limit int) ([]SecurityEvent, error)
}

const (
	defaultListLimit = 50
	maxListLimit     = 500
)

func clampLimit(limit int) int32 {
	switch {
	case limit <= 0:
		return defaultListLimit
	case limit > maxListLimit:
		return maxListLimit
	}
	return int32(limit)
}
