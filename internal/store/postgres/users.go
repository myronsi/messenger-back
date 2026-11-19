package postgres

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

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
		ProfileVersion:     u.ProfileVersion,
		Pronouns:           u.Pronouns,
		Birthday:           birthdayFrom(u.BirthdayDay, u.BirthdayMonth, u.BirthdayYear),
		TimeZone:           u.TimeZone,
		MusicAttachmentID:  u.MusicAttachmentID,
		MusicTitle:         u.MusicTitle,
		MusicArtist:        u.MusicArtist,
		Background:         backgroundFrom(u.BackgroundKind, u.BackgroundAttachmentID, u.BackgroundPreset, u.BackgroundColors),
		TermsVersion:       u.TermsVersion,
		TermsAcceptedAt:    u.TermsAcceptedAt,
		AgeConfirmedAt:     u.AgeConfirmedAt,
	}
}

func birthdayFrom(day, month, year *int16) *Birthday {
	if day == nil || month == nil {
		return nil
	}
	b := &Birthday{Day: int(*day), Month: int(*month)}
	if year != nil {
		y := int(*year)
		b.Year = &y
	}
	return b
}

func backgroundFrom(kind *string, attachmentID *uuid.UUID, preset *string, colors []string) *Background {
	if kind == nil || (*kind == BackgroundMedia && attachmentID == nil) {
		return nil
	}
	return &Background{Kind: *kind, AttachmentID: attachmentID, Preset: preset, Colors: colors}
}

func smallint(v int) *int16 {
	n := int16(v) //nolint:gosec // birthday parts are validated and checked by the schema
	return &n
}

func birthdayParams(b *Birthday) (day, month, year *int16) {
	if b == nil {
		return nil, nil, nil
	}
	day, month = smallint(b.Day), smallint(b.Month)
	if b.Year != nil {
		year = smallint(*b.Year)
	}
	return day, month, year
}

func backgroundParams(bg *Background) (kind *string, attachmentID *uuid.UUID, preset *string, colors []string) {
	if bg == nil {
		return nil, nil, nil, nil
	}
	k := bg.Kind
	return &k, bg.AttachmentID, bg.Preset, bg.Colors
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

func (r userRepo) PatchProfile(ctx context.Context, id int64, p ProfilePatch) (User, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	day, month, year := birthdayParams(p.Birthday)
	u, err := r.s.q.PatchUserProfile(ctx, sqlcdb.PatchUserProfileParams{
		ID: id, DisplayName: p.DisplayName, SetBio: p.SetBio, Bio: p.Bio, SetPronouns: p.SetPronouns, Pronouns: p.Pronouns,
		SetBirthday: p.SetBirthday, BirthdayDay: day, BirthdayMonth: month, BirthdayYear: year, TimeZone: p.TimeZone,
	})
	if err != nil {
		return User{}, mapError(err)
	}
	return userFrom(u), nil
}

func (r userRepo) SetMusic(ctx context.Context, id int64, attachmentID *uuid.UUID, title, artist *string) (User, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	if attachmentID == nil {
		title, artist = nil, nil
	}
	u, err := r.s.q.SetUserMusic(ctx, sqlcdb.SetUserMusicParams{ID: id, AttachmentID: attachmentID, Title: title, Artist: artist})
	if err != nil {
		return User{}, mapError(err)
	}
	return userFrom(u), nil
}

func (r userRepo) SetBackground(ctx context.Context, id int64, bg *Background) (User, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	kind, attachmentID, preset, colors := backgroundParams(bg)
	u, err := r.s.q.SetUserBackground(ctx, sqlcdb.SetUserBackgroundParams{ID: id, Kind: kind, AttachmentID: attachmentID, Preset: preset, Colors: colors})
	if err != nil {
		return User{}, mapError(err)
	}
	return userFrom(u), nil
}

func (r userRepo) BirthdayCandidates(ctx context.Context, now time.Time, fromHour, limit int) ([]BirthdayCandidate, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	months, days := birthdayWindow(now)
	rows, err := r.s.q.ListBirthdayCandidates(ctx, sqlcdb.ListBirthdayCandidatesParams{
		Now: now, FromHour: int32(fromHour), MaxRows: clampLimit(limit), Months: months, Days: days, //nolint:gosec // an hour of the day
	})
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]BirthdayCandidate, len(rows))
	for i, row := range rows {
		out[i] = BirthdayCandidate{ReceiverID: row.ReceiverID, UserID: row.UserID, DisplayName: row.DisplayName, ChatID: row.ChatID, Year: int(row.LocalYear)}
	}
	return out, nil
}

func (r userRepo) KnownTimeZone(ctx context.Context, name string) (bool, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	var ok bool
	err := r.s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_timezone_names WHERE name = $1)`, name).Scan(&ok)
	return ok, mapError(err)
}

func birthdayWindow(now time.Time) (months, days []int16) {
	seen := map[[2]int16]bool{}
	add := func(month, day int16) {
		if seen[[2]int16{month, day}] {
			return
		}
		seen[[2]int16{month, day}] = true
		months, days = append(months, month), append(days, day)
	}
	for _, offset := range []time.Duration{-12 * time.Hour, 0, 14 * time.Hour} {
		t := now.UTC().Add(offset)
		add(int16(t.Month()), int16(t.Day())) //nolint:gosec // a month and a day
		if t.Month() == time.February && t.Day() == 28 {
			add(2, 29)
		}
	}
	return months, days
}

func (r userRepo) MarkBirthdayReminder(ctx context.Context, userID, receiverID int64, year int) (bool, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	n, err := r.s.q.MarkBirthdayReminder(ctx, sqlcdb.MarkBirthdayReminderParams{UserID: userID, ReceiverID: receiverID, Year: *smallint(year)})
	if err != nil {
		return false, mapError(err)
	}
	return n > 0, nil
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
	return r.s.deleteAccount(ctx, id, nil)
}

func (s *Store) deleteAccount(ctx context.Context, id int64, stillDue func(ctx context.Context, tx pgx.Tx) error) (DeletedAccount, error) {
	var out DeletedAccount
	err := s.inRawTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		out = DeletedAccount{DeletedChats: map[int64][]int64{}} // the transaction can be retried
		var err error
		// Concurrent writers that reference the user wait for this lock instead of racing the deletion.
		if _, err := q.LockUser(ctx, id); err != nil {
			return err
		}
		if stillDue != nil {
			if err := stillDue(ctx, tx); err != nil {
				return err
			}
		}
		// Deleted here, under the lock, so a login that commits meanwhile is in the list as well.
		if out.Sessions, err = q.DeleteSessionsOfUser(ctx, id); err != nil {
			return err
		}
		// Lock every chat that references the user (member, creator, uploader, invitation party or pinner) in id order before reading roles: a concurrent ownership
		// transfer holds the same lock, so the roles read below cannot change underneath us.
		if _, err := q.LockChatsOfUser(ctx, id); err != nil {
			return err
		}
		// A direct chat cannot outlive one of its two sides.
		var doomed []int64
		doomed, err = q.ListDirectChatIDsOfUser(ctx, id)
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
		all, err := q.ListChatIDsOfUser(ctx, id)
		if err != nil {
			return err
		}
		for _, chatID := range doomed {
			members, err := q.ListParticipantIDs(ctx, chatID)
			if err != nil {
				return err
			}
			out.DeletedChats[chatID] = slices.DeleteFunc(members, func(m int64) bool { return m == id })
			if _, err := q.DeleteChat(ctx, chatID); err != nil {
				return err
			}
		}
		for _, chatID := range all {
			if _, gone := out.DeletedChats[chatID]; !gone {
				out.LeftGroups = append(out.LeftGroups, chatID)
			}
		}
		if err := q.DeleteStickerPacksOfOwner(ctx, &id); err != nil {
			return err
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

// minTrigramQuery is the shortest query whose display-name match can use the trigram index.
const minTrigramQuery = 3

// likeEscape makes user input safe inside a LIKE pattern.
func likeEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

func (r userRepo) Search(ctx context.Context, viewerID int64, query, after string, limit int) ([]User, error) {
	q := likeEscape(strings.ToLower(strings.TrimSpace(query)))
	if q == "" {
		return nil, nil
	}
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	p := sqlcdb.SearchUsersParams{Prefix: q, Contains: q, ViewerID: viewerID, MaxRows: clampLimit(limit)}
	if after != "" {
		a := strings.ToLower(after)
		p.After = &a
	}
	var rows []sqlcdb.User
	var err error
	if utf8.RuneCountInString(query) < minTrigramQuery {
		// pg_trgm finds nothing to index in fewer than three characters: the display name would be a scan.
		rows, err = r.s.q.SearchUsersByUsername(ctx, sqlcdb.SearchUsersByUsernameParams{Prefix: p.Prefix, ViewerID: viewerID, After: p.After, MaxRows: p.MaxRows})
	} else {
		rows, err = r.s.q.SearchUsers(ctx, p)
	}
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]User, len(rows))
	for i, u := range rows {
		out[i] = userFrom(u)
	}
	return out, nil
}
