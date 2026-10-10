package redis

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// Bus delivers events between API instances over Redis pub/sub. Every user has a channel user:{user_id};
// an instance subscribes to it while the user has a connection there, and whoever produces an event
// publishes it to the channel of every recipient (for a message: every member of the chat).
//
// Pub/sub is fire and forget: an event published while an instance is reconnecting to Redis is lost for
// the users connected there. Clients close that gap by loading what they missed through REST after a
// reconnect (the `after` cursor); durable work goes through the event streams instead.
type Bus struct {
	rdb     goredis.UniversalClient
	prefix  string
	handler func(userID int64, payload []byte)

	ps *goredis.PubSub

	mu   sync.Mutex
	subs map[int64]int
}

// BusOptions configures a Bus.
type BusOptions struct {
	// Prefix is put in front of every channel ("" in production).
	Prefix string
	// Handler receives every event published to a subscribed user, on the receiving goroutine of the bus.
	// It must not block: hand the payload to the connections' send buffers and return.
	Handler func(userID int64, payload []byte)
}

// NewBus creates the bus of this instance. Call Run to start receiving.
func NewBus(rdb goredis.UniversalClient, o BusOptions) (*Bus, error) {
	if o.Handler == nil {
		return nil, errors.New("bus: handler is required")
	}
	return &Bus{
		rdb: rdb, prefix: o.Prefix, handler: o.Handler,
		// No channels yet: SUBSCRIBE is sent when the first user subscribes.
		ps:   rdb.Subscribe(context.Background()),
		subs: make(map[int64]int),
	}, nil
}

func (b *Bus) channel(userID int64) string {
	return b.prefix + "user:" + strconv.FormatInt(userID, 10)
}

func (b *Bus) userOf(channel string) (int64, bool) {
	s, ok := strings.CutPrefix(channel, b.prefix+"user:")
	if !ok {
		return 0, false
	}
	uid, err := strconv.ParseInt(s, 10, 64)
	return uid, err == nil
}

// Subscribe starts delivering the user's events to this instance. Calls are counted: the channel is left
// when Unsubscribe was called as often.
func (b *Bus) Subscribe(ctx context.Context, userID int64) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.subs[userID]++
	if b.subs[userID] > 1 {
		return nil
	}
	if err := b.ps.Subscribe(ctx, b.channel(userID)); err != nil {
		// Keep the count: the subscription is in the PubSub's set and is sent again on its reconnect.
		return fmt.Errorf("bus subscribe: %w", err)
	}
	return nil
}

// Unsubscribe stops delivering the user's events once every Subscribe has been matched.
func (b *Bus) Unsubscribe(ctx context.Context, userID int64) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	n, ok := b.subs[userID]
	if !ok {
		return nil
	}
	if n > 1 {
		b.subs[userID] = n - 1
		return nil
	}
	delete(b.subs, userID)
	if err := b.ps.Unsubscribe(ctx, b.channel(userID)); err != nil {
		return fmt.Errorf("bus unsubscribe: %w", err)
	}
	return nil
}

// Subscribed reports whether the user's channel is subscribed on this instance.
func (b *Bus) Subscribed(userID int64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.subs[userID] > 0
}

// Publish sends the payload to the channels of all users in one round trip. It returns how many
// subscriptions received it (an instance counts once per user it serves).
func (b *Bus) Publish(ctx context.Context, payload []byte, userIDs ...int64) (int64, error) {
	if len(userIDs) == 0 {
		return 0, nil
	}
	pipe := b.rdb.Pipeline()
	cmds := make([]*goredis.IntCmd, len(userIDs))
	for i, uid := range userIDs {
		cmds[i] = pipe.Publish(ctx, b.channel(uid), payload)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, fmt.Errorf("bus publish: %w", err)
	}
	var n int64
	for _, c := range cmds {
		n += c.Val()
	}
	return n, nil
}

// Run receives events and hands them to the handler until ctx is done or Close is called. The PubSub
// pings Redis while idle, reconnects after errors and subscribes to its channels again.
func (b *Bus) Run(ctx context.Context) {
	stop := context.AfterFunc(ctx, func() { _ = b.ps.Close() })
	defer stop()
	ch := b.ps.Channel(
		goredis.WithChannelSize(1024),
		goredis.WithChannelHealthCheckInterval(15*time.Second),
		goredis.WithChannelSendTimeout(time.Second),
	)
	for m := range ch {
		uid, ok := b.userOf(m.Channel)
		if !ok {
			continue
		}
		b.handler(uid, []byte(m.Payload))
	}
}

// Close stops the bus.
func (b *Bus) Close() error { return b.ps.Close() }
