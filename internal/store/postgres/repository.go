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
	AvatarURL   *string
	Bio         *string
	LastSeenAt  time.Time
	CreatedAt   time.Time
}

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

// DeletedAccount reports what a deletion left behind for the caller to clean up.
type DeletedAccount struct {
	// AttachmentKeys are the object-storage keys of the files that belonged to chats removed with the
	// account. The rows are gone; delete the objects.
	AttachmentKeys []string
}

// UserRepository stores accounts.
type UserRepository interface {
	// Create fails with ErrUsernameTaken (case-insensitive) or ErrInvalid.
	Create(ctx context.Context, username, displayName, passwordHash string) (User, error)
	Get(ctx context.Context, id int64) (User, error)
	GetByUsername(ctx context.Context, username string) (User, error)
	Credentials(ctx context.Context, username string) (Credentials, error)
	UpdateProfile(ctx context.Context, id int64, p Profile) (User, error)
	SetPasswordHash(ctx context.Context, id int64, passwordHash string) error
	TouchLastSeen(ctx context.Context, id int64) error
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
	CreatedBy   *int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Participant is a user's membership of a chat.
type Participant struct {
	ChatID            int64
	UserID            int64
	Role              Role
	LastReadMessageID *int64
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
	// MarkRead moves the read marker of a participant forward (never back).
	MarkRead(ctx context.Context, chatID, userID, messageID int64) error
	// Delete removes the chat and returns the object-storage keys of its attachments.
	Delete(ctx context.Context, chatID int64) (attachmentKeys []string, err error)
}

// Attachment is the metadata of an uploaded file.
type Attachment struct {
	ID         uuid.UUID
	UploaderID *int64
	ChatID     int64
	StorageKey string
	MimeType   string
	Size       int64
	Width      *int32
	Height     *int32
	// Duration is in seconds (audio and video).
	Duration  *float64
	Waveform  []int16
	CreatedAt time.Time
}

// NewAttachment describes a file that was stored under StorageKey.
type NewAttachment struct {
	UploaderID *int64
	ChatID     int64
	StorageKey string
	MimeType   string
	Size       int64
	Width      *int32
	Height     *int32
	Duration   *float64
	Waveform   []int16
}

// AttachmentRepository stores attachment metadata.
type AttachmentRepository interface {
	Create(ctx context.Context, a NewAttachment) (Attachment, error)
	Get(ctx context.Context, id uuid.UUID) (Attachment, error)
	// ListByChat returns the newest attachments of a chat. limit is capped at 500.
	ListByChat(ctx context.Context, chatID int64, limit int) ([]Attachment, error)
	// Delete removes the row and returns the storage key, so the caller can delete the object.
	Delete(ctx context.Context, id uuid.UUID) (storageKey string, err error)
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
