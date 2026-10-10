package events

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/myronsi/messenger-back/internal/store/redis"
)

type appended struct {
	stream string
	fields map[string]any
}

type fakeAppender struct{ got []appended }

func (f *fakeAppender) Add(_ context.Context, stream string, fields map[string]any) (string, error) {
	f.got = append(f.got, appended{stream, fields})
	return "1-0", nil
}

func TestRoundTrip(t *testing.T) {
	at := time.UnixMilli(1_760_000_000_123).UTC()
	e := Event{Type: MessageHidden, ChatID: 7, MessageID: 1 << 60, UserID: 3, At: at}
	fields := map[string]string{}
	for k, v := range e.Fields() {
		fields[k] = toString(v)
	}
	if _, ok := fields["actor_id"]; ok {
		t.Fatal("absent id encoded")
	}
	got, err := Parse(fields)
	if err != nil || got != e {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	for _, bad := range []map[string]string{{"type": "nope"}, {"type": MessageCreated, "chat_id": "x"}} {
		if _, err := Parse(bad); !errors.Is(err, ErrMalformed) {
			t.Fatalf("parsed %v", bad)
		}
	}
}

func toString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case int64:
		return strconv.FormatInt(x, 10)
	}
	return ""
}

func TestEmitRoutesByType(t *testing.T) {
	app := &fakeAppender{}
	l := NewLog(app)
	ctx := context.Background()
	for _, e := range []Event{{Type: MessageCreated, ChatID: 1}, {Type: ChatDeleted, ChatID: 1}, {Type: UserDeleted, UserID: 2}} {
		if err := l.Emit(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{redis.StreamMessages, redis.StreamChats, redis.StreamUsers}
	for i, a := range app.got {
		if a.stream != want[i] || a.fields["at"] == nil {
			t.Fatalf("event %d: %+v", i, a)
		}
	}
	if err := l.Emit(ctx, Event{Type: "made.up"}); err == nil {
		t.Fatal("emitted an unknown type")
	}
}
