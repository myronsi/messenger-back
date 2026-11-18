package realtime

import (
	"context"
	"encoding/json"
	"testing"
)

type capture struct{ payloads map[int64][]byte }

func (c *capture) PublishEach(_ context.Context, p map[int64][]byte) error {
	c.payloads = p
	return nil
}

func (c *capture) Broadcast(context.Context, []byte) error { return nil }

func TestUserEventsAreFramedForTheGateway(t *testing.T) {
	bus := &capture{}
	NewUserNotifier(bus, nil).NotifyUser(context.Background(), 7, "account_status", map[string]any{"status": "warned"})
	var f frame
	if err := json.Unmarshal(bus.payloads[7], &f); err != nil || f.Kind != kindEvent || len(f.Event) == 0 {
		t.Fatalf("frame %s (%v)", bus.payloads[7], err)
	}
	var ev struct {
		Type   string          `json:"type"`
		ChatID *string         `json:"chat_id"`
		Data   json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(f.Event, &ev); err != nil || ev.Type != "account_status" || ev.ChatID != nil || string(ev.Data) != `{"status":"warned"}` {
		t.Fatalf("event %s (%v)", f.Event, err)
	}
}
