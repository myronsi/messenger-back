package main

import (
	"context"
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var usernameRE = regexp.MustCompile(`^[A-Za-z0-9_]{3,32}$`)

// cleanUsername makes a v1 username fit v2 (3-32 of A-Z, a-z, 0-9, _; unique ignoring case). v1 never checked
// them (B6); a name that does not fit, or collides, gets a fitting one with the user id.
func cleanUsername(name string, id int64, taken map[string]bool) (string, bool) {
	if usernameRE.MatchString(name) && !taken[strings.ToLower(name)] {
		taken[strings.ToLower(name)] = true
		return name, false
	}
	var b strings.Builder
	for _, r := range name {
		if r < 128 && (r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			b.WriteRune(r)
		}
	}
	base := b.String()
	// The id makes it unique in practice; a further counter covers a user whose own name looks like one.
	for n := 0; ; n++ {
		suffix := "_" + strconv.FormatInt(id, 10)
		if n > 0 {
			suffix += "_" + strconv.Itoa(n)
		}
		head := base
		if len(head)+len(suffix) > 32 {
			head = head[:32-len(suffix)]
		}
		out := head + suffix
		for len(out) < 3 {
			out = "u" + out
		}
		if !taken[strings.ToLower(out)] {
			taken[strings.ToLower(out)] = true
			return out, true
		}
	}
}

func cleanText(s string, maxRunes int) string {
	s = strings.TrimSpace(strings.ToValidUTF8(s, ""))
	if utf8.RuneCountInString(s) > maxRunes {
		s = string([]rune(s)[:maxRunes])
	}
	return s
}

var (
	visibilities = map[string]bool{"everyone": true, "shared_chats": true, "everyone_except": true, "nobody_except": true, "nobody": true}
	directModes  = map[string]bool{"everyone": true, "shared_chats": true, "wait_approval": true}
	inviteModes  = map[string]bool{"everyone": true, "shared_chats": true, "everyone_except": true, "nobody_except": true, "nobody": true, "wait_approval": true}
	searchModes  = map[string]bool{"everyone": true, "nobody": true}
	exceptKeys   = map[string]bool{"avatar_visibility": true, "profile_visibility": true, "presence_visibility": true, "group_invites": true}
)

func pick(v string, allowed map[string]bool) string {
	if allowed[v] {
		return v
	}
	return "everyone"
}

// users copies the accounts and everything that belongs to them except avatars (the files phase).
func (m *migrator) users(ctx context.Context) error {
	if err := m.ensureState(ctx); err != nil {
		return err
	}
	rows, err := m.v1.Query(ctx, `SELECT id, username, display_name, password, bio, created_at, last_seen,
		encrypted_cloud_part, salt, verification_ciphertext FROM users ORDER BY id`)
	if err != nil {
		return err
	}
	type user struct {
		id                          int64
		username, display, password string
		bio                         *string
		created, seen               *time.Time
		cloud, verify               *string
		salt                        []byte
	}
	var all []user
	for rows.Next() {
		var u user
		if err := rows.Scan(&u.id, &u.username, &u.display, &u.password, &u.bio, &u.created, &u.seen, &u.cloud, &u.salt, &u.verify); err != nil {
			return err
		}
		all = append(all, u)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	// Names that fit are kept, the lowest id winning a name that differs only in case; then the others are
	// renamed, so a renamed user can never take a name another user keeps.
	taken := map[string]bool{}
	names := make(map[int64]string, len(all))
	for _, u := range all {
		if low := strings.ToLower(u.username); usernameRE.MatchString(u.username) && !taken[low] {
			taken[low] = true
			names[u.id] = u.username
		}
	}
	for _, u := range all {
		if _, kept := names[u.id]; !kept {
			names[u.id], _ = cleanUsername(u.username, u.id, taken)
			m.count("users_renamed", 1)
		}
	}
	batch := newQueue("users")
	for _, u := range all {
		name := names[u.id]
		display := cleanText(u.display, 100)
		if display == "" {
			display = name
		}
		created := time.Now()
		if u.created != nil {
			created = *u.created
		}
		seen := created
		if u.seen != nil {
			seen = *u.seen
		}
		var bio *string
		if u.bio != nil {
			if b := cleanText(*u.bio, 500); b != "" {
				bio = &b
			}
		}
		// The password hash as it is: v2 verifies v1's argon2 and PBKDF2 hashes and upgrades them at login.
		batch.Queue(`INSERT INTO users (id, username, display_name, password_hash, bio, created_at, last_seen_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (id) DO UPDATE SET password_hash = EXCLUDED.password_hash, display_name = EXCLUDED.display_name,
				bio = EXCLUDED.bio, last_seen_at = GREATEST(users.last_seen_at, EXCLUDED.last_seen_at)`, u.id, name, display, u.password, bio, created, seen)
		batch.Queue(`INSERT INTO user_privacy_settings (user_id) VALUES ($1) ON CONFLICT DO NOTHING`, u.id)
		batch.Queue(`INSERT INTO user_security_settings (user_id) VALUES ($1) ON CONFLICT DO NOTHING`, u.id)
		if u.cloud != nil || u.verify != nil {
			batch.Queue(`INSERT INTO user_recovery_shares (user_id, encrypted_cloud_part, salt, verification_ciphertext)
				VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`, u.id, u.cloud, u.salt, u.verify)
		}
	}
	m.count("users", int64(len(all)))
	if err := m.send(ctx, batch); err != nil {
		return err
	}
	if err := m.privacy(ctx); err != nil {
		return err
	}
	if err := m.social(ctx); err != nil {
		return err
	}
	if err := m.security(ctx); err != nil {
		return err
	}
	if err := m.sessions(ctx); err != nil {
		return err
	}
	// New users must not get the id of a v1 user who deleted the account: their messages keep the sender id
	// (v1 had no foreign key there), and a new user with it would be their sender.
	floor, err := m.v1Floor(ctx, `SELECT GREATEST(COALESCE((SELECT MAX(id) FROM users), 0), COALESCE((SELECT MAX(sender_id) FROM messages), 0),
		COALESCE((SELECT last_value FROM pg_sequences WHERE sequencename = 'users_id_seq'), 0))`)
	if err != nil {
		return err
	}
	if err := m.fixSequence(ctx, "users", floor); err != nil {
		return err
	}
	return m.fixSequences(ctx, "user_2fa_recovery_codes")
}

// v1Floor reads a number from the v1 database (the highest id v1 ever handed out).
func (m *migrator) v1Floor(ctx context.Context, q string) (int64, error) {
	var n int64
	if err := m.v1.QueryRow(ctx, q).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

func (m *migrator) privacy(ctx context.Context) error {
	rows, err := m.v1.Query(ctx, `SELECT user_id, avatar_visibility, profile_visibility, presence_visibility, read_receipts_enabled,
		direct_messages, group_invites, search_visibility FROM user_privacy_settings`)
	if err != nil {
		return err
	}
	batch := newQueue("users")
	for rows.Next() {
		var (
			uid                                     int64
			avatar, profile, presence, dm, gi, srch string
			receipts                                bool
		)
		if err := rows.Scan(&uid, &avatar, &profile, &presence, &receipts, &dm, &gi, &srch); err != nil {
			return err
		}
		batch.Queue(`UPDATE user_privacy_settings SET avatar_visibility = $2, profile_visibility = $3, presence_visibility = $4,
			read_receipts_enabled = $5, direct_messages = $6, group_invites = $7, search_visibility = $8 WHERE user_id = $1`,
			uid, pick(avatar, visibilities), pick(profile, visibilities), pick(presence, visibilities), receipts,
			pick(dm, directModes), pick(gi, inviteModes), pick(srch, searchModes))
		m.count("privacy_settings", 1)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows, err = m.v1.Query(ctx, `SELECT owner_id, setting_key, target_user_id, effect, created_at FROM user_privacy_exceptions`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var (
			owner, target int64
			key, effect   string
			at            *time.Time
		)
		if err := rows.Scan(&owner, &key, &target, &effect, &at); err != nil {
			return err
		}
		if !exceptKeys[key] || (effect != "allow" && effect != "deny") || owner == target {
			m.count("privacy_exceptions_skipped", 1)
			continue
		}
		batch.Queue(`INSERT INTO user_privacy_exceptions (owner_id, setting_key, target_user_id, effect, created_at)
			SELECT $1, $2, $3, $4, COALESCE($5, now()) WHERE EXISTS (SELECT 1 FROM users WHERE id = $1)
			AND EXISTS (SELECT 1 FROM users WHERE id = $3) ON CONFLICT DO NOTHING`,
			owner, key, target, effect, at)
		m.count("privacy_exceptions", 1)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return m.send(ctx, batch)
}

func (m *migrator) social(ctx context.Context) error {
	batch := newQueue("users")
	rows, err := m.v1.Query(ctx, `SELECT blocker_id, blocked_id, created_at FROM user_blocks WHERE blocker_id <> blocked_id`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var a, b int64
		var at *time.Time
		if err := rows.Scan(&a, &b, &at); err != nil {
			return err
		}
		batch.Queue(`INSERT INTO user_blocks (blocker_id, blocked_id, created_at) SELECT $1, $2, COALESCE($3, now())
			WHERE EXISTS (SELECT 1 FROM users WHERE id = $1) AND EXISTS (SELECT 1 FROM users WHERE id = $2) ON CONFLICT DO NOTHING`, a, b, at)
		m.count("blocks", 1)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows, err = m.v1.Query(ctx, `SELECT owner_id, target_id, display_name FROM user_contact_names`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var a, b int64
		var name string
		if err := rows.Scan(&a, &b, &name); err != nil {
			return err
		}
		if name = cleanText(name, 100); name == "" || a == b {
			continue
		}
		batch.Queue(`INSERT INTO user_contact_names (owner_id, target_id, display_name) SELECT $1, $2, $3
			WHERE EXISTS (SELECT 1 FROM users WHERE id = $1) AND EXISTS (SELECT 1 FROM users WHERE id = $2) ON CONFLICT DO NOTHING`, a, b, name)
		m.count("contact_names", 1)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return m.send(ctx, batch)
}

// security copies the session lifetimes and 2FA: TOTP secrets are decrypted with the old key and sealed with
// ENCRYPTION_KEY. A secret that cannot be decrypted stops the run (most likely V1_SECRET_KEY is wrong, and
// every user would lose 2FA); with -drop-unreadable-2fa it turns 2FA off for that user instead (reported), since
// it could never be checked again and the account would be locked out.
func (m *migrator) security(ctx context.Context) error {
	rows, err := m.v1.Query(ctx, `SELECT user_id, session_duration_days, two_factor_enabled, two_factor_secret FROM user_security_settings`)
	if err != nil {
		return err
	}
	batch := newQueue("users")
	for rows.Next() {
		var (
			uid     int64
			days    int
			enabled bool
			secret  *string
		)
		if err := rows.Scan(&uid, &days, &enabled, &secret); err != nil {
			return err
		}
		if days <= 0 {
			days = 90
		}
		var sealed *string
		if enabled && secret != nil && *secret != "" {
			plain, err := m.openTOTP(*secret)
			if err != nil && !m.drop2FA {
				rows.Close()
				return fmt.Errorf("the TOTP secret of user %d cannot be decrypted (%w): check V1_SECRET_KEY, or pass -drop-unreadable-2fa to turn 2FA off for such users", uid, err)
			}
			if err != nil {
				m.count("two_factor_disabled_unreadable", 1)
				enabled = false
			} else {
				s, err := m.sealer.Seal(plain, fmt.Sprintf("totp:%d", uid))
				if err != nil {
					return err
				}
				sealed = &s
				m.count("two_factor_migrated", 1)
			}
		} else {
			enabled = false
		}
		batch.Queue(`UPDATE user_security_settings SET session_duration_days = $2, two_factor_enabled = $3, two_factor_secret = $4
			WHERE user_id = $1`, uid, days, enabled, sealed)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows, err = m.v1.Query(ctx, `SELECT user_id, code_hash, created_at, used_at FROM user_2fa_recovery_codes`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var (
			uid      int64
			hash     string
			at, used *time.Time
		)
		if err := rows.Scan(&uid, &hash, &at, &used); err != nil {
			return err
		}
		// The same hash in v1 and v2 (sha256 of the normalized code); no unique key, so check before inserting.
		batch.Queue(`INSERT INTO user_2fa_recovery_codes (user_id, code_hash, created_at, used_at)
			SELECT $1, $2, COALESCE($3, now()), $4 WHERE EXISTS (SELECT 1 FROM users WHERE id = $1)
			AND NOT EXISTS (SELECT 1 FROM user_2fa_recovery_codes WHERE user_id = $1 AND code_hash = $2)`, uid, hash, at, used)
		m.count("two_factor_recovery_codes", 1)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return m.send(ctx, batch)
}

func (m *migrator) openTOTP(token string) (string, error) {
	if m.fernet == nil {
		return "", fmt.Errorf("V1_SECRET_KEY is not set")
	}
	plain, err := fernetDecrypt(m.fernet, token)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// sessions copies the sessions that are still valid, so users stay logged in: v2 looks refresh tokens up by the
// same sha256 hash. The session ids become UUIDs (they were never sent to clients).
func (m *migrator) sessions(ctx context.Context) error {
	rows, err := m.v1.Query(ctx, `SELECT user_id, refresh_token_hash, user_agent, ip_address, created_at, last_active_at, expires_at
		FROM user_sessions WHERE revoked_at IS NULL AND expires_at > now()`)
	if err != nil {
		return err
	}
	batch := newQueue("users")
	for rows.Next() {
		var (
			uid           int64
			hash          string
			agent, ip     *string
			created, last *time.Time
			expires       time.Time
		)
		if err := rows.Scan(&uid, &hash, &agent, &ip, &created, &last, &expires); err != nil {
			return err
		}
		var addr *string
		if ip != nil {
			if a, err := netip.ParseAddr(strings.TrimSpace(*ip)); err == nil {
				s := a.String()
				addr = &s
			}
		}
		batch.Queue(`INSERT INTO user_sessions (user_id, refresh_token_hash, user_agent, ip_address, created_at, last_active_at, expires_at)
			SELECT $1, $2, $3, $4::inet, COALESCE($5, now()), COALESCE($6, now()), $7 WHERE EXISTS (SELECT 1 FROM users WHERE id = $1)
			ON CONFLICT (refresh_token_hash) DO UPDATE SET expires_at = EXCLUDED.expires_at,
				last_active_at = GREATEST(user_sessions.last_active_at, EXCLUDED.last_active_at)`, uid, hash, agent, addr, created, last, expires)
		m.count("sessions", 1)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	// A session copied by an earlier run and revoked in v1 since (a logout, a password change) ends in v2 too.
	rows, err = m.v1.Query(ctx, `SELECT refresh_token_hash FROM user_sessions WHERE revoked_at IS NOT NULL OR expires_at <= now()`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err != nil {
			return err
		}
		batch.Queue(`UPDATE user_sessions SET revoked_at = now() WHERE refresh_token_hash = $1 AND revoked_at IS NULL`, hash)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return m.send(ctx, batch)
}

// fixSequences moves identity sequences past the copied ids, so new rows do not collide with them.
func (m *migrator) fixSequences(ctx context.Context, tables ...string) error {
	for _, t := range tables {
		if err := m.fixSequence(ctx, t, 0); err != nil {
			return err
		}
	}
	return nil
}

// fixSequence moves a table's identity sequence past its highest id and past floor (never back).
func (m *migrator) fixSequence(ctx context.Context, table string, floor int64) error {
	if m.dry {
		return nil
	}
	q := fmt.Sprintf(`SELECT setval(pg_get_serial_sequence('%[1]s', 'id'), GREATEST((SELECT COALESCE(MAX(id), 0) FROM %[1]s), $1::bigint, 1,
		(SELECT last_value FROM pg_sequences WHERE sequencename = pg_get_serial_sequence('%[1]s', 'id')::regclass::text)))`, table)
	if _, err := m.pg.Pool().Exec(ctx, q, floor); err != nil {
		return fmt.Errorf("sequence of %s: %w", table, err)
	}
	return nil
}
