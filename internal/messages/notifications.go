package messages

import (
	"context"

	"github.com/myronsi/messenger-back/internal/store/scylla"
)

func (s *Service) queueNotification(ctx context.Context, m scylla.Message, members []int64) error {
	if s.d.QueueNotification == nil || m.SenderID == nil || m.Type == scylla.TypeSystem {
		return nil
	}
	return s.d.QueueNotification(ctx, m.ChatID, m.ID, without(members, *m.SenderID))
}
