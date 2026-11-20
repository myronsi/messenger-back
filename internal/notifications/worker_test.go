package notifications

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gocql/gocql"
	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/scylla"
)

type queueFake struct {
	postgres.NotificationRepository
	mu         sync.Mutex
	recipients []postgres.NotificationRecipient
	devices    []postgres.NotificationDevice
	done       bool
	deferredTo time.Time
	delivered  map[uuid.UUID]bool
	rejected   map[uuid.UUID]bool
	removed    bool
}

func (q *queueFake) Recipients(_ context.Context, _, sender int64, ids []int64) ([]postgres.NotificationRecipient, error) {
	out := []postgres.NotificationRecipient{}
	for _, r := range q.recipients {
		if r.UserID != sender && slices.Contains(ids, r.UserID) {
			out = append(out, r)
		}
	}
	return out, nil
}
func (q *queueFake) Devices(_ context.Context, _, _ int64, ids []int64) ([]postgres.NotificationDevice, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := []postgres.NotificationDevice{}
	for _, d := range q.devices {
		if !q.delivered[d.ID] && !q.rejected[d.ID] && slices.Contains(ids, d.UserID) {
			out = append(out, d)
		}
	}
	return out, nil
}
func (q *queueFake) Finish(context.Context, int64) error { q.done = true; return nil }
func (q *queueFake) Defer(_ context.Context, _ int64, until time.Time) error {
	q.deferredTo = until
	return nil
}
func (q *queueFake) Delivered(_ context.Context, _ int64, id uuid.UUID) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.delivered[id] = true
	return nil
}
func (q *queueFake) Rejected(_ context.Context, _ int64, id uuid.UUID, counts bool) (bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.rejected[id] = counts
	return false, nil
}
func (q *queueFake) RemoveDevice(context.Context, uuid.UUID) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.removed = true
	return nil
}

type chatFake struct {
	postgres.ChatRepository
	chat postgres.Chat
}

func (c chatFake) Get(context.Context, int64) (postgres.Chat, error) { return c.chat, nil }

type socialFake struct {
	postgres.SocialRepository
	names map[int64]string
}

func (s socialFake) ContactNamesFor(context.Context, []int64, int64) (map[int64]string, error) {
	return s.names, nil
}

type messageFake struct {
	scylla.MessageRepository
	m      scylla.Message
	hidden bool
	err    error
}

func (m messageFake) Get(context.Context, int64, int64) (scylla.Message, error) { return m.m, m.err }
func (m messageFake) Hidden(context.Context, int64, int64, int64) (bool, error) { return m.hidden, nil }

type userFake struct {
	postgres.UserRepository
	name string
}

func (u userFake) Get(context.Context, int64) (postgres.User, error) {
	return postgres.User{DisplayName: u.name}, nil
}

type attachmentFake struct {
	postgres.AttachmentRepository
	mime string
}

func (a attachmentFake) Get(context.Context, uuid.UUID) (postgres.Attachment, error) {
	return postgres.Attachment{MimeType: a.mime}, nil
}

type senderFake struct {
	mu     sync.Mutex
	sent   []Payload
	err    error
	failID uuid.UUID
}

func (s *senderFake) Send(_ context.Context, d postgres.NotificationDevice, p Payload) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d.ID == s.failID {
		return errors.New("try again")
	}
	s.sent = append(s.sent, p)
	return s.err
}

func workerFixture() (*Worker, *queueFake, *senderFake, postgres.NotificationJob) {
	uid := int64(1)
	content := "private message"
	q := &queueFake{
		recipients: []postgres.NotificationRecipient{{UserID: 2, Preferences: postgres.NotificationPreferences{Enabled: true}}},
		devices:    []postgres.NotificationDevice{{ID: uuid.New(), UserID: 2, SessionID: uuid.New()}},
		delivered:  map[uuid.UUID]bool{}, rejected: map[uuid.UUID]bool{},
	}
	s := &senderFake{}
	w := &Worker{
		Queue: q, Chats: chatFake{chat: postgres.Chat{ID: 3, Type: postgres.ChatDirect}}, Social: socialFake{}, Users: userFake{name: "Alice"},
		Messages: messageFake{m: scylla.Message{ID: 9, ChatID: 3, SenderID: &uid, Type: scylla.TypeText, Content: &content, CreatedAt: time.Now()}},
		Sender:   s, Tokens: NewActionTokens("secret"), Log: slog.New(slog.DiscardHandler),
	}
	return w, q, s, postgres.NotificationJob{MessageID: 9, ChatID: 3, Recipients: []int64{1, 2}, CreatedAt: time.Now()}
}

func previews(q *queueFake) { q.recipients[0].Preferences.Previews = true }

func TestMessageNotificationPrivacyAndEligibility(t *testing.T) {
	for _, scenario := range []string{"private", "preview", "read", "removed", "rejoined", "blocked", "hidden", "deleted", "disabled", "expired", "missing"} {
		t.Run(scenario, func(t *testing.T) {
			w, q, s, j := workerFixture()
			want := 0
			switch scenario {
			case "private":
				want = 1
			case "preview":
				previews(q)
				want = 1
			case "read":
				id := int64(9)
				q.recipients[0].LastReadMessageID = &id
			case "removed":
				q.recipients = nil
			case "rejoined":
				q.recipients[0].JoinedAt = time.Now().Add(time.Hour)
			case "blocked":
				q.recipients[0].Blocked = true
			case "hidden":
				m := w.Messages.(messageFake)
				m.hidden = true
				w.Messages = m
			case "deleted":
				m := w.Messages.(messageFake)
				m.m.Deleted = true
				w.Messages = m
			case "disabled":
				q.recipients[0].Preferences.Enabled = false
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
			if want == 0 {
				return
			}
			p := s.sent[0]
			if p.UserID != "2" || p.ContentType != ContentText || p.ChatType != "direct" {
				t.Fatalf("unexpected payload: %+v", p)
			}
			if scenario == "private" && (p.Body != "" || p.Title != "Messenger" || p.ChatTitle != "") {
				t.Fatal("private content leaked")
			}
			if scenario == "preview" && (p.Body != "private message" || p.Title != "Alice") {
				t.Fatal("preview missing")
			}
			if got, err := w.Tokens.ParseRead(p.ReadToken); err != nil || got.IDs[0] != 3 || got.IDs[1] != 9 || got.Device != q.devices[0].ID {
				t.Fatalf("read action missing: %v", err)
			}
			if (p.SenderID != "") != (scenario == "preview") || scenario == "preview" && p.SenderID != "1" {
				t.Fatal("the sender id must come only with previews")
			}
			if p.Preview != (scenario == "preview") {
				t.Fatal("the preview flag does not match the preferences")
			}
			if scenario == "private" && p.AvatarToken != "" {
				t.Fatal("the sender's avatar leaked without previews")
			}
			if got, err := w.Tokens.ParseAvatar(p.AvatarToken); scenario == "preview" && (err != nil || got.IDs[0] != 1) {
				t.Fatalf("preview avatar missing: %v", err)
			}
		})
	}
}

func TestPreviewUsesContactNameAndClipsLongText(t *testing.T) {
	w, q, s, j := workerFixture()
	previews(q)
	w.Social = socialFake{names: map[int64]string{2: "Mom"}}
	long := strings.Repeat("ж", 300)
	m := w.Messages.(messageFake)
	m.m.Content = &long
	w.Messages = m
	if err := w.Handle(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	p := s.sent[0]
	if p.Title != "Mom" || len([]rune(p.Body)) != bodyRunes || !strings.HasSuffix(p.Body, "…") {
		t.Fatalf("title=%q body runes=%d", p.Title, len([]rune(p.Body)))
	}
}

func TestGroupPreviewCarriesChatTitleAndPicture(t *testing.T) {
	for _, picture := range []bool{true, false} {
		t.Run(fmt.Sprint("picture=", picture), func(t *testing.T) {
			w, q, s, j := workerFixture()
			previews(q)
			name := "Family"
			chat := postgres.Chat{ID: 3, Type: postgres.ChatGroup, Name: &name}
			if picture {
				id := uuid.New()
				chat.AvatarAttachmentID = &id
			}
			w.Chats = chatFake{chat: chat}
			if err := w.Handle(context.Background(), j); err != nil {
				t.Fatal(err)
			}
			p := s.sent[0]
			if p.ChatType != "group" || p.ChatTitle != "Family" || p.Title != "Alice" {
				t.Fatalf("group payload: %+v", p)
			}
			_, chatErr := w.Tokens.ParseChatAvatar(p.AvatarToken)
			_, userErr := w.Tokens.ParseAvatar(p.AvatarToken)
			if picture && chatErr != nil || !picture && userErr != nil {
				t.Fatalf("wrong avatar token: chat=%v user=%v", chatErr, userErr)
			}
		})
	}
}

func TestContentTypeReplacesEnglishText(t *testing.T) {
	emoji := "😀"
	one := []gocql.UUID{gocql.TimeUUID()}
	for _, tc := range []struct {
		name, mime, want, body string
		msg                    scylla.Message
	}{
		{"photo", "image/jpeg", ContentPhoto, "", scylla.Message{Type: scylla.TypeFile, AttachmentID: &one[0]}},
		{"video", "video/mp4", ContentVideo, "", scylla.Message{Type: scylla.TypeFile, AttachmentID: &one[0]}},
		{"file", "application/pdf", ContentFile, "", scylla.Message{Type: scylla.TypeFile, AttachmentID: &one[0]}},
		{"album", "image/png", ContentAlbum, "", scylla.Message{Type: scylla.TypeFile, AlbumIDs: []gocql.UUID{one[0], gocql.TimeUUID()}}},
		{"voice", "", ContentVoice, "", scylla.Message{Type: scylla.TypeVoice}},
		{"sticker", "", ContentSticker, emoji, scylla.Message{Type: scylla.TypeSticker, Content: &emoji}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, q, s, j := workerFixture()
			previews(q)
			w.Attachments = attachmentFake{mime: tc.mime}
			m := w.Messages.(messageFake)
			sender := int64(1)
			tc.msg.ID, tc.msg.ChatID, tc.msg.SenderID, tc.msg.CreatedAt = 9, 3, &sender, time.Now()
			m.m = tc.msg
			w.Messages = m
			if err := w.Handle(context.Background(), j); err != nil {
				t.Fatal(err)
			}
			if p := s.sent[0]; p.ContentType != tc.want || p.Body != tc.body {
				t.Fatalf("content_type=%q body=%q", p.ContentType, p.Body)
			}
		})
	}
}

func TestActiveDeviceIsCheckedAgainAfterItsWindow(t *testing.T) {
	w, q, s, j := workerFixture()
	until := time.Now().Add(20 * time.Second)
	q.devices[0].ActiveUntil = &until
	if err := w.Handle(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	if len(s.sent) != 0 || q.done || !q.deferredTo.Equal(until.Add(activityGrace)) {
		t.Fatalf("active device: sent=%d done=%v deferred=%v", len(s.sent), q.done, q.deferredTo)
	}
	j.Deferred = true
	if err := w.Handle(context.Background(), j); err != nil || len(s.sent) != 0 || !q.done {
		t.Fatalf("still active after the window must finish without a push: sent=%d err=%v", len(s.sent), err)
	}
	q.done = false
	q.devices[0].ActiveUntil = nil
	if err := w.Handle(context.Background(), j); err != nil || len(s.sent) != 1 || !q.done {
		t.Fatalf("app closed during the window must get the push: sent=%d err=%v", len(s.sent), err)
	}
}

func TestPermanentRejectionFinishesTheJob(t *testing.T) {
	for _, tc := range []struct {
		err    error
		counts bool
	}{{fmt.Errorf("%w: web push returned status 403", ErrRejected), true}, {fmt.Errorf("%w: web push returned status 413", ErrTooLarge), false}} {
		w, q, s, j := workerFixture()
		s.err = tc.err
		if err := w.Handle(context.Background(), j); err != nil || !q.done {
			t.Fatalf("rejection must not retry: %v", err)
		}
		counts, ok := q.rejected[q.devices[0].ID]
		if !ok || counts != tc.counts {
			t.Fatalf("rejection not recorded: %v %v", ok, counts)
		}
	}
}

func TestDevicesAreSentInParallel(t *testing.T) {
	w, q, _, j := workerFixture()
	for range 15 {
		q.devices = append(q.devices, postgres.NotificationDevice{ID: uuid.New(), UserID: 2})
	}
	gate := &blockingSender{release: make(chan struct{})}
	w.Sender = gate
	done := make(chan error, 1)
	go func() { done <- w.Handle(context.Background(), j) }()
	deadline := time.After(5 * time.Second)
	for gate.waiting() < sendsAtOnce {
		select {
		case <-deadline:
			t.Fatalf("only %d sends started at once", gate.waiting())
		case <-time.After(5 * time.Millisecond):
		}
	}
	close(gate.release)
	if err := <-done; err != nil || !q.done || len(q.delivered) != 16 {
		t.Fatalf("err=%v delivered=%d", err, len(q.delivered))
	}
}

type blockingSender struct {
	mu      sync.Mutex
	active  int
	release chan struct{}
}

func (b *blockingSender) waiting() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.active
}

func (b *blockingSender) Send(context.Context, postgres.NotificationDevice, Payload) error {
	b.mu.Lock()
	b.active++
	b.mu.Unlock()
	<-b.release
	return nil
}

func TestPartialRetryDoesNotResendSuccessfulDevices(t *testing.T) {
	w, q, s, j := workerFixture()
	bad := uuid.New()
	q.devices = append(q.devices, postgres.NotificationDevice{ID: bad, UserID: 2})
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
