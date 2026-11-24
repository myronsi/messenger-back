package scylla

import "testing"

func TestStatementName(t *testing.T) {
	for stmt, want := range map[string]string{
		"SELECT chat_id, id FROM messages WHERE chat_id = ? AND bucket = ?": "select_messages",
		"select * from messenger.chat_buckets where chat_id = ?":            "select_chat_buckets",
		"INSERT INTO messages (chat_id, bucket, id) VALUES (?, ?, ?)":       "insert_messages",
		"insert into reactions(chat_id, message_id) values (?, ?)":          "insert_reactions",
		"UPDATE messages SET content = ? WHERE chat_id = ? IF EXISTS":       "update_messages",
		"DELETE FROM hidden_messages WHERE chat_id = ?":                     "delete_hidden_messages",
		"SELECT toTimestamp(now()) FROM system.local":                       "select_local",
		"TRUNCATE messages": "other",
		"":                  "other",
	} {
		if got := StatementName(stmt); got != want {
			t.Errorf("StatementName(%q) = %q, want %q", stmt, got, want)
		}
	}
}
