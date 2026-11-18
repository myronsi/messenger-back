package realtime

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"
)

type UserNotifier struct {
	bus Publisher
	log *slog.Logger
}

func NewUserNotifier(bus Publisher, log *slog.Logger) *UserNotifier {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &UserNotifier{bus: bus, log: log}
}

func (n *UserNotifier) NotifyUser(ctx context.Context, uid int64, kind string, data any) {
	publishToUsers(ctx, n.bus, n.log, []int64{uid}, kind, data)
}

func publishToUsers(ctx context.Context, bus Publisher, log *slog.Logger, users []int64, kind string, data any) {
	if len(users) == 0 {
		return
	}
	event, err := json.Marshal(serverEvent{Type: kind, EventID: "0", Data: data})
	if err != nil {
		log.ErrorContext(ctx, "encode user event", "error", err, "kind", kind)
		return
	}
	payload, err := json.Marshal(frame{Kind: kindEvent, Event: event})
	if err != nil {
		log.ErrorContext(ctx, "encode user frame", "error", err, "kind", kind)
		return
	}
	payloads := make(map[int64][]byte, len(users))
	for _, uid := range users {
		payloads[uid] = payload
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := bus.PublishEach(ctx, payloads); err != nil {
		log.WarnContext(ctx, "publish user event", "error", err, "kind", kind, "recipients", len(payloads))
	}
}
