package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/myronsi/messenger-back/internal/auth"
	"github.com/myronsi/messenger-back/internal/media"
	"github.com/myronsi/messenger-back/internal/store/scylla"
	"github.com/myronsi/messenger-back/internal/testenv"
)

func TestMain(m *testing.M) {
	code := m.Run()
	testenv.DropKeyspace()
	os.Exit(code)
}

// v1Database creates a database with the schema of the Python backend.
func v1Database(t *testing.T) *pgxpool.Pool {
	t.Helper()
	admin := os.Getenv("TEST_DATABASE_URL")
	if admin == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	name := "v1_" + hex.EncodeToString(b)
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		_ = conn.Close(context.Background())
	})
	u, _ := url.Parse(admin)
	u.Path = "/" + name
	pool, err := pgxpool.New(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	schema, err := os.ReadFile("testdata/v1_schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(schema)); err != nil {
		t.Fatalf("v1 schema: %v", err)
	}
	return pool
}

func pngFile(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 40, 30))
	for i := range img.Pix {
		img.Pix[i] = 200
	}
	img.Set(1, 1, color.Black)
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const totpSecret = "JBSWY3DPEHPK3PXP"

// fixture fills the v1 database with a bit of everything the migration has to handle.
func fixture(t *testing.T, v1 *pgxpool.Pool, static string, fkey []byte) {
	t.Helper()
	ctx := context.Background()
	for dir, data := range map[string][]byte{
		"uploads/5f0c8a64_photo.png": pngFile(t),
		"vm/9a1b_voice.webm":         []byte("\x1a\x45\xdf\xa3 not really a webm"),
		"uploads/77aa_notes.pdf":     []byte("%PDF-1.4 notes"),
		"avatars/id-1/abc.png":       pngFile(t),
		"avatars/groups/20/team.png": pngFile(t),
	} {
		p := filepath.Join(static, filepath.FromSlash(dir))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	secret := fernetEncrypt(fkey, []byte(totpSecret), bytes.Repeat([]byte{7}, 16), 1700000000)
	now := time.Now().UTC()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := v1.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO users (id, username, display_name, password, avatar_url, created_at, last_seen) VALUES
		(1, 'alice', 'Alice', 'argon2$$argon2id$v=19$m=65536,t=3,p=4$c2FsdA$aGFzaA', '/static/avatars/id-1/abc.png', $1, $1),
		(2, 'bob smith', 'Bob', 'c2FsdHNhbHQ=:aGFzaA==', NULL, $1, $1),
		(3, 'ALICE', '   ', 'x', NULL, $1, $1),
		(4, 'carol', 'Carol', 'x', NULL, $1, $1)`, now.Add(-48*time.Hour))
	exec(`INSERT INTO user_avatar_history (id, user_id, avatar_url, is_current, created_at) VALUES (1, 1, '/static/avatars/id-1/abc.png', TRUE, $1)`, now)
	exec(`INSERT INTO user_privacy_settings (user_id, search_visibility, direct_messages) VALUES (4, 'nobody', 'wait_approval'), (2, 'bogus', 'everyone')`)
	exec(`INSERT INTO user_privacy_exceptions (owner_id, setting_key, target_user_id, effect) VALUES (4, 'avatar_visibility', 1, 'deny'), (4, 'direct_messages', 2, 'allow')`)
	exec(`INSERT INTO user_blocks (blocker_id, blocked_id) VALUES (1, 2), (2, 2)`)
	exec(`INSERT INTO user_contact_names (owner_id, target_id, display_name) VALUES (1, 4, 'Caro')`)
	exec(`INSERT INTO user_security_settings (user_id, session_duration_days, two_factor_enabled, two_factor_secret) VALUES (4, 30, TRUE, $1), (2, 0, TRUE, 'garbage')`, secret)
	exec(`INSERT INTO user_2fa_recovery_codes (user_id, code_hash) VALUES (4, 'abc123')`)
	exec(`INSERT INTO user_sessions (id, user_id, refresh_token_hash, ip_address, expires_at, revoked_at) VALUES
		('s1', 4, 'hash-valid', '192.0.2.4', $1, NULL), ('s2', 4, 'hash-revoked', 'not an ip', $1, $2), ('s3', 1, 'hash-old', NULL, $2, NULL)`,
		now.Add(24*time.Hour), now.Add(-time.Hour))
	exec(`INSERT INTO chats (id, name, type, user1_id, user2_id) VALUES (10, 'alice & bob', 'one-on-one', 1, 2), (11, 'bob & alice', 'one-on-one', 2, 1)`)
	exec(`INSERT INTO chats (id, name, type, description, avatar_url) VALUES (20, ' Team ', 'group', 'the team', '/static/avatars/groups/20/team.png')`)
	exec(`INSERT INTO groups (chat_id, admin_id) VALUES (20, 1)`)
	exec(`INSERT INTO participants (chat_id, user_id, role) VALUES (10, 1, 'member'), (10, 2, 'member'), (11, 1, 'member'), (11, 2, 'member'),
		(20, 1, 'member'), (20, 2, 'admin'), (20, 3, 'owner'), (20, 4, 'whatever')`)
	exec(`INSERT INTO user_chat_pins (user_id, chat_id) VALUES (1, 11)`)
	exec(`INSERT INTO approval_requests (id, type, requester_id, recipient_id, status, message_text) VALUES (5, 'direct_message', 2, 4, 'pending', 'hi carol')`)
	exec(`INSERT INTO approval_requests (id, type, requester_id, recipient_id, status, chat_id) VALUES (6, 'group_invite', 1, 4, 'approved', 20)`)
	at := func(m int) time.Time { return now.Add(-time.Duration(100-m) * time.Minute) }
	file := `{"file_url":"/static/uploads/5f0c8a64_photo.png","file_name":"photo.png","file_type":"image","file_size":100}`
	missing := `{"file_url":"/static/uploads/gone_doc.pdf","file_name":"doc.pdf","file_type":"document","file_size":10}`
	voice := `{"file_url":"/static/vm/9a1b_voice.webm","file_name":"voice.webm","file_type":"voice","file_size":20}`
	exec(`INSERT INTO messages (id, chat_id, sender_id, sender_name, content, timestamp, reactions, read_by) VALUES
		(1, 10, 1, 'Alice', 'hi bob', $1, '[{"user_id":2,"username":"bob","reaction":"👍"}]', '[{"user_id":2,"read_at":"2026-10-10T10:00:00Z"}]')`, at(1))
	exec(`INSERT INTO messages (id, chat_id, sender_id, sender_name, content, timestamp, reply_to) VALUES (2, 10, 2, 'Bob', 'hi alice', $1, 1)`, at(2))
	exec(`INSERT INTO messages (id, chat_id, sender_id, sender_name, content, timestamp) VALUES (3, 11, 2, 'Bob', 'from the duplicate', $1)`, at(3))
	exec(`INSERT INTO messages (id, chat_id, sender_id, sender_name, content, timestamp) VALUES (4, 20, 1, 'Alice', $2, $1)`, at(4), file)
	exec(`INSERT INTO messages (id, chat_id, sender_id, sender_name, content, timestamp) VALUES (5, 20, 1, 'Alice', $2, $1)`, at(5), missing)
	exec(`INSERT INTO messages (id, chat_id, sender_id, sender_name, content, timestamp, audio_duration, audio_waveform) VALUES
		(6, 20, 2, 'Bob', $2, $1, 2.5, '[0.16, 0.5, 1.0]')`, at(6), voice)
	exec(`INSERT INTO messages (id, chat_id, sender_id, sender_name, content, timestamp, deleted_for, undelivered_to) VALUES
		(7, 20, 4, 'Carol', 'secret', $1, '[1]', '[2]')`, at(7))
	exec(`INSERT INTO messages (id, chat_id, sender_id, sender_name, content, timestamp, forwarded_from_message_id, forwarded_from_sender_id, forwarded_from_sender_name)
		VALUES (8, 20, 1, 'Alice', 'hi bob', $1, 1, 1, 'Alice')`, at(8))
	exec(`INSERT INTO messages (id, chat_id, sender_id, sender_name, content, timestamp) VALUES (9, 20, 2, 'Bob', '', $1)`, at(9))
	// A file forwarded into another chat: one attachment, used in two chats.
	notes := `{"file_url":"/static/uploads/77aa_notes.pdf","file_name":"notes.pdf","file_type":"document","file_size":14}`
	exec(`INSERT INTO messages (id, chat_id, sender_id, sender_name, content, timestamp) VALUES (10, 20, 1, 'Alice', $2, $1)`, at(10), notes)
	exec(`INSERT INTO messages (id, chat_id, sender_id, sender_name, content, timestamp, forwarded_from_message_id, forwarded_from_sender_id, forwarded_from_sender_name)
		VALUES (11, 10, 1, 'Alice', $2, $1, 10, 1, 'Alice')`, at(11), notes)
	// File JSON pointing at someone's avatar is not a file message, whatever message_attachments says.
	exec(`INSERT INTO messages (id, chat_id, sender_id, sender_name, content, timestamp) VALUES
		(12, 20, 2, 'Bob', '{"file_url":"/static/avatars/id-1/abc.png","file_name":"a.png","file_type":"image"}', $1)`, at(12))
	// A direct chat with oneself, which v2 has no place for: it and its message are left out.
	exec(`INSERT INTO chats (id, name, type, user1_id, user2_id) VALUES (30, 'me', 'one-on-one', 4, 4)`)
	exec(`INSERT INTO messages (id, chat_id, sender_id, sender_name, content, timestamp) VALUES (14, 30, 4, 'Carol', 'note to self', $1)`, at(14))
	// A message of a user who deleted the account since.
	exec(`INSERT INTO messages (id, chat_id, sender_id, sender_name, content, timestamp) VALUES (13, 20, 99, 'Gone', 'bye', $1)`, at(13))
	exec(`INSERT INTO message_attachments (message_id, file_path) VALUES (4, 'uploads/5f0c8a64_photo.png'), (5, 'uploads/gone_doc.pdf'),
		(6, 'vm/9a1b_voice.webm'), (10, 'uploads/77aa_notes.pdf'), (11, 'uploads/77aa_notes.pdf'), (12, 'avatars/id-1/abc.png')`)
}

func TestMigration(t *testing.T) {
	ctx := context.Background()
	v1 := v1Database(t)
	pg := testenv.Postgres(t)
	repo := scylla.NewMessages(testenv.Scylla(t), 20*time.Second)
	static, store := t.TempDir(), t.TempDir()
	disk, err := media.NewDisk(store)
	if err != nil {
		t.Fatal(err)
	}
	fkey, err := fernetKey(strings.Repeat("s", 40))
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := auth.NewSealer(bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	fixture(t, v1, static, fkey)
	root, err := os.OpenRoot(static)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	newMigrator := func(dry bool) *migrator {
		return &migrator{v1: v1, pg: pg, msgs: repo, storage: disk, sealer: sealer, fernet: fkey, root: root, dry: dry,
			drop2FA: true, log: slog.New(slog.DiscardHandler), report: newReport(dry)}
	}

	// An unreadable TOTP secret stops the run (a wrong V1_SECRET_KEY would turn 2FA off for everyone), the dry
	// run included, unless -drop-unreadable-2fa says otherwise.
	strict := newMigrator(true)
	strict.drop2FA = false
	if err := strict.users(ctx); err == nil || !strings.Contains(err.Error(), "user 2") {
		t.Fatalf("strict 2FA: %v", err)
	}

	// A dry run reports and writes nothing.
	dry := newMigrator(true)
	for _, phase := range []func(context.Context) error{dry.users, dry.chats, dry.files} {
		if err := phase(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err := dry.messages(ctx, false); err != nil {
		t.Fatal(err)
	}
	var users int
	_ = pg.Pool().QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&users)
	if users != 0 || dry.report.Counts["messages"] != 13 || dry.report.Counts["messages_skipped_chat"] != 1 || dry.report.Counts["users_renamed"] != 2 || dry.report.Counts["message_files_missing"] != 1 ||
		dry.report.Counts["files_to_copy"] != 3 || dry.report.Counts["file_messages_kept_as_text"] != 0 {
		t.Fatalf("dry run: %d users written, counts %v", users, dry.report.Counts)
	}

	run := func() *migrator {
		m := newMigrator(false)
		for _, phase := range []func(context.Context) error{m.users, m.chats, m.files} {
			if err := phase(ctx); err != nil {
				t.Fatal(err)
			}
		}
		if err := m.messages(ctx, false); err != nil {
			t.Fatal(err)
		}
		if err := m.verify(ctx); err != nil {
			t.Fatal(err)
		}
		return m
	}
	m := run()

	// Users: names that do not fit v2 are renamed, everything else as it was.
	var names []string
	rows, _ := pg.Pool().Query(ctx, `SELECT username FROM users ORDER BY id`)
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		names = append(names, n)
	}
	if fmt.Sprint(names) != "[alice bobsmith_2 ALICE_3 carol]" {
		t.Fatalf("usernames: %v", names)
	}
	var display string
	_ = pg.Pool().QueryRow(ctx, `SELECT display_name FROM users WHERE id = 3`).Scan(&display)
	if display != "ALICE_3" {
		t.Fatalf("an empty display name falls back to the username: %q", display)
	}
	var sealed *string
	var enabled bool
	var days int
	_ = pg.Pool().QueryRow(ctx, `SELECT two_factor_enabled, two_factor_secret, session_duration_days FROM user_security_settings WHERE user_id = 4`).Scan(&enabled, &sealed, &days)
	if !enabled || sealed == nil || days != 30 {
		t.Fatalf("carol's 2FA: %v %v %d", enabled, sealed, days)
	}
	if plain, err := sealer.Open(*sealed, "totp:4"); err != nil || plain != totpSecret {
		t.Fatalf("re-sealed TOTP secret: %q %v", plain, err)
	}
	_ = pg.Pool().QueryRow(ctx, `SELECT two_factor_enabled FROM user_security_settings WHERE user_id = 2`).Scan(&enabled)
	if enabled || m.report.Counts["two_factor_disabled_unreadable"] != 1 {
		t.Fatalf("an unreadable secret must turn 2FA off: %v %v", enabled, m.report.Counts)
	}
	var sessions int
	_ = pg.Pool().QueryRow(ctx, `SELECT count(*) FROM user_sessions WHERE refresh_token_hash = 'hash-valid' AND ip_address = '192.0.2.4'`).Scan(&sessions)
	if sessions != 1 {
		t.Fatalf("valid session: %d", sessions)
	}
	var exceptions, blocks int
	_ = pg.Pool().QueryRow(ctx, `SELECT count(*) FROM user_privacy_exceptions`).Scan(&exceptions)
	_ = pg.Pool().QueryRow(ctx, `SELECT count(*) FROM user_blocks`).Scan(&blocks)
	if exceptions != 1 || blocks != 1 {
		t.Fatalf("exceptions %d (direct_messages has none in v2), blocks %d (no self-blocks)", exceptions, blocks)
	}

	// Chats: the duplicate direct chat merged into the first; the group's owner from groups.admin_id.
	var directs int
	_ = pg.Pool().QueryRow(ctx, `SELECT count(*) FROM chats WHERE type = 'direct'`).Scan(&directs)
	var merged int64
	_ = pg.Pool().QueryRow(ctx, `SELECT v2_id FROM migrate_v1_chats WHERE v1_id = 11`).Scan(&merged)
	if directs != 1 || merged != 10 {
		t.Fatalf("direct chats %d, 11 -> %d", directs, merged)
	}
	roleOf := map[int64]string{}
	rows, _ = pg.Pool().Query(ctx, `SELECT user_id, role FROM participants WHERE chat_id = 20`)
	for rows.Next() {
		var uid int64
		var role string
		_ = rows.Scan(&uid, &role)
		roleOf[uid] = role
	}
	if roleOf[1] != "owner" || roleOf[2] != "admin" || roleOf[3] != "admin" || roleOf[4] != "member" {
		t.Fatalf("roles: %v", roleOf)
	}
	var pinned int
	_ = pg.Pool().QueryRow(ctx, `SELECT count(*) FROM user_chat_pins WHERE user_id = 1 AND chat_id = 10`).Scan(&pinned)
	if pinned != 1 {
		t.Fatal("the pin of the merged chat moves along")
	}

	// Messages.
	page, err := repo.Page(ctx, scylla.PageQuery{ChatID: 10, Limit: 50})
	if err != nil || len(page.Messages) != 4 {
		t.Fatalf("chat 10: %d messages, %v", len(page.Messages), err)
	}
	if page.Messages[1].ReplyTo == nil || *page.Messages[1].ReplyTo != 1 || *page.Messages[2].Content != "from the duplicate" {
		t.Fatalf("chat 10: %+v", page.Messages)
	}
	if r, _ := repo.Reactions(ctx, 10, []int64{1}); len(r[1]) != 1 || r[1][0].Emoji != "👍" {
		t.Fatalf("reactions: %v", r)
	}
	var lastRead *int64
	_ = pg.Pool().QueryRow(ctx, `SELECT last_read_message_id FROM participants WHERE chat_id = 10 AND user_id = 2`).Scan(&lastRead)
	if lastRead == nil || *lastRead != 1 {
		t.Fatalf("bob's read marker: %v", lastRead)
	}
	byID := map[int64]scylla.Message{}
	g, _ := repo.Page(ctx, scylla.PageQuery{ChatID: 20, Limit: 50})
	for _, msg := range g.Messages {
		byID[msg.ID] = msg
	}
	if msg := byID[4]; msg.Type != scylla.TypeFile || msg.AttachmentID == nil {
		t.Fatalf("photo message: %+v", msg)
	}
	att, err := pg.Attachments().Get(ctx, uuid.UUID(*byID[4].AttachmentID))
	if err != nil || att.Kind != "image" || att.ChatID == nil || *att.ChatID != 20 || att.ThumbnailKey == nil || att.Width == nil || *att.Width != 40 {
		t.Fatalf("photo attachment: %+v %v", att, err)
	}
	if msg := byID[5]; msg.Type != scylla.TypeText || msg.Content == nil || !strings.Contains(*msg.Content, "gone_doc.pdf") {
		t.Fatalf("a message whose file is gone stays text: %+v", msg)
	}
	if msg := byID[6]; msg.AttachmentID == nil {
		t.Fatalf("voice message: %+v", msg)
	}
	if hidden, _ := repo.Hidden(ctx, 1, 20, 7); !hidden {
		t.Fatal("deleted_for became hidden")
	}
	if hidden, _ := repo.Hidden(ctx, 2, 20, 7); !hidden {
		t.Fatal("undelivered_to became hidden")
	}
	if msg := byID[8]; msg.Forwarded == nil || msg.Forwarded.MessageID != 1 {
		t.Fatalf("forward: %+v", msg)
	}
	if msg := byID[9]; !msg.Deleted {
		t.Fatalf("an empty legacy message is a deleted one: %+v", msg)
	}
	if msg := byID[12]; msg.Type != scylla.TypeText || m.report.Counts["file_messages_kept_as_text"] != 2 {
		t.Fatalf("file JSON pointing at an avatar stays text: %+v %v", msg, m.report.Counts)
	}
	// The forwarded file: the same attachment in both chats, owned by neither, linked to both messages.
	fwd := page.Messages[3]
	if byID[10].AttachmentID == nil || fwd.ID != 11 || fwd.AttachmentID == nil || *fwd.AttachmentID != *byID[10].AttachmentID {
		t.Fatalf("forwarded file: %+v %+v", byID[10], fwd)
	}
	shared, err := pg.Attachments().Get(ctx, uuid.UUID(*fwd.AttachmentID))
	var links int
	_ = pg.Pool().QueryRow(ctx, `SELECT count(*) FROM attachment_links WHERE attachment_id = $1`, shared.ID).Scan(&links)
	if err != nil || shared.ChatID != nil || links != 2 {
		t.Fatalf("shared attachment: %+v %v, %d links", shared, err, links)
	}
	var lastMessage *int64
	_ = pg.Pool().QueryRow(ctx, `SELECT last_message_id FROM chats WHERE id = 20`).Scan(&lastMessage)
	if lastMessage == nil || *lastMessage != 13 {
		t.Fatalf("chat activity: %v", lastMessage)
	}

	// New users and chats get ids v1 never handed out: user 99 deleted the account but kept their messages.
	var nextUser, nextChat int64
	_ = pg.Pool().QueryRow(ctx, `SELECT nextval(pg_get_serial_sequence('users', 'id')), nextval(pg_get_serial_sequence('chats', 'id'))`).Scan(&nextUser, &nextChat)
	if nextUser <= 99 || nextChat <= 20 {
		t.Fatalf("next ids: user %d, chat %d", nextUser, nextChat)
	}

	// Avatars: alice's is current in her history; the group's is the chat's.
	var avatar *uuid.UUID
	var current bool
	_ = pg.Pool().QueryRow(ctx, `SELECT u.avatar_attachment_id, h.is_current FROM users u JOIN user_avatar_history h ON h.attachment_id = u.avatar_attachment_id
		WHERE u.id = 1`).Scan(&avatar, &current)
	if avatar == nil || !current {
		t.Fatalf("alice's avatar: %v current %v", avatar, current)
	}
	var groupAvatar *uuid.UUID
	_ = pg.Pool().QueryRow(ctx, `SELECT avatar_attachment_id FROM chats WHERE id = 20`).Scan(&groupAvatar)
	if groupAvatar == nil {
		t.Fatal("the group's avatar")
	}
	var approvals, contacts int
	_ = pg.Pool().QueryRow(ctx, `SELECT count(*) FROM approval_requests`).Scan(&approvals)
	_ = pg.Pool().QueryRow(ctx, `SELECT count(*) FROM user_contact_names WHERE owner_id = 1 AND target_id = 4`).Scan(&contacts)
	if approvals != 2 || contacts != 1 {
		t.Fatalf("approvals %d, contact names %d", approvals, contacts)
	}

	// The report: every sampled chat has as many messages in v2 as in v1 (11 counts into 10).
	for _, c := range m.report.Chats {
		if c.V1 != c.V2 {
			t.Errorf("chat %d: v1 %d, v2 %d", c.V2Chat, c.V1, c.V2)
		}
	}

	// A second run copies no file twice and picks up what changed in v1 meanwhile: a new display name, a
	// session ended by a logout.
	if _, err := v1.Exec(ctx, `UPDATE users SET display_name = 'Bobby' WHERE id = 2`); err != nil {
		t.Fatal(err)
	}
	if _, err := v1.Exec(ctx, `UPDATE user_sessions SET revoked_at = now() WHERE refresh_token_hash = 'hash-valid'`); err != nil {
		t.Fatal(err)
	}
	again := run()
	var files int
	_ = pg.Pool().QueryRow(ctx, `SELECT count(*) FROM attachments`).Scan(&files)
	if again.report.Counts["files_copied"] != 0 || files != 5 {
		t.Fatalf("rerun: copied %d, %d attachments", again.report.Counts["files_copied"], files)
	}
	_ = pg.Pool().QueryRow(ctx, `SELECT display_name FROM users WHERE id = 2`).Scan(&display)
	var revoked bool
	_ = pg.Pool().QueryRow(ctx, `SELECT revoked_at IS NOT NULL FROM user_sessions WHERE refresh_token_hash = 'hash-valid'`).Scan(&revoked)
	if display != "Bobby" || !revoked {
		t.Fatalf("rerun: display name %q, session revoked %v", display, revoked)
	}

	// -restart-messages copies every message again and changes nothing.
	restart := newMigrator(false)
	if err := restart.messages(ctx, true); err != nil {
		t.Fatal(err)
	}
	after, err := repo.Page(ctx, scylla.PageQuery{ChatID: 10, Limit: 50})
	if err != nil || len(after.Messages) != 4 || restart.report.Counts["messages"] != 13 || restart.report.Counts["messages_skipped_chat"] != 1 {
		t.Fatalf("restart: %d messages, %v %v", len(after.Messages), err, restart.report.Counts)
	}
	if r, _ := repo.Reactions(ctx, 10, []int64{1}); len(r[1]) != 1 {
		t.Fatalf("restart doubled reactions: %v", r)
	}
	_ = pg.Pool().QueryRow(ctx, `SELECT count(*) FROM attachments`).Scan(&files)
	if files != 5 {
		t.Fatalf("restart: %d attachments", files)
	}
}

func TestUsernames(t *testing.T) {
	taken := map[string]bool{}
	for _, c := range []struct {
		in, out string
		renamed bool
	}{
		{"alice", "alice", false}, {"Alice", "Alice_2", true}, {"a", "a_3", true}, {"ü", "u_4", true},
		{"x" + strings.Repeat("y", 40), "x" + strings.Repeat("y", 29) + "_5", true},
		// Someone already has the name the id would give.
		{"a_7", "a_7", false}, {"a!", "a_7_1", true},
	} {
		id := int64(len(taken) + 1)
		if strings.HasPrefix(c.in, "a_") || c.in == "a!" {
			id = 7
		}
		got, renamed := cleanUsername(c.in, id, taken)
		if got != c.out || renamed != c.renamed || !usernameRE.MatchString(got) {
			t.Errorf("%q -> %q %v, want %q", c.in, got, renamed, c.out)
		}
	}
}
