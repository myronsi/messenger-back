package main

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// queue collects the writes of a phase. They go out in chunks; a chunk that fails is written again row by row,
// so one row v2 refuses is skipped and reported (by the id it starts with) instead of failing the whole phase.
type queue struct {
	what  string
	items []queued
}

type queued struct {
	sql  string
	args []any
}

func newQueue(what string) *queue { return &queue{what: what} }

// Queue adds a write; the first argument identifies the row in reports (an id, never a name).
func (q *queue) Queue(sql string, args ...any) { q.items = append(q.items, queued{sql, args}) }

func (q *queue) Len() int { return len(q.items) }

const chunkSize = 200

// send writes the queue (nothing in a dry run).
func (m *migrator) send(ctx context.Context, q *queue) error {
	if m.dry || q.Len() == 0 {
		return nil
	}
	for start := 0; start < len(q.items); start += chunkSize {
		chunk := q.items[start:min(start+chunkSize, len(q.items))]
		b := &pgx.Batch{}
		for _, it := range chunk {
			b.Queue(it.sql, it.args...)
		}
		if err := m.pg.Pool().SendBatch(ctx, b).Close(); err == nil {
			continue
		} else if ctx.Err() != nil {
			return err
		}
		for _, it := range chunk {
			if _, err := m.pg.Pool().Exec(ctx, it.sql, it.args...); err != nil {
				if ctx.Err() != nil {
					return err
				}
				m.count(q.what+"_rows_refused", 1)
				ref := ""
				if len(it.args) > 0 {
					ref = fmt.Sprint(it.args[0])
				}
				m.log.WarnContext(ctx, "row refused", "phase", q.what, "ref", ref, "error", err)
			}
		}
	}
	return nil
}
