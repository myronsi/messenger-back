package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gocql/gocql"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/sync/errgroup"

	"github.com/myronsi/messenger-back/internal/store/scylla"
)

// messageBatch is how many v1 messages are read at once.
const messageBatch = 1000

// person is a user reference in v1's JSON columns: user_id, or id in older rows.
type person struct {
	UserID   *int64  `json:"user_id"`
	ID       *int64  `json:"id"`
	Reaction string  `json:"reaction"`
	ReadAt   *string `json:"read_at"`
}

func (p person) uid() int64 {
	if p.UserID != nil {
		return *p.UserID
	}
	if p.ID != nil {
		return *p.ID
	}
	return 0
}

func people(raw *string) []person {
	if raw == nil || *raw == "" {
		return nil
	}
	var out []person
	if json.Unmarshal([]byte(*raw), &out) == nil {
		return out
	}
	return nil
}

func ids(raw *string) []int64 {
	if raw == nil || *raw == "" {
		return nil
	}
	var out []int64
	if json.Unmarshal([]byte(*raw), &out) == nil {
		return out
	}
	return nil
}

// readMark is how far a user read a chat.
type readMark struct {
	id int64
	at time.Time
}

// v1Message is one row of v1's messages table.
type v1Message struct {
	id, chat, sender                   int64
	content                            string
	at, edited                         *time.Time
	reply                              *int64
	reactions, readBy, undelivered, dl *string
	fwdID, fwdSender                   *int64
	fwdName                            *string
}

// messageWorkers is how many messages are written at once.
const messageWorkers = 16

// messages copies the messages in id order, continuing after the last batch an earlier run finished. Messages
// keep their v1 ids (below the first Snowflake id, so they sort before every v2 message) and their creation time,
// which also picks their bucket.
func (m *migrator) messages(ctx context.Context, restart bool) error {
	if err := m.ensureState(ctx); err != nil {
		return err
	}
	mapping, err := m.chatMapping(ctx)
	if err != nil {
		return err
	}
	files, err := m.fileMap(ctx)
	if err != nil {
		return err
	}
	skipped, err := m.skippedChats(ctx)
	if err != nil {
		return err
	}
	after := int64(0)
	if !m.dry && !restart {
		_ = m.pg.Pool().QueryRow(ctx, `SELECT value FROM migrate_v1_state WHERE key = 'messages_after'`).Scan(&after)
	}
	for {
		batch, err := m.messageRows(ctx, after)
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			return nil
		}
		owned, err := m.ownedFiles(ctx, batch)
		if err != nil {
			return err
		}
		var (
			rows []v1Message
			msgs []scylla.Message
		)
		for _, r := range batch {
			v2chat, ok := mapping[r.chat]
			switch {
			case ok:
				rows = append(rows, r)
				msgs = append(msgs, m.toMessage(r, v2chat, owned[r.id], files))
			case skipped[r.chat]:
				// A chat the chats phase left out (a direct chat with a missing user or with oneself).
				m.count("messages_skipped_chat", 1)
			default:
				// The chat is newer than the chats phase: skipping its messages would lose them for good, as the
				// position moves past them.
				return fmt.Errorf("message %d is in chat %d, which the chats phase has not copied: run it again", r.id, r.chat)
			}
		}
		m.count("messages", int64(len(rows)))
		after = batch[len(batch)-1].id
		if !m.dry {
			if err := m.writeMessages(ctx, rows, msgs); err != nil {
				return err
			}
			if err := m.flushBatch(ctx, rows, msgs, after); err != nil {
				return err
			}
		}
		if len(batch) < messageBatch {
			return nil
		}
		m.log.InfoContext(ctx, "messages copied", "up_to", after)
	}
}

// skippedChats reads the v1 chats the chats phase left out on purpose.
func (m *migrator) skippedChats(ctx context.Context) (map[int64]bool, error) {
	if m.dry {
		return m.drySkipped, nil // nil without a chats phase, which maps every chat
	}
	out := map[int64]bool{}
	rows, err := m.pg.Pool().Query(ctx, `SELECT v1_id FROM migrate_v1_skipped_chats`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// messageRows reads the next batch of v1 messages after the given id.
func (m *migrator) messageRows(ctx context.Context, after int64) ([]v1Message, error) {
	rows, err := m.v1.Query(ctx, `SELECT id, chat_id, sender_id, content, timestamp, edited_at, reply_to,
		reactions, read_by, undelivered_to, deleted_for, forwarded_from_message_id, forwarded_from_sender_id, forwarded_from_sender_name
		FROM messages WHERE id > $1 ORDER BY id LIMIT $2`, after, messageBatch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []v1Message
	for rows.Next() {
		var r v1Message
		if err := rows.Scan(&r.id, &r.chat, &r.sender, &r.content, &r.at, &r.edited, &r.reply, &r.reactions, &r.readBy,
			&r.undelivered, &r.dl, &r.fwdID, &r.fwdSender, &r.fwdName); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ownedFiles reads which files the messages of a batch own (message_attachments): only such a file makes a
// message a file message, so a text that merely looks like file JSON cannot point at someone else's file.
func (m *migrator) ownedFiles(ctx context.Context, batch []v1Message) (map[int64]map[string]bool, error) {
	ids := make([]int64, len(batch))
	for i, r := range batch {
		ids[i] = r.id
	}
	rows, err := m.v1.Query(ctx, `SELECT message_id, file_path FROM message_attachments WHERE message_id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]map[string]bool{}
	for rows.Next() {
		var id int64
		var p string
		if err := rows.Scan(&id, &p); err != nil {
			return nil, err
		}
		if out[id] == nil {
			out[id] = map[string]bool{}
		}
		out[id][path.Clean(p)] = true
	}
	return out, rows.Err()
}

// toMessage maps a v1 row. A file message whose file was not copied (missing, or not the message's own) stays
// visible as text rather than as a broken file.
func (m *migrator) toMessage(r v1Message, v2chat int64, owned map[string]bool, files map[string]copiedFile) scylla.Message {
	created := time.Unix(0, 0).UTC()
	if r.at != nil {
		created = r.at.UTC()
	}
	s := r.sender
	msg := scylla.Message{ChatID: v2chat, ID: r.id, SenderID: &s, Type: scylla.TypeText, CreatedAt: created, ReplyTo: r.reply, EditedAt: r.edited}
	text := func() {
		c := r.content
		msg.Content = &c
	}
	switch f, isFile := parseFile(r.content); {
	case r.content == "":
		msg.Deleted = true // legacy rows deleted for everyone
	case !isFile:
		text()
	default:
		rel := staticPath(f.FileURL)
		c, ok := files[rel]
		if !ok || !owned[rel] || !messageFolder(rel) {
			text()
			// A dry run copied no file, so it cannot tell.
			if !m.dry {
				m.count("file_messages_kept_as_text", 1)
			}
			break
		}
		msg.Type = scylla.TypeFile
		if c.kind == "voice" {
			msg.Type = scylla.TypeVoice
		}
		a := gocql.UUID(c.id)
		msg.AttachmentID = &a
		if caption := strings.TrimSpace(f.Caption); caption != "" {
			msg.Content = &caption
		}
	}
	if r.fwdID != nil {
		name := ""
		if r.fwdName != nil {
			name = *r.fwdName
		}
		msg.Forwarded = &scylla.Forwarded{MessageID: *r.fwdID, SenderID: r.fwdSender, SenderName: name}
	}
	return msg
}

// writeMessages writes a batch with several workers; the writes are independent and idempotent.
func (m *migrator) writeMessages(ctx context.Context, batch []v1Message, msgs []scylla.Message) error {
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(messageWorkers)
	for i := range batch {
		g.Go(func() error { return m.message(ctx, msgs[i], batch[i].reactions, batch[i].undelivered, batch[i].dl) })
	}
	return g.Wait()
}

// message writes one message and what hangs on it. Every write is idempotent, so a batch that is repeated after
// a crash changes nothing.
func (m *migrator) message(ctx context.Context, msg scylla.Message, reactions, undelivered, deletedFor *string) error {
	if msg.AttachmentID != nil {
		if _, err := m.pg.Pool().Exec(ctx, `INSERT INTO attachment_links (attachment_id, chat_id, message_id, created_at)
			VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`, uuid.UUID(*msg.AttachmentID), msg.ChatID, msg.ID, msg.CreatedAt); err != nil {
			return err
		}
	}
	deleted := msg.Deleted
	msg.Deleted = false
	if deleted {
		msg.Content = nil
	}
	if err := m.msgs.Insert(ctx, msg); err != nil {
		return err
	}
	if deleted {
		if err := m.msgs.Delete(ctx, msg.ChatID, msg.ID); err != nil {
			return err
		}
	}
	for _, p := range people(reactions) {
		emoji := strings.TrimSpace(p.Reaction)
		if p.uid() == 0 || emoji == "" || utf8.RuneCountInString(emoji) > 32 {
			continue
		}
		// v1 kept no reaction time; the message's own time keeps the order sensible.
		if err := m.msgs.AddReaction(ctx, msg.ChatID, msg.ID, p.uid(), emoji, msg.CreatedAt); err != nil {
			return err
		}
		m.count("reactions", 1)
	}
	for _, uid := range ids(deletedFor) {
		if err := m.msgs.Hide(ctx, uid, msg.ChatID, msg.ID, scylla.HiddenDeletedForMe); err != nil {
			return err
		}
		m.count("hidden_deleted_for_me", 1)
	}
	for _, uid := range ids(undelivered) {
		if err := m.msgs.Hide(ctx, uid, msg.ChatID, msg.ID, scylla.HiddenNotDelivered); err != nil {
			return err
		}
		m.count("hidden_not_delivered", 1)
	}
	return nil
}

// flushBatch moves read markers forward, records each chat's newest message and saves the position.
func (m *migrator) flushBatch(ctx context.Context, batch []v1Message, msgs []scylla.Message, after int64) error {
	reads := map[[2]int64]readMark{} // (v2 chat, user) -> furthest read
	last := map[int64]scylla.Message{}
	for i, r := range batch {
		msg := msgs[i]
		for _, p := range people(r.readBy) {
			uid := p.uid()
			if uid == 0 || uid == r.sender {
				continue
			}
			readAt := msg.CreatedAt
			if p.ReadAt != nil {
				if t, err := time.Parse(time.RFC3339Nano, *p.ReadAt); err == nil {
					readAt = t
				}
			}
			k := [2]int64{msg.ChatID, uid}
			if rm, ok := reads[k]; !ok || r.id > rm.id {
				reads[k] = readMark{id: r.id, at: readAt}
			}
		}
		last[msg.ChatID] = msg
	}
	b := &pgx.Batch{}
	for k, r := range reads {
		b.Queue(`UPDATE participants SET last_read_message_id = $3, last_read_at = $4
			WHERE chat_id = $1 AND user_id = $2 AND (last_read_message_id IS NULL OR last_read_message_id < $3)`, k[0], k[1], r.id, r.at)
	}
	for chatID, msg := range last {
		b.Queue(`UPDATE chats SET last_message_id = $2, last_activity_at = GREATEST(last_activity_at, $3)
			WHERE id = $1 AND (last_message_id IS NULL OR last_message_id < $2)`, chatID, msg.ID, msg.CreatedAt)
	}
	b.Queue(`INSERT INTO migrate_v1_state (key, value) VALUES ('messages_after', $1)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, after)
	return m.pg.Pool().SendBatch(ctx, b).Close()
}
