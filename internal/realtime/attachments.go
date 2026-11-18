package realtime

import (
	"context"
	"log/slog"

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
	publishToUsers(ctx, n.bus, n.log, users, "attachment_updated", map[string]any{"attachment": httpapi.PresentAttachment(a, "", n.basePath)})
}
