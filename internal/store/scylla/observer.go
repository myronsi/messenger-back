package scylla

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/gocql/gocql"
)

type Observer func(op string, d time.Duration, err error)

type observer struct{ observe Observer }

func (o observer) ObserveQuery(_ context.Context, q gocql.ObservedQuery) {
	err := q.Err
	if errors.Is(err, gocql.ErrNotFound) || errors.Is(err, context.Canceled) {
		err = nil
	}
	o.observe(StatementName(q.Statement), q.End.Sub(q.Start), err)
}

func (o observer) ObserveBatch(_ context.Context, b gocql.ObservedBatch) {
	o.observe("batch", b.End.Sub(b.Start), b.Err)
}

func StatementName(stmt string) string {
	words := strings.Fields(strings.ToLower(stmt))
	if len(words) == 0 {
		return "other"
	}
	verb, table := words[0], ""
	switch verb {
	case "select", "delete":
		for i, w := range words {
			if w == "from" && i+1 < len(words) {
				table = words[i+1]
				break
			}
		}
	case "insert":
		if len(words) > 2 && words[1] == "into" {
			table = words[2]
		}
	case "update":
		if len(words) > 1 {
			table = words[1]
		}
	default:
		return "other"
	}
	if table = tableName(table); table == "" {
		return verb
	}
	return verb + "_" + table
}

func tableName(raw string) string {
	if i := strings.IndexByte(raw, '('); i >= 0 {
		raw = raw[:i]
	}
	if i := strings.LastIndexByte(raw, '.'); i >= 0 {
		raw = raw[i+1:]
	}
	var b strings.Builder
	for _, r := range raw {
		if r == '_' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}
