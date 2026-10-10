package main

import (
	"context"
	"fmt"
	"time"
)

var roles = map[string]bool{"owner": true, "admin": true, "moderator": true, "member": true}

// chats copies chats and their members. A v1 pair of users can have several direct chats; v2 allows one, so the
// lowest id wins and the others are merged into it (migrate_v1_chats maps every v1 chat to its v2 chat).
func (m *migrator) chats(ctx context.Context) error {
	if err := m.ensureState(ctx); err != nil {
		return err
	}
	// The first and last message of each chat give the creation time (v1 chats have none) and the order.
	times := map[int64][2]time.Time{}
	rows, err := m.v1.Query(ctx, `SELECT chat_id, MIN(timestamp), MAX(timestamp) FROM messages GROUP BY chat_id`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int64
		var first, last *time.Time
		if err := rows.Scan(&id, &first, &last); err != nil {
			return err
		}
		if first != nil && last != nil {
			times[id] = [2]time.Time{*first, *last}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	users := map[int64]bool{}
	rows, err = m.v1.Query(ctx, `SELECT id FROM users`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		users[id] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}

	type chat struct {
		id               int64
		name, kind       string
		description, avt *string
		u1, u2, admin    *int64
	}
	rows, err = m.v1.Query(ctx, `SELECT c.id, c.name, c.type, c.description, c.avatar_url, c.user1_id, c.user2_id, g.admin_id
		FROM chats c LEFT JOIN groups g ON g.chat_id = c.id ORDER BY c.id`)
	if err != nil {
		return err
	}
	var all []chat
	for rows.Next() {
		var c chat
		if err := rows.Scan(&c.id, &c.name, &c.kind, &c.description, &c.avt, &c.u1, &c.u2, &c.admin); err != nil {
			return err
		}
		all = append(all, c)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	members := map[int64][][2]any{} // v1 chat -> (user, role)
	rows, err = m.v1.Query(ctx, `SELECT chat_id, user_id, role FROM participants`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var cid, uid int64
		var role string
		if err := rows.Scan(&cid, &uid, &role); err != nil {
			return err
		}
		members[cid] = append(members[cid], [2]any{uid, role})
	}
	if err := rows.Err(); err != nil {
		return err
	}

	pairs := map[string]int64{} // direct key -> the v1 chat that won
	mapping := map[int64]int64{}
	batch := newQueue("chats")
	for _, c := range all {
		created := time.Now()
		activity := created
		if t, ok := times[c.id]; ok {
			created, activity = t[0], t[1]
		}
		switch c.kind {
		case "group":
			name := cleanText(c.name, 100)
			if name == "" {
				name = "Group"
			}
			desc := ""
			if c.description != nil {
				desc = cleanText(*c.description, 500)
			}
			owner := c.admin
			if owner != nil && !users[*owner] {
				owner = nil
			}
			batch.Queue(`INSERT INTO chats (id, type, name, description, created_by, created_at, last_activity_at)
				VALUES ($1, 'group', $2, $3, $4, $5, $6) ON CONFLICT (id) DO NOTHING`, c.id, name, desc, owner, created, activity)
			ownerSet := false
			for _, p := range members[c.id] {
				uid, role := p[0].(int64), p[1].(string)
				if !users[uid] {
					continue
				}
				if !roles[role] {
					role = "member"
				}
				// groups.admin_id is the owner; a stray "owner" role elsewhere becomes admin (one owner per group).
				switch {
				case owner != nil && uid == *owner:
					role, ownerSet = "owner", true
				case role == "owner":
					role = "admin"
				}
				batch.Queue(`INSERT INTO participants (chat_id, user_id, role, joined_at) VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`,
					c.id, uid, role, created)
			}
			if owner != nil && !ownerSet {
				batch.Queue(`INSERT INTO participants (chat_id, user_id, role, joined_at) VALUES ($1, $2, 'owner', $3) ON CONFLICT DO NOTHING`,
					c.id, *owner, created)
			}
			mapping[c.id] = c.id
			m.count("groups", 1)
		default: // one-on-one
			if c.u1 == nil || c.u2 == nil || *c.u1 == *c.u2 || !users[*c.u1] || !users[*c.u2] {
				m.count("direct_chats_skipped", 1)
				continue
			}
			key := fmt.Sprintf("%d:%d", min(*c.u1, *c.u2), max(*c.u1, *c.u2))
			if winner, dup := pairs[key]; dup {
				mapping[c.id] = winner
				m.count("direct_chats_merged", 1)
				continue
			}
			pairs[key] = c.id
			mapping[c.id] = c.id
			batch.Queue(`INSERT INTO chats (id, type, direct_key, created_by, created_at, last_activity_at)
				VALUES ($1, 'direct', $2, $3, $4, $5) ON CONFLICT (id) DO NOTHING`, c.id, key, *c.u1, created, activity)
			for _, uid := range []int64{*c.u1, *c.u2} {
				batch.Queue(`INSERT INTO participants (chat_id, user_id, role, joined_at) VALUES ($1, $2, 'member', $3) ON CONFLICT DO NOTHING`,
					c.id, uid, created)
			}
			m.count("direct_chats", 1)
		}
	}
	for v1id, v2id := range mapping {
		batch.Queue(`INSERT INTO migrate_v1_chats (v1_id, v2_id) VALUES ($1, $2) ON CONFLICT (v1_id) DO UPDATE SET v2_id = EXCLUDED.v2_id`, v1id, v2id)
	}
	if err := m.send(ctx, batch); err != nil {
		return err
	}
	if err := m.pinsAndRequests(ctx, mapping); err != nil {
		return err
	}
	floor, err := m.v1Floor(ctx, `SELECT GREATEST(COALESCE((SELECT MAX(id) FROM chats), 0),
		COALESCE((SELECT last_value FROM pg_sequences WHERE sequencename = 'chats_id_seq'), 0))`)
	if err != nil {
		return err
	}
	if err := m.fixSequence(ctx, "chats", floor); err != nil {
		return err
	}
	return m.fixSequences(ctx, "approval_requests")
}

func (m *migrator) pinsAndRequests(ctx context.Context, mapping map[int64]int64) error {
	batch := newQueue("chats")
	rows, err := m.v1.Query(ctx, `SELECT user_id, chat_id, pinned_at FROM user_chat_pins`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var uid, cid int64
		var at *time.Time
		if err := rows.Scan(&uid, &cid, &at); err != nil {
			return err
		}
		v2, ok := mapping[cid]
		if !ok {
			continue
		}
		batch.Queue(`INSERT INTO user_chat_pins (user_id, chat_id, pinned_at) SELECT $1, $2, COALESCE($3, now())
			WHERE EXISTS (SELECT 1 FROM participants WHERE chat_id = $2 AND user_id = $1) ON CONFLICT DO NOTHING`, uid, v2, at)
		m.count("pins", 1)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows, err = m.v1.Query(ctx, `SELECT id, type, requester_id, recipient_id, status, message_text, chat_id, created_at, responded_at
		FROM approval_requests ORDER BY id`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var (
			id, from, to  int64
			kind, status  string
			text          *string
			chat          *int64
			created, resp *time.Time
		)
		if err := rows.Scan(&id, &kind, &from, &to, &status, &text, &chat, &created, &resp); err != nil {
			return err
		}
		var v2chat *int64
		if chat != nil {
			if c, ok := mapping[*chat]; ok {
				v2chat = &c
			}
		}
		// The shape v2 requires: invitations have a group, direct-message requests none.
		if from == to || (kind == "group_invite") != (v2chat != nil) || (kind != "group_invite" && kind != "direct_message") {
			m.count("approval_requests_skipped", 1)
			continue
		}
		if status != "pending" && status != "approved" && status != "rejected" {
			status = "rejected"
		}
		// ON CONFLICT DO NOTHING also skips a second pending request of the same pair (v2 allows one).
		batch.Queue(`INSERT INTO approval_requests (id, type, requester_id, recipient_id, status, message_text, chat_id, created_at, responded_at)
			SELECT $1, $2, $3, $4, $5, $6, $7, COALESCE($8, now()), $9
			WHERE EXISTS (SELECT 1 FROM users WHERE id = $3) AND EXISTS (SELECT 1 FROM users WHERE id = $4)
			ON CONFLICT DO NOTHING`, id, kind, from, to, status, text, v2chat, created, resp)
		m.count("approval_requests", 1)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return m.send(ctx, batch)
}

// chatMapping reads the v1 → v2 chat mapping of an earlier chats phase.
func (m *migrator) chatMapping(ctx context.Context) (map[int64]int64, error) {
	out := map[int64]int64{}
	if m.dry {
		// A dry run wrote no mapping: every chat maps to itself, which is what a real run does except merges.
		rows, err := m.v1.Query(ctx, `SELECT id FROM chats`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				return nil, err
			}
			out[id] = id
		}
		return out, rows.Err()
	}
	rows, err := m.pg.Pool().Query(ctx, `SELECT v1_id, v2_id FROM migrate_v1_chats`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var a, b int64
		if err := rows.Scan(&a, &b); err != nil {
			return nil, err
		}
		out[a] = b
	}
	return out, rows.Err()
}
