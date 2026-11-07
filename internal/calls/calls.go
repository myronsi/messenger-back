package calls

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/messages"
	"github.com/myronsi/messenger-back/internal/notifications"
	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/scylla"
)

var (
	ErrDisabled         = errors.New("calls are switched off")
	ErrBusy             = errors.New("the server is at its call capacity")
	ErrNotFound         = errors.New("not found")
	ErrForbidden        = errors.New("forbidden")
	ErrNotAllowed       = errors.New("calls not allowed")
	ErrIdentityRequired = errors.New("call identity required")
	ErrFull             = errors.New("call full")
	ErrEnded            = errors.New("call ended")
	ErrAlreadyInCall    = errors.New("already in a call")
	ErrStaleEpoch       = errors.New("stale epoch")
	ErrNotJoined        = errors.New("this session has not joined the call")
	ErrInvalid          = errors.New("invalid request")
	ErrUnavailable      = errors.New("temporarily unavailable")
)

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

func unavailable(err error) error { return fmt.Errorf("%w: %w", ErrUnavailable, err) }

func Code(err error) string {
	switch {
	case errors.Is(err, ErrDisabled), errors.Is(err, ErrBusy):
		return "calls_unavailable"
	case errors.Is(err, ErrNotFound):
		return "not_found"
	case errors.Is(err, ErrForbidden):
		return "forbidden"
	case errors.Is(err, ErrNotAllowed):
		return "calls_not_allowed"
	case errors.Is(err, ErrIdentityRequired):
		return "call_identity_required"
	case errors.Is(err, ErrFull):
		return "call_full"
	case errors.Is(err, ErrEnded):
		return "call_ended"
	case errors.Is(err, ErrAlreadyInCall):
		return "already_in_call"
	case errors.Is(err, ErrStaleEpoch):
		return "stale_epoch"
	case errors.Is(err, ErrNotJoined):
		return "conflict"
	case errors.Is(err, ErrInvalid):
		return "validation_failed"
	}
	return "internal_error"
}

type Config struct {
	PublicURL             string
	MaxParticipants       int
	MaxActiveParticipants int
	RingTimeout           time.Duration
	EmptyGrace            time.Duration
	TokenTTL              time.Duration
}

const Retention = 90 * 24 * time.Hour

const staleAfter = 24 * time.Hour

type Store interface {
	Calls() postgres.CallRepository
	Chats() postgres.ChatRepository
	Social() postgres.SocialRepository
	Users() postgres.UserRepository
	Notifications() postgres.NotificationRepository
}

type Notifier interface {
	CallUpdated(ctx context.Context, c postgres.Call, recipients []int64)
	CallKeys(ctx context.Context, c postgres.Call, recipient int64, session uuid.UUID, envelopes []postgres.CallKeyEnvelope)
}

type SystemSender interface {
	SystemMessageID() (int64, error)
	SendSystemAs(ctx context.Context, chatID, messageID int64, e messages.SystemEvent) (scylla.Message, error)
}

type Media interface {
	CreateRoom(ctx context.Context, room string, emptyTimeout, departureTimeout time.Duration, maxParticipants int) error
	RemoveParticipant(ctx context.Context, room, identity string) error
	DeleteRoom(ctx context.Context, room string) error
	ListParticipants(ctx context.Context, room string) ([]string, error)
}

type Pusher interface {
	SendCall(ctx context.Context, d postgres.NotificationDevice, p notifications.CallPayload, ttl time.Duration) error
}

type Metrics interface {
	CallStarted()
	CallEnded(reason string)
	CallParticipants(n int)
	CallJoinFailed(code string)
	CallWebhook(event, outcome string)
}

type Once interface {
	Claim(ctx context.Context, id string, ttl time.Duration) (bool, error)
	Release(ctx context.Context, id string) error
}

type Deps struct {
	Store    Store
	Members  func(ctx context.Context, chatID int64) ([]int64, error)
	Notifier Notifier
	System   SystemSender
	Media    Media
	Grants   *Grants
	Push     Pusher
	Tokens   *notifications.ActionTokens
	Metrics  Metrics
	Webhooks Once
	Log      *slog.Logger
}

type Service struct {
	d        Deps
	cfg      Config
	now      func() time.Time
	bg       sync.WaitGroup
	sweeping sync.Mutex
}

func New(cfg Config, d Deps) *Service {
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if d.Metrics == nil {
		d.Metrics = nopMetrics{}
	}
	return &Service{d: d, cfg: cfg, now: time.Now}
}

func (s *Service) Config() Config { return s.cfg }

type nopMetrics struct{}

func (nopMetrics) CallStarted()               {}
func (nopMetrics) CallEnded(string)           {}
func (nopMetrics) CallParticipants(int)       {}
func (nopMetrics) CallJoinFailed(string)      {}
func (nopMetrics) CallWebhook(string, string) {}

func (s *Service) background(ctx context.Context, fn func(ctx context.Context)) {
	ctx = context.WithoutCancel(ctx)
	s.bg.Go(func() {
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		fn(ctx)
	})
}

func (s *Service) Wait(ctx context.Context) {
	done := make(chan struct{})
	go func() { s.bg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

func RoomName(callID int64) string { return "call-" + strconv.FormatInt(callID, 10) }

func roomCall(room string) (int64, bool) {
	id, err := strconv.ParseInt(strings.TrimPrefix(room, "call-"), 10, 64)
	return id, strings.HasPrefix(room, "call-") && err == nil && id > 0
}

func Identity(userID int64, session uuid.UUID) string {
	return strconv.FormatInt(userID, 10) + ":" + session.String()
}

func parseIdentity(s string) (int64, uuid.UUID, bool) {
	u, sid, ok := strings.Cut(s, ":")
	if !ok {
		return 0, uuid.UUID{}, false
	}
	uid, err := strconv.ParseInt(u, 10, 64)
	if err != nil || uid <= 0 {
		return 0, uuid.UUID{}, false
	}
	id, err := uuid.Parse(sid)
	return uid, id, err == nil
}
