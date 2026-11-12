package chats

import (
	"encoding/base64"
	"testing"
	"time"

	"github.com/myronsi/messenger-back/internal/store/postgres"
)

func TestCursorKeepsThePinPosition(t *testing.T) {
	at := time.UnixMicro(1_700_000_000_000_000)
	c := postgres.ChatCursor{Unpinned: false, Position: -3, SortAt: at, ChatID: 42}
	got, err := DecodeCursor(EncodeCursor(c))
	if err != nil || got.Unpinned || got.Position != -3 || !got.SortAt.Equal(at) || got.ChatID != 42 {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	old := base64.RawURLEncoding.EncodeToString([]byte("1.1700000000000000.7"))
	got, err = DecodeCursor(old)
	if err != nil || !got.Unpinned || got.Position != 0 || got.ChatID != 7 {
		t.Fatalf("cursor without a position: %+v %v", got, err)
	}
	for _, bad := range []string{"x", base64.RawURLEncoding.EncodeToString([]byte("2.0.1.1")), base64.RawURLEncoding.EncodeToString([]byte("0.a.1.1"))} {
		if _, err := DecodeCursor(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}
