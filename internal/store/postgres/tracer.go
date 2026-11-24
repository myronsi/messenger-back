package postgres

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type Observer func(op string, d time.Duration, err error)

type queryTracer struct{ observe Observer }

type traceKey struct{}

type traceStart struct {
	op string
	at time.Time
}

var sqlcName = regexp.MustCompile(`^\s*-- name: (\w+)`)

var sqlVerbs = map[string]bool{
	"select": true, "insert": true, "update": true, "delete": true, "with": true,
	"begin": true, "commit": true, "rollback": true, "savepoint": true, "release": true,
}

func QueryName(sql string) string {
	if m := sqlcName.FindStringSubmatch(sql); m != nil {
		return m[1]
	}
	fields := strings.Fields(sql)
	if len(fields) == 0 {
		return "other"
	}
	if verb := strings.ToLower(fields[0]); sqlVerbs[verb] {
		return "sql_" + verb
	}
	return "other"
}

func (t queryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, traceKey{}, traceStart{op: QueryName(d.SQL), at: time.Now()})
}

func (t queryTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryEndData) {
	s, ok := ctx.Value(traceKey{}).(traceStart)
	if !ok {
		return
	}
	err := d.Err
	if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, context.Canceled) {
		err = nil
	}
	t.observe(s.op, time.Since(s.at), err)
}
