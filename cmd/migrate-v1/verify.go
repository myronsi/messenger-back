package main

import (
	"context"

	"github.com/myronsi/messenger-back/internal/store/scylla"
)

// chatSamples is how many chats get their messages counted in both stores.
const chatSamples = 50

// verify fills the report's comparison: rows per table in v1 and v2, and the messages of sampled chats (the
// largest ones and some at random). Direct chats that v1 had twice are counted under the chat they merged into.
func (m *migrator) verify(ctx context.Context) error {
	if m.dry {
		return nil
	}
	pairs := []struct{ name, v1, v2 string }{
		{"users", `SELECT count(*) FROM users`, `SELECT count(*) FROM users`},
		{"blocks", `SELECT count(*) FROM user_blocks WHERE blocker_id <> blocked_id`, `SELECT count(*) FROM user_blocks`},
		{"contact_names", `SELECT count(*) FROM user_contact_names`, `SELECT count(*) FROM user_contact_names`},
		{"groups", `SELECT count(*) FROM chats WHERE type = 'group'`, `SELECT count(*) FROM chats WHERE type = 'group'`},
		{"direct_chats", `SELECT count(DISTINCT LEAST(user1_id, user2_id) || ':' || GREATEST(user1_id, user2_id)) FROM chats WHERE type <> 'group' AND user1_id <> user2_id`,
			`SELECT count(*) FROM chats WHERE type = 'direct'`},
		{"participants_of_groups", `SELECT count(*) FROM participants p JOIN chats c ON c.id = p.chat_id WHERE c.type = 'group'`,
			`SELECT count(*) FROM participants p JOIN chats c ON c.id = p.chat_id WHERE c.type = 'group'`},
		{"pins", `SELECT count(*) FROM user_chat_pins`, `SELECT count(*) FROM user_chat_pins`},
		{"approval_requests", `SELECT count(*) FROM approval_requests`, `SELECT count(*) FROM approval_requests`},
		{"sessions_valid", `SELECT count(*) FROM user_sessions WHERE revoked_at IS NULL AND expires_at > now()`,
			`SELECT count(*) FROM user_sessions WHERE revoked_at IS NULL AND expires_at > now()`},
		{"two_factor_enabled", `SELECT count(*) FROM user_security_settings WHERE two_factor_enabled`,
			`SELECT count(*) FROM user_security_settings WHERE two_factor_enabled`},
	}
	for _, p := range pairs {
		var a, b int64
		if err := m.v1.QueryRow(ctx, p.v1).Scan(&a); err != nil {
			return err
		}
		if err := m.pg.Pool().QueryRow(ctx, p.v2).Scan(&b); err != nil {
			return err
		}
		m.report.Verification[p.name] = [2]int64{a, b}
	}

	mapping, err := m.chatMapping(ctx)
	if err != nil {
		return err
	}
	rows, err := m.v1.Query(ctx, `(SELECT chat_id, count(*) FROM messages GROUP BY chat_id ORDER BY count(*) DESC LIMIT $1)
		UNION (SELECT chat_id, count(*) FROM messages GROUP BY chat_id ORDER BY random() LIMIT $1)`, chatSamples/2)
	if err != nil {
		return err
	}
	v1counts := map[int64]int64{} // v2 chat -> v1 messages (merged chats add up)
	var order []int64
	for rows.Next() {
		var chat, n int64
		if err := rows.Scan(&chat, &n); err != nil {
			return err
		}
		v2, ok := mapping[chat]
		if !ok {
			continue
		}
		if _, seen := v1counts[v2]; !seen {
			order = append(order, v2)
		}
		v1counts[v2] += n
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, v2 := range order {
		n, err := m.countMessages(ctx, v2)
		if err != nil {
			return err
		}
		m.report.Chats = append(m.report.Chats, chatCheck{V2Chat: v2, V1: v1counts[v2], V2: n})
		if n != v1counts[v2] {
			m.log.WarnContext(ctx, "message count differs", "chat_id", v2, "v1", v1counts[v2], "v2", n)
		}
	}
	return nil
}

// countMessages counts every message of a chat in ScyllaDB (deleted ones included, as in v1), paging back.
func (m *migrator) countMessages(ctx context.Context, chatID int64) (int64, error) {
	var n int64
	q := scylla.PageQuery{ChatID: chatID, Limit: 500}
	for {
		p, err := m.msgs.Page(ctx, q)
		if err != nil {
			return 0, err
		}
		n += int64(len(p.Messages))
		if !p.HasOlder || len(p.Messages) == 0 {
			return n, nil
		}
		q.Before = p.Messages[0].ID
	}
}
