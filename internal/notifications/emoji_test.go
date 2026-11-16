package notifications

import (
	"context"
	"testing"
	"time"

	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/scylla"
)

type emojiUserFake struct{ postgres.UserRepository }

func (emojiUserFake) Get(context.Context, int64) (postgres.User, error) {
	return postgres.User{DisplayName: "Ann <a:wave:5>"}, nil
}

func TestPushPreviewsShowCustomEmojiAsShortcodes(t *testing.T) {
	w, q, s, j := workerFixture()
	q.p.Previews = true
	uid := int64(1)
	content := "party <:party_cat:42>!"
	w.Users = emojiUserFake{}
	w.Messages = messageFake{m: scylla.Message{ID: 9, ChatID: 3, SenderID: &uid, Type: scylla.TypeText, Content: &content, CreatedAt: time.Now()}}
	if err := w.Handle(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	if len(s.sent) != 1 || s.sent[0].Body != "party :party_cat:!" || s.sent[0].Title != "Ann :wave:" {
		t.Fatalf("payload %+v", s.sent)
	}
}
