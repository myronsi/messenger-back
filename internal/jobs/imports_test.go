package jobs

import (
	"context"
	"errors"
	"testing"

	"github.com/myronsi/messenger-back/internal/events"
)

type rangeDeleter struct {
	got [][3]int64
	err error
}

func (d *rangeDeleter) DeleteRange(_ context.Context, chatID, from, to int64) error {
	d.got = append(d.got, [3]int64{chatID, from, to})
	return d.err
}

type importRows struct {
	forgot  [][3]int64
	deleted []int64
}

func (r *importRows) ForgetMessages(_ context.Context, chatID, from, to int64) error {
	r.forgot = append(r.forgot, [3]int64{chatID, from, to})
	return nil
}

func (r *importRows) Delete(_ context.Context, id int64) error {
	r.deleted = append(r.deleted, id)
	return nil
}

func TestImportCleanupRemovesTheRange(t *testing.T) {
	msgs, rows := &rangeDeleter{}, &importRows{}
	h := ImportCleanup(msgs, rows, nil)
	ctx := context.Background()
	if err := h(ctx, entry(events.Event{Type: events.ImportRemoved, ChatID: 4, ImportID: 9, MessageID: 100, UpTo: 200})); err != nil {
		t.Fatal(err)
	}
	if len(msgs.got) != 1 || msgs.got[0] != [3]int64{4, 100, 200} || len(rows.forgot) != 1 || rows.forgot[0] != [3]int64{4, 100, 200} {
		t.Fatalf("deleted %v, forgot %v", msgs.got, rows.forgot)
	}
	if len(rows.deleted) != 1 || rows.deleted[0] != 9 {
		t.Fatalf("import rows deleted: %v", rows.deleted)
	}
	for _, e := range []events.Event{
		{Type: events.HistoryImported, ChatID: 4, ImportID: 9, MessageID: 100, UpTo: 200},
		{Type: events.ImportRemoved, ChatID: 4, MessageID: 100, UpTo: 200},
		{Type: events.ImportRemoved, ChatID: 4, ImportID: 9, MessageID: 200, UpTo: 100},
		{Type: events.ChatDeleted, ChatID: 4},
	} {
		if err := h(ctx, entry(e)); err != nil {
			t.Fatal(err)
		}
	}
	if len(msgs.got) != 1 {
		t.Fatalf("other events deleted messages: %v", msgs.got)
	}
	msgs.err = errors.New("scylla away")
	if err := h(ctx, entry(events.Event{Type: events.ImportRemoved, ChatID: 4, ImportID: 9, MessageID: 100, UpTo: 200})); err == nil || len(rows.deleted) != 1 {
		t.Fatalf("a failed delete removed the import rows: %v %v", err, rows.deleted)
	}
}
