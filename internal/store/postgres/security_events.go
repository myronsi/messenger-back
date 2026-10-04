package postgres

import (
	"context"
	"encoding/json"

	"github.com/myronsi/messenger-back/internal/store/postgres/sqlcdb"
)

type securityEventRepo struct{ s *Store }

var _ SecurityEventRepository = securityEventRepo{}

func eventFrom(e sqlcdb.UserSecurityEvent) SecurityEvent {
	return SecurityEvent{
		ID:        e.ID,
		UserID:    e.UserID,
		Type:      e.EventType,
		IP:        e.IpAddress,
		UserAgent: e.UserAgent,
		Details:   json.RawMessage(e.Details),
		CreatedAt: e.CreatedAt,
	}
}

func (r securityEventRepo) Record(ctx context.Context, e NewSecurityEvent) (SecurityEvent, error) {
	details := []byte(e.Details)
	if len(details) == 0 {
		details = []byte("{}")
	}
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	row, err := r.s.q.RecordSecurityEvent(ctx, sqlcdb.RecordSecurityEventParams{
		UserID: e.UserID, EventType: e.Type, IpAddress: e.IP, UserAgent: e.UserAgent, Details: details,
	})
	if err != nil {
		return SecurityEvent{}, mapError(err)
	}
	return eventFrom(row), nil
}

func (r securityEventRepo) List(ctx context.Context, userID int64, beforeID *int64, limit int) ([]SecurityEvent, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	rows, err := r.s.q.ListSecurityEvents(ctx, sqlcdb.ListSecurityEventsParams{UserID: userID, BeforeID: beforeID, MaxRows: clampLimit(limit)})
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]SecurityEvent, len(rows))
	for i, e := range rows {
		out[i] = eventFrom(e)
	}
	return out, nil
}
