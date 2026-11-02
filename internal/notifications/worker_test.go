package notifications

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/scylla"
)

type queueFake struct {
	postgres.NotificationRepository
	p         postgres.NotificationPreferences
	devices   []postgres.NotificationDevice
	done      bool
	delivered map[uuid.UUID]bool
	removed   bool
}

func (q *queueFake) Preferences(context.Context, int64) (postgres.NotificationPreferences, error) {
	return q.p, nil
}
func (q *queueFake) Devices(context.Context, int64, int64, int64) ([]postgres.NotificationDevice, error) {
	out := []postgres.NotificationDevice{}
	for _, d := range q.devices {
		if !q.delivered[d.ID] {
			out = append(out, d)
		}
	}
	return out, nil
}
func (q *queueFake) Finish(context.Context, int64) error { q.done = true; return nil }
func (q *queueFake) Delivered(_ context.Context, _ int64, id uuid.UUID) error {
	q.delivered[id] = true
	return nil
}
func (q *queueFake) RemoveDevice(context.Context, uuid.UUID) error { q.removed = true; return nil }

type chatFake struct {
	postgres.ChatRepository
	p   postgres.Participant
	err error
}

func (c chatFake) Participant(context.Context, int64, int64) (postgres.Participant, error) {
	return c.p, c.err
}

type socialFake struct {
	postgres.SocialRepository
	blocked bool
}

func (s socialFake) BlockedEither(context.Context, int64, int64) (bool, error) { return s.blocked, nil }

type messageFake struct {
	scylla.MessageRepository
	m      scylla.Message
	hidden bool
	err    error
}

func (m messageFake) Get(context.Context, int64, int64) (scylla.Message, error) { return m.m, m.err }
func (m messageFake) Hidden(context.Context, int64, int64, int64) (bool, error) { return m.hidden, nil }

type userFake struct{ postgres.UserRepository }

func (userFake) Get(context.Context, int64) (postgres.User, error) {
	return postgres.User{DisplayName: "Alice"}, nil
}

type senderFake struct {
	sent   []Payload
	err    error
	failID uuid.UUID
}

func (s *senderFake) Send(_ context.Context, d postgres.NotificationDevice, p Payload) error {
	if d.ID == s.failID {
		return errors.New("try again")
	}
	s.sent = append(s.sent, p)
	return s.err
}
func workerFixture() (*Worker, *queueFake, *senderFake, postgres.NotificationJob) {
	uid := int64(1)
	content := "private message"
	q := &queueFake{p: postgres.NotificationPreferences{Enabled: true}, devices: []postgres.NotificationDevice{{ID: uuid.New()}}, delivered: map[uuid.UUID]bool{}}
	s := &senderFake{}
	w := &Worker{Queue: q, Chats: chatFake{}, Social: socialFake{}, Users: userFake{}, Messages: messageFake{m: scylla.Message{ID: 9, ChatID: 3, SenderID: &uid, Type: scylla.TypeText, Content: &content, CreatedAt: time.Now()}}, Sender: s, Tokens: NewActionTokens("secret")}
	return w, q, s, postgres.NotificationJob{MessageID: 9, ChatID: 3, Recipients: []int64{1, 2}, CreatedAt: time.Now()}
}
func TestMessageNotificationPrivacyAndEligibility(t *testing.T) {
	for _, scenario := range []string{"private", "preview", "read", "removed", "rejoined", "blocked", "hidden", "deleted", "disabled", "expired", "missing"} {
		t.Run(scenario, func(t *testing.T) {
			w, q, s, j := workerFixture()
			want := 0
			switch scenario {
			case "private":
				want = 1
			case "preview":
				q.p.Previews = true
				want = 1
			case "read":
				id := int64(9)
				w.Chats = chatFake{p: postgres.Participant{LastReadMessageID: &id}}
			case "removed":
				w.Chats = chatFake{err: postgres.ErrNotFound}
			case "rejoined":
				w.Chats = chatFake{p: postgres.Participant{JoinedAt: time.Now().Add(time.Hour)}}
			case "blocked":
				w.Social = socialFake{blocked: true}
			case "hidden":
				m := w.Messages.(messageFake)
				m.hidden = true
				w.Messages = m
			case "deleted":
				m := w.Messages.(messageFake)
				m.m.Deleted = true
				w.Messages = m
			case "disabled":
				q.p.Enabled = false
			case "expired":
				j.CreatedAt = time.Now().Add(-time.Hour)
			case "missing":
				m := w.Messages.(messageFake)
				m.err = scylla.ErrNotFound
				w.Messages = m
			}
			err := w.Handle(context.Background(), j)
			if scenario == "missing" {
				if err == nil || q.done {
					t.Fatal("uncommitted message must stay pending")
				}
				return
			}
			if err != nil || !q.done || len(s.sent) != want {
				t.Fatalf("sent=%d done=%v err=%v", len(s.sent), q.done, err)
			}
			if want == 1 {
				p := s.sent[0]
				if p.UserID != "2" {
					t.Fatal("sender was notified")
				}
				if scenario == "private" && (p.Body != "New message" || p.Title != "Messenger") {
					t.Fatal("private content leaked")
				}
				if scenario == "preview" && p.Body != "private message" {
					t.Fatal("preview missing")
				}
				if _, chat, message, err := w.Tokens.ParseRead(p.ReadToken); err != nil || chat != 3 || message != 9 {
					t.Fatalf("read action missing: %v", err)
				}
				if scenario == "private" && p.AvatarToken != "" {
					t.Fatal("the sender's avatar leaked without previews")
				}
				if _, sender, err := w.Tokens.ParseAvatar(p.AvatarToken); scenario == "preview" && (err != nil || sender != 1) {
					t.Fatalf("preview avatar missing: %v", err)
				}
			}
		})
	}
}
func TestPartialRetryDoesNotResendSuccessfulDevices(t *testing.T) {
	w, q, s, j := workerFixture()
	bad := uuid.New()
	q.devices = append(q.devices, postgres.NotificationDevice{ID: bad})
	s.failID = bad
	if w.Handle(context.Background(), j) == nil || q.done {
		t.Fatal("partial failure must retry")
	}
	s.failID = uuid.Nil
	if err := w.Handle(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	if len(s.sent) != 2 || !q.done {
		t.Fatalf("successful destination was sent twice: %d", len(s.sent))
	}
}
func TestInvalidDeviceIsRemoved(t *testing.T) {
	w, q, s, j := workerFixture()
	s.err = ErrUnregistered
	if err := w.Handle(context.Background(), j); err != nil || !q.removed || !q.done {
		t.Fatalf("cleanup failed: %v", err)
	}
}
