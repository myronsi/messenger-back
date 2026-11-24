package postgres

import "testing"

func TestQueryName(t *testing.T) {
	for sql, want := range map[string]string{
		"-- name: GetUser :one\nSELECT id FROM users WHERE id = $1": "GetUser",
		"  -- name: TouchActivity :exec\nUPDATE chats SET x = 1":    "TouchActivity",
		"SELECT 1":                 "sql_select",
		"insert into t values (1)": "sql_insert",
		"begin":                    "sql_begin",
		"VACUUM users":             "other",
		"":                         "other",
		"   ":                      "other",
	} {
		if got := QueryName(sql); got != want {
			t.Errorf("QueryName(%q) = %q, want %q", sql, got, want)
		}
	}
}
