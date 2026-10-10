package postgres

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/myronsi/messenger-back/internal/store/postgres/sqlcdb"
)

type userRepo struct{ s *Store }

var _ UserRepository = userRepo{}

func userFrom(u sqlcdb.User) User {
	return User{
		ID:          u.ID,
		Username:    u.Username,
		DisplayName: u.DisplayName,
		AvatarURL:   u.AvatarUrl,
		Bio:         u.Bio,
		LastSeenAt:  u.LastSeenAt,
		CreatedAt:   u.CreatedAt,

		AvatarAttachmentID: u.AvatarAttachmentID,
	}
}

func (r userRepo) Create(ctx context.Context, username, displayName, passwordHash string) (User, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	u, err := r.s.q.CreateUser(ctx, sqlcdb.CreateUserParams{Username: username, DisplayName: displayName, PasswordHash: passwordHash})
	if err != nil {
		return User{}, mapError(err)
	}
	return userFrom(u), nil
}

func (r userRepo) Get(ctx context.Context, id int64) (User, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	u, err := r.s.q.GetUser(ctx, id)
	if err != nil {
		return User{}, mapError(err)
	}
	return userFrom(u), nil
}

func (r userRepo) GetMany(ctx context.Context, ids []int64) (map[int64]User, error) {
	out := make(map[int64]User, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	rows, err := r.s.q.ListUsersByIDs(ctx, ids)
	if err != nil {
		return nil, mapError(err)
	}
	for _, u := range rows {
		out[u.ID] = userFrom(u)
	}
	return out, nil
}

func (r userRepo) GetByUsername(ctx context.Context, username string) (User, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	u, err := r.s.q.GetUserByUsername(ctx, username)
	if err != nil {
		return User{}, mapError(err)
	}
	return userFrom(u), nil
}

func (r userRepo) Credentials(ctx context.Context, username string) (Credentials, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	u, err := r.s.q.GetUserByUsername(ctx, username)
	if err != nil {
		return Credentials{}, mapError(err)
	}
	return Credentials{UserID: u.ID, PasswordHash: u.PasswordHash}, nil
}

func (r userRepo) UpdateProfile(ctx context.Context, id int64, p Profile) (User, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	u, err := r.s.q.UpdateUserProfile(ctx, sqlcdb.UpdateUserProfileParams{
		ID: id, DisplayName: p.DisplayName, Bio: p.Bio, AvatarUrl: p.AvatarURL,
	})
	if err != nil {
		return User{}, mapError(err)
	}
	return userFrom(u), nil
}

func (r userRepo) SetPasswordHash(ctx context.Context, id int64, passwordHash string) error {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	n, err := r.s.q.SetPasswordHash(ctx, sqlcdb.SetPasswordHashParams{ID: id, PasswordHash: passwordHash})
	return affected(n, err)
}

func (r userRepo) Register(ctx context.Context, username, displayName, passwordHash string) (User, error) {
	var out User
	err := r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		u, err := q.CreateUser(ctx, sqlcdb.CreateUserParams{Username: username, DisplayName: displayName, PasswordHash: passwordHash})
		if err != nil {
			return err
		}
		if err := q.EnsureSecuritySettings(ctx, u.ID); err != nil {
			return err
		}
		if err := q.EnsurePrivacySettings(ctx, u.ID); err != nil {
			return err
		}
		out = userFrom(u)
		return nil
	})
	if err != nil {
		return User{}, err
	}
	return out, nil
}

func (r userRepo) CredentialsByID(ctx context.Context, id int64) (Credentials, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	u, err := r.s.q.GetUser(ctx, id)
	if err != nil {
		return Credentials{}, mapError(err)
	}
	return Credentials{UserID: u.ID, PasswordHash: u.PasswordHash}, nil
}

func (r userRepo) RehashPassword(ctx context.Context, id int64, oldHash, newHash string) (bool, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	n, err := r.s.q.RehashPassword(ctx, sqlcdb.RehashPasswordParams{ID: id, OldHash: oldHash, NewHash: newHash})
	return n == 1, mapError(err)
}

func (r userRepo) ChangePassword(ctx context.Context, id int64, oldHash, newHash string, keepID uuid.UUID) ([]uuid.UUID, error) {
	var revoked []uuid.UUID
	err := r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		revoked = nil // the transaction can be retried
		if _, err := q.LockUser(ctx, id); err != nil {
			return err
		}
		n, err := q.RehashPassword(ctx, sqlcdb.RehashPasswordParams{ID: id, OldHash: oldHash, NewHash: newHash})
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrConflict
		}
		revoked, err = q.RevokeOtherSessions(ctx, sqlcdb.RevokeOtherSessionsParams{UserID: id, KeepID: keepID})
		return err
	})
	if err != nil {
		return nil, err
	}
	return revoked, nil
}

func (r userRepo) TouchLastSeen(ctx context.Context, id int64) error {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	n, err := r.s.q.TouchLastSeen(ctx, id)
	return affected(n, err)
}

func (r userRepo) DeleteAccount(ctx context.Context, id int64) (DeletedAccount, error) {
	var out DeletedAccount
	err := r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		out = DeletedAccount{} // the transaction can be retried
		// Concurrent writers that reference the user wait for this lock instead of racing the deletion.
		if _, err := q.LockUser(ctx, id); err != nil {
			return err
		}
		// Lock every chat that references the user (member, creator, uploader, invitation party or pinner) in id order before reading roles: a concurrent ownership
		// transfer holds the same lock, so the roles read below cannot change underneath us.
		if _, err := q.LockChatsOfUser(ctx, id); err != nil {
			return err
		}
		// A direct chat cannot outlive one of its two sides.
		doomed, err := q.ListDirectChatIDsOfUser(ctx, id)
		if err != nil {
			return err
		}
		owned, err := q.ListOwnedGroupIDs(ctx, id)
		if err != nil {
			return err
		}
		for _, chatID := range owned {
			next, err := q.NextOwnerCandidate(ctx, sqlcdb.NextOwnerCandidateParams{ChatID: chatID, LeavingUserID: id})
			if errors.Is(err, pgx.ErrNoRows) {
				doomed = append(doomed, chatID) // nobody else is in the group
				continue
			}
			if err != nil {
				return err
			}
			// Demote first: a group has at most one owner at any moment.
			if err := setRole(ctx, q, chatID, id, RoleMember); err != nil {
				return err
			}
			if err := setRole(ctx, q, chatID, next, RoleOwner); err != nil {
				return err
			}
		}

		if out.AttachmentKeys, err = q.ListAttachmentKeysOfChats(ctx, doomed); err != nil {
			return err
		}
		for _, chatID := range doomed {
			if _, err := q.DeleteChat(ctx, chatID); err != nil {
				return err
			}
		}
		_, err = q.DeleteUser(ctx, id)
		return err
	})
	if err != nil {
		return DeletedAccount{}, err
	}
	return out, nil
}

// affected turns "no row changed" into ErrNotFound.
func affected(n int64, err error) error {
	if err != nil {
		return mapError(err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func setRole(ctx context.Context, q *sqlcdb.Queries, chatID, userID int64, role Role) error {
	n, err := q.SetParticipantRole(ctx, sqlcdb.SetParticipantRoleParams{ChatID: chatID, UserID: userID, Role: string(role)})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
