package realtime

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/store/postgres"
)

type capturingBus struct {
	mu       sync.Mutex
	payloads map[int64][]byte
}

func (b *capturingBus) PublishEach(_ context.Context, payloads map[int64][]byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	for k, v := range payloads {
		b.payloads[k] = v
	}
	return nil
}

func (b *capturingBus) Broadcast(context.Context, []byte) error { return nil }

type countingIDs struct{ n int64 }

func (c *countingIDs) Next() (int64, error) { c.n++; return 7217400317439950000 + c.n, nil }

func testCall(t *testing.T) (postgres.Call, uuid.UUID, uuid.UUID) {
	t.Helper()
	k, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key := base64.RawURLEncoding.EncodeToString(k.PublicKey().Bytes())
	s1, s2 := uuid.New(), uuid.New()
	now := time.Now()
	return postgres.Call{
		ID: 9, ChatID: 77, Kind: postgres.ChatGroup, InitiatorID: 1, State: postgres.CallActive, Epoch: 2, Version: 4,
		MaxParticipants: 10, CreatedAt: now, StartedAt: &now, RingExpiresAt: now.Add(30 * time.Second),
		Participants: []postgres.CallParticipant{
			{UserID: 1, Position: 0, State: postgres.CallParticipantJoined, SessionID: &s1, IdentityKey: key, JoinedAt: &now},
			{UserID: 2, Position: 1, State: postgres.CallParticipantJoined, SessionID: &s2, IdentityKey: key, JoinedAt: &now},
			{UserID: 3, Position: 2, State: postgres.CallParticipantRinging},
			{UserID: 4, Position: 3, State: postgres.CallParticipantLeft, JoinedAt: &now},
		},
	}, s1, s2
}

func TestCallEventsFollowTheContract(t *testing.T) {
	bus := &capturingBus{payloads: map[int64][]byte{}}
	f := NewFanout(FanoutDeps{Bus: bus, IDs: &countingIDs{}})
	call, s1, s2 := testCall(t)
	ctx := context.Background()

	f.CallUpdated(ctx, call, []int64{1, 2, 3})
	if len(bus.payloads) != 3 {
		t.Fatalf("call_updated went to %d users", len(bus.payloads))
	}
	var fr frame
	if err := json.Unmarshal(bus.payloads[3], &fr); err != nil {
		t.Fatal(err)
	}
	if fr.Session != nil || fr.ChatID != 77 {
		t.Fatalf("call_updated frame: %+v", fr)
	}
	if err := eventViolation(fr.Event); err != nil {
		t.Fatal(err)
	}
	updated := fr.Event

	clear(bus.payloads)
	f.CallKeys(ctx, call, 2, s2, []postgres.CallKeyEnvelope{{Epoch: 2, SenderUserID: 1, SenderSessionID: s1, RecipientSessionID: s2, Enc: "BJCC", Ciphertext: "DDDD"}})
	if len(bus.payloads) != 1 {
		t.Fatalf("call_keys went to %d users", len(bus.payloads))
	}
	fr = frame{}
	if err := json.Unmarshal(bus.payloads[2], &fr); err != nil {
		t.Fatal(err)
	}
	if fr.Session == nil || *fr.Session != s2 {
		t.Fatalf("call_keys frame carries no session filter: %+v", fr)
	}
	if err := eventViolation(fr.Event); err != nil {
		t.Fatal(err)
	}

	g, err := New(Options{Fanout: f, MinClientAPIVersion: "2.0.0-alpha.1"})
	if err != nil {
		t.Fatal(err)
	}
	mine, other := newConn(g, nil, 2, s2), newConn(g, nil, 2, uuid.New())
	g.users[2] = &userState{conns: map[*conn]struct{}{mine: {}, other: {}}, removed: map[int64]bool{}, presence: map[int64]int64{}}
	g.Deliver(2, bus.payloads[2])
	if len(mine.send) != 1 || len(other.send) != 0 {
		t.Fatalf("call_keys reached %d and %d connections", len(mine.send), len(other.send))
	}
	raw, _ := json.Marshal(frame{Kind: kindEvent, ChatID: 77, Event: updated})
	g.Deliver(2, raw)
	if len(mine.send) != 2 || len(other.send) != 1 {
		t.Fatal("call_updated must reach every connection of the user")
	}
}

func TestCallExamplesFollowTheContract(t *testing.T) {
	for _, name := range []string{"call_updated", "call_keys"} {
		raw, err := os.ReadFile("../../api/websocket/examples/server/" + name + ".json")
		if err != nil {
			t.Fatal(err)
		}
		if err := eventViolation(raw); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}
