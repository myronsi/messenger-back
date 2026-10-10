package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"math"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/myronsi/messenger-back/internal/media"
)

// v1File is the JSON a v1 file message holds in its content.
type v1File struct {
	FileURL  string `json:"file_url"`
	FileName string `json:"file_name"`
	FileType string `json:"file_type"`
	Width    int    `json:"image_width"`
	Height   int    `json:"image_height"`
	Caption  string `json:"caption"`
}

// parseFile reads a v1 file message: content that is a JSON object with file_url. Whether it really is one is
// decided by message_attachments (see messageFiles); anything else is text, even if it looks like JSON (the B12
// bug of v1 showed such text as a broken file).
func parseFile(content string) (v1File, bool) {
	if !strings.HasPrefix(strings.TrimSpace(content), "{") {
		return v1File{}, false
	}
	var f v1File
	if json.Unmarshal([]byte(content), &f) != nil || f.FileURL == "" {
		return v1File{}, false
	}
	return f, true
}

// staticPath turns a /static/... URL into a path below V1_STATIC_DIR ("" when it points elsewhere or tries to
// leave the folder).
func staticPath(u string) string {
	p, err := url.PathUnescape(strings.SplitN(u, "?", 2)[0])
	if err != nil || !strings.HasPrefix(p, "/static/") {
		return ""
	}
	rel := path.Clean(strings.TrimPrefix(p, "/static/"))
	if rel == "." || strings.HasPrefix(rel, "..") || !fs.ValidPath(rel) {
		return ""
	}
	return rel
}

// messageFolder tells whether a path may hold a message's file: v1 kept those in uploads/ and vm/ only. A
// message cannot make the migration pick up another folder (an avatar, say) by naming it in its text.
func messageFolder(rel string) bool {
	return strings.HasPrefix(rel, "uploads/") || strings.HasPrefix(rel, "vm/")
}

// placeholders are the shared default images of v1; they are no one's avatar.
var placeholders = map[string]bool{"avatars/default.jpg": true, "avatars/deleted.jpg": true, "avatars/group.png": true}

// fileJob is one file to copy.
type fileJob struct {
	rel      string // below V1_STATIC_DIR
	purpose  string // message or avatar
	kind     string // what v1 said: voice for /static/vm, image, ...
	chatID   *int64 // the one chat that uses it (v1 attachments belong to their chat); nil when several do
	uploader *int64
	name     string
	duration *float64
	waveform []int16
	created  time.Time
}

// fileNamespace makes the attachment id of a v1 file deterministic (UUIDv5 of purpose and path), so a run that
// stopped between storing the object and recording it writes the same object and row again instead of a second.
var fileNamespace = uuid.MustParse("6a7e1f62-5d8e-4b1c-9c43-0b7f6a2d9e10")

func fileID(purpose, rel string) uuid.UUID {
	return uuid.NewSHA1(fileNamespace, []byte(purpose+":"+rel))
}

// readFile reads a file below V1_STATIC_DIR through an os.Root: symbolic links that lead out of it are refused.
func (m *migrator) readFile(rel string) ([]byte, error) {
	f, err := m.root.Open(rel)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(f)
}

// exists reports whether a regular file is below V1_STATIC_DIR (links out of it do not count).
func (m *migrator) exists(rel string) bool {
	st, err := m.root.Stat(rel)
	return err == nil && st.Mode().IsRegular()
}

// stored copies a file into object storage and records its attachment, once per file and purpose. Every step is
// idempotent (the same id and key each time), and the attachment and its mapping are written in one transaction.
func (m *migrator) stored(ctx context.Context, j fileJob) (uuid.UUID, string, error) {
	var kind string
	err := m.pg.Pool().QueryRow(ctx, `SELECT kind FROM migrate_v1_files WHERE path = $1 AND purpose = $2`, j.rel, j.purpose).Scan(&kind)
	id := fileID(j.purpose, j.rel)
	if err == nil {
		return id, kind, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, "", err
	}
	data, err := m.readFile(j.rel)
	if err != nil {
		return uuid.Nil, "", err
	}
	hint := ""
	if j.kind == "voice" || strings.HasPrefix(j.rel, "vm/") {
		hint = media.KindVoice
	}
	det, err := media.Detect(data[:min(len(data), 512)], j.purpose, hint)
	if err != nil {
		// v1 accepted it, so keep it as a plain file rather than losing it (avatars must be images).
		if j.purpose == media.PurposeAvatar {
			return uuid.Nil, "", errNotImage
		}
		det = media.Detected{Kind: media.KindFile, ContentType: "application/octet-stream"}
	}
	prefix := "attachments/"
	if j.purpose == media.PurposeAvatar {
		prefix = "avatars/"
	}
	key := prefix + id.String()
	body, contentType := data, det.ContentType
	var width, height *int32
	var thumbKey *string
	if det.Kind == media.KindImage {
		// Re-encoded like every v2 upload: EXIF (locations) and anything malformed go, and a thumbnail is made.
		if img, err := media.ProcessImage(data, det.ContentType); err == nil {
			body, contentType = img.Data, img.ContentType
			w, h := int32(img.Width), int32(img.Height) //nolint:gosec // bounded by media.MaxImageSide
			width, height = &w, &h
			tk := key + ".thumb.jpg"
			if err := m.storage.Put(ctx, tk, bytes.NewReader(img.Thumbnail), int64(len(img.Thumbnail)), "image/jpeg"); err != nil {
				return uuid.Nil, "", err
			}
			thumbKey = &tk
		} else {
			m.count("images_kept_unprocessed", 1)
		}
	}
	if err := m.storage.Put(ctx, key, bytes.NewReader(body), int64(len(body)), contentType); err != nil {
		return uuid.Nil, "", err
	}
	name := cleanText(j.name, 255)
	if name == "" {
		name = path.Base(j.rel)
		if i := strings.IndexByte(name, '_'); i >= 0 && i < len(name)-1 {
			name = name[i+1:] // v1 names are <uuid>_<name>
		}
		name = cleanText(name, 255)
	}
	tx, err := m.pg.Pool().Begin(ctx)
	if err != nil {
		return uuid.Nil, "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `INSERT INTO attachments (id, uploader_id, chat_id, purpose, kind, filename, storage_key, thumbnail_key,
		mime_type, size, width, height, duration, waveform, created_at)
		VALUES ($1, (SELECT id FROM users WHERE id = $2), $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15) ON CONFLICT (id) DO NOTHING`,
		id, j.uploader, j.chatID, j.purpose, det.Kind, name, key, thumbKey, contentType, int64(len(body)), width, height,
		j.duration, j.waveform, j.created); err != nil {
		return uuid.Nil, "", err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO migrate_v1_files (path, purpose, attachment_id, kind) VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`,
		j.rel, j.purpose, id, det.Kind); err != nil {
		return uuid.Nil, "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return uuid.Nil, "", err
	}
	m.count("files_copied", 1)
	return id, det.Kind, nil
}

var errNotImage = errors.New("not an image")

// waveformOf scales v1's bars (floats 0.16-1) to v2's (0-255).
func waveformOf(raw *string) []int16 {
	if raw == nil {
		return nil
	}
	var bars []float64
	if json.Unmarshal([]byte(*raw), &bars) != nil {
		return nil
	}
	out := make([]int16, 0, min(len(bars), 128))
	for _, b := range bars {
		if len(out) == 128 {
			break
		}
		out = append(out, int16(math.Round(math.Max(0, math.Min(1, b))*255)))
	}
	return out
}

// fileUse is one message that uses a file.
type fileUse struct {
	message, chat int64
}

// messageFiles lists the files of v1 messages the way v1 decided access: message_attachments rows (backfilled
// for old messages by v1 itself), in uploads/ and vm/ only. The content JSON gives the display name and kind.
func (m *migrator) messageFiles(ctx context.Context, mapping map[int64]int64) (map[string]*fileJob, map[string][]fileUse, error) {
	rows, err := m.v1.Query(ctx, `SELECT a.file_path, m.id, m.chat_id, m.sender_id, m.content, m.audio_duration, m.audio_waveform, m.timestamp
		FROM message_attachments a JOIN messages m ON m.id = a.message_id ORDER BY m.id`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	jobs := map[string]*fileJob{}
	uses := map[string][]fileUse{}
	for rows.Next() {
		var (
			rel              string
			id, chat, sender int64
			content          string
			dur              *float64
			wave             *string
			at               *time.Time
		)
		if err := rows.Scan(&rel, &id, &chat, &sender, &content, &dur, &wave, &at); err != nil {
			return nil, nil, err
		}
		f, ok := parseFile(content)
		// The row must be the message's own file (not its thumbnail), in a folder for message files.
		rel = path.Clean(rel)
		if !ok || staticPath(f.FileURL) != rel || !messageFolder(rel) {
			continue
		}
		v2, ok := mapping[chat]
		if !ok {
			continue
		}
		uses[rel] = append(uses[rel], fileUse{message: id, chat: v2})
		if _, seen := jobs[rel]; seen {
			continue
		}
		created := time.Now()
		if at != nil {
			created = *at
		}
		s := sender
		jobs[rel] = &fileJob{rel: rel, purpose: media.PurposeMessage, kind: f.FileType, uploader: &s, name: f.FileName,
			duration: dur, waveform: waveformOf(wave), created: created}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	// A file used in one chat belongs to that chat, as in v1. One that forwards spread over several chats
	// belongs to none: the links decide who may see it, and deleting one chat must not delete it for the others.
	for rel, j := range jobs {
		chats := map[int64]bool{}
		for _, u := range uses[rel] {
			chats[u.chat] = true
		}
		if len(chats) == 1 {
			c := uses[rel][0].chat
			j.chatID = &c
		}
	}
	return jobs, uses, nil
}

// files copies the files of file messages and the avatars, and wires the avatars to users, history and groups.
// A message whose file does not exist stays a text message (the messages phase finds no mapping for it); a
// file that exists but cannot be stored stops the run, so it is never mistaken for a missing one.
func (m *migrator) files(ctx context.Context) error {
	if err := m.ensureState(ctx); err != nil {
		return err
	}
	mapping, err := m.chatMapping(ctx)
	if err != nil {
		return err
	}
	jobs, uses, err := m.messageFiles(ctx, mapping)
	if err != nil {
		return err
	}
	for rel, j := range jobs {
		if !m.exists(rel) {
			m.count("message_files_missing", 1)
			continue
		}
		if m.dry {
			m.count("files_to_copy", 1)
			continue
		}
		id, _, err := m.stored(ctx, *j)
		if err != nil {
			return errors.New("copy a message file: " + err.Error())
		}
		// The links right away: an attachment without a chat and without links would look unused to the
		// worker's cleanup.
		b := &pgx.Batch{}
		for _, u := range uses[rel] {
			b.Queue(`INSERT INTO attachment_links (attachment_id, chat_id, message_id, created_at) VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`,
				id, u.chat, u.message, j.created)
		}
		if err := m.pg.Pool().SendBatch(ctx, b).Close(); err != nil {
			return err
		}
	}
	return m.avatars(ctx, mapping)
}

// avatars copies user avatars (the current one and the history) and group avatars.
func (m *migrator) avatars(ctx context.Context, mapping map[int64]int64) error {
	type hist struct {
		id, user int64
		url      string
		at       *time.Time
	}
	var all []hist
	rows, err := m.v1.Query(ctx, `SELECT id, user_id, avatar_url, created_at FROM user_avatar_history ORDER BY id`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var h hist
		if err := rows.Scan(&h.id, &h.user, &h.url, &h.at); err != nil {
			return err
		}
		all = append(all, h)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	current := map[int64]string{}
	rows, err = m.v1.Query(ctx, `SELECT id, avatar_url FROM users WHERE avatar_url IS NOT NULL`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int64
		var u string
		if err := rows.Scan(&id, &u); err != nil {
			return err
		}
		current[id] = u
	}
	if err := rows.Err(); err != nil {
		return err
	}
	planned := map[string]bool{}
	avatar := func(uid int64, u string, at time.Time) (*uuid.UUID, error) {
		rel := staticPath(u)
		if rel == "" || placeholders[rel] || !strings.HasPrefix(rel, "avatars/") || !m.exists(rel) {
			return nil, nil
		}
		if m.dry {
			if !planned[rel] {
				planned[rel] = true
				m.count("avatars_to_copy", 1)
			}
			return nil, nil
		}
		id, _, err := m.stored(ctx, fileJob{rel: rel, purpose: media.PurposeAvatar, uploader: &uid, created: at})
		if errors.Is(err, errNotImage) {
			m.count("avatars_not_images", 1)
			return nil, nil // the user keeps the default avatar
		}
		if err != nil {
			return nil, err
		}
		return &id, nil
	}
	for _, h := range all {
		at := time.Now()
		if h.at != nil {
			at = *h.at
		}
		id, err := avatar(h.user, h.url, at)
		if err != nil {
			return err
		}
		if id == nil {
			continue
		}
		if _, err := m.pg.Pool().Exec(ctx, `INSERT INTO user_avatar_history (id, user_id, attachment_id, is_current, created_at)
			SELECT $1, $2, $3, FALSE, $4 WHERE EXISTS (SELECT 1 FROM users WHERE id = $2) ON CONFLICT DO NOTHING`, h.id, h.user, *id, at); err != nil {
			return err
		}
		m.count("avatar_history", 1)
	}
	// History rows of their own follow: the sequence goes past the copied ids first.
	if err := m.fixSequences(ctx, "user_avatar_history"); err != nil {
		return err
	}
	for uid, u := range current {
		id, err := avatar(uid, u, time.Now())
		if err != nil {
			return err
		}
		if id == nil {
			continue
		}
		// The current avatar: on the user and marked in the history (one current row per user).
		b := &pgx.Batch{}
		b.Queue(`UPDATE users SET avatar_attachment_id = $2 WHERE id = $1`, uid, *id)
		b.Queue(`UPDATE user_avatar_history SET is_current = FALSE WHERE user_id = $1 AND is_current AND attachment_id IS DISTINCT FROM $2`, uid, *id)
		b.Queue(`UPDATE user_avatar_history SET is_current = TRUE WHERE id = (SELECT id FROM user_avatar_history WHERE user_id = $1 AND attachment_id = $2 ORDER BY id DESC LIMIT 1)`, uid, *id)
		b.Queue(`INSERT INTO user_avatar_history (user_id, attachment_id, is_current)
			SELECT $1, $2, TRUE WHERE EXISTS (SELECT 1 FROM users WHERE id = $1) AND NOT EXISTS (SELECT 1 FROM user_avatar_history WHERE user_id = $1 AND is_current)`, uid, *id)
		if err := m.pg.Pool().SendBatch(ctx, b).Close(); err != nil {
			return err
		}
		m.count("avatars", 1)
	}
	rows, err = m.v1.Query(ctx, `SELECT c.id, c.avatar_url, g.admin_id FROM chats c JOIN groups g ON g.chat_id = c.id WHERE c.avatar_url IS NOT NULL`)
	if err != nil {
		return err
	}
	type group struct {
		id, admin int64
		url       string
	}
	var groups []group
	for rows.Next() {
		var g group
		if err := rows.Scan(&g.id, &g.url, &g.admin); err != nil {
			return err
		}
		groups = append(groups, g)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, g := range groups {
		v2, ok := mapping[g.id]
		if !ok {
			continue
		}
		id, err := avatar(g.admin, g.url, time.Now())
		if err != nil {
			return err
		}
		if id == nil {
			continue
		}
		if _, err := m.pg.Pool().Exec(ctx, `UPDATE chats SET avatar_attachment_id = $2 WHERE id = $1`, v2, *id); err != nil {
			return err
		}
		m.count("group_avatars", 1)
	}
	return nil
}

// fileMap loads the message files copied so far: v1 path -> attachment and kind.
func (m *migrator) fileMap(ctx context.Context) (map[string]copiedFile, error) {
	out := map[string]copiedFile{}
	if m.dry {
		return out, nil
	}
	rows, err := m.pg.Pool().Query(ctx, `SELECT path, attachment_id, kind FROM migrate_v1_files WHERE purpose = 'message'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var c copiedFile
		var p string
		if err := rows.Scan(&p, &c.id, &c.kind); err != nil {
			return nil, err
		}
		out[p] = c
	}
	return out, rows.Err()
}

type copiedFile struct {
	id   uuid.UUID
	kind string
}
