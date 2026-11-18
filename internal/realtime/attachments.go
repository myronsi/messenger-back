package realtime

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/httpapi"
	"github.com/myronsi/messenger-back/internal/store/postgres"
)

type AttachmentAudience interface {
	Audience(ctx context.Context, attachmentID uuid.UUID) ([]int64, error)
}

type AttachmentNotifier struct {
	bus      Publisher
	audience AttachmentAudience
	basePath string
	log      *slog.Logger
}

func NewAttachmentNotifier(bus Publisher, audience AttachmentAudience, basePath string, log *slog.Logger) *AttachmentNotifier {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &AttachmentNotifier{bus: bus, audience: audience, basePath: basePath, log: log}
}

func (n *AttachmentNotifier) AttachmentUpdated(ctx context.Context, a postgres.Attachment) {
	users, err := n.audience.Audience(ctx, a.ID)
	if err != nil {
		n.log.WarnContext(ctx, "attachment audience", "error", err)
		return
	}
	if len(users) == 0 {
		return
	}
	payload, err := json.Marshal(serverEvent{
		Type: "attachment_updated", EventID: "0",
		Data: map[string]any{"attachment": httpapi.PresentAttachment(a, "", n.basePath)},
	})
	if err != nil {
		n.log.ErrorContext(ctx, "encode attachment event", "error", err)
		return
	}
	payloads := make(map[int64][]byte, len(users))
	for _, uid := range users {
		payloads[uid] = payload
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := n.bus.PublishEach(ctx, payloads); err != nil {
		n.log.WarnContext(ctx, "publish attachment event", "error", err, "recipients", len(payloads))
	}
}
