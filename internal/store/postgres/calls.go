package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/myronsi/messenger-back/internal/store/postgres/sqlcdb"
)

const (
	CallRinging = "ringing"
	CallActive  = "active"
	CallEnded   = "ended"

	CallParticipantRinging  = "ringing"
	CallParticipantJoined   = "joined"
	CallParticipantDeclined = "declined"
	CallParticipantMissed   = "missed"
	CallParticipantLeft     = "left"
)

type Call struct {
	ID              int64
	ChatID          int64
	Kind            ChatType
	InitiatorID     int64
	State           string
	EndReason       string
	Epoch           int64
	Version         int64
	MaxParticipants int
	CreatedAt       time.Time
	StartedAt       *time.Time
	EndedAt         *time.Time
	RingExpiresAt   time.Time
	EmptySince      *time.Time
	Participants    []CallParticipant
}

type CallParticipant struct {
	UserID         int64
	Position       int
	State          string
	SessionID      *uuid.UUID
	IdentityKey    string
	JoinedAt       *time.Time
	LeftAt         *time.Time
	DisconnectedAt *time.Time
}

type NewCall struct {
	ChatID          int64
	Kind            ChatType
	InitiatorID     int64
	Ring            func(others []CallCandidate) ([]int64, error)
	MaxParticipants int
	RingExpiresAt   time.Time
	ClientRequestID uuid.UUID
}

type CallCandidate struct {
	UserID       int64
	CallsEnabled bool
	Blocked      bool
}

type CallMember struct {
	ChatID    int64
	UserID    int64
	SessionID uuid.UUID
}

type CallGuard struct {
	IdentityKey string
	Blocked     bool
}

type CallStart struct {
	Call    Call
	Created bool
}

type CallKeyEnvelope struct {
	Epoch              int64
	SenderUserID       int64
	SenderSessionID    uuid.UUID
	RecipientSessionID uuid.UUID
	Enc                string
	Ciphertext         string
}

type CallPushDevice struct {
	NotificationDevice
	UserID   int64
	Enabled  bool
	Previews bool
}

type CallPurge struct {
	Calls, StartRequests, Envelopes, Identities int64
}

type CallRepository interface {
	StartRequest(ctx context.Context, userID int64, requestID uuid.UUID) (CallStart, error)
	Start(ctx context.Context, n NewCall) (CallStart, error)
	Get(ctx context.Context, id int64) (Call, error)
	OpenInChat(ctx context.Context, chatID int64) (Call, error)
	ForUser(ctx context.Context, userID int64) ([]Call, error)
	Update(ctx context.Context, id int64, fn func(c *Call) (bool, error)) (Call, bool, error)
	UpdateAsMember(ctx context.Context, id int64, m CallMember, fn func(c *Call, g CallGuard) (bool, error)) (Call, bool, error)
	JoinedCallOf(ctx context.Context, userID int64) (int64, error)
	CallsOfSessions(ctx context.Context, sessions []uuid.UUID) ([]int64, error)
	JoinedCount(ctx context.Context) (int, error)
	Due(ctx context.Context, now time.Time, grace, staleAfter time.Duration, limit int) ([]int64, error)
	OpenIDs(ctx context.Context, limit int) ([]int64, error)
	ClaimSummary(ctx context.Context, id, messageID int64) (int64, bool, error)
	ReleaseSummary(ctx context.Context, id int64) error
	FinishSummary(ctx context.Context, id int64) error
	PendingSummaries(ctx context.Context, since, until time.Time, limit int) ([]int64, error)
	DeleteOrphans(ctx context.Context) (int64, error)
	Purge(ctx context.Context, retention time.Duration) (CallPurge, error)

	SetIdentity(ctx context.Context, userID int64, sessionID uuid.UUID, publicKey string) (bool, error)
	Identity(ctx context.Context, sessionID uuid.UUID) (string, error)
	DeleteIdentities(ctx context.Context, sessions []uuid.UUID) error

	PutEnvelopes(ctx context.Context, callID int64, check func(Call) error, sender CallKeyEnvelope, recipients []CallKeyEnvelope) (Call, error)
	Envelopes(ctx context.Context, callID, epoch int64, recipient uuid.UUID) ([]CallKeyEnvelope, error)
	PushDevices(ctx context.Context, userIDs []int64) ([]CallPushDevice, error)
}

type callRepo struct{ s *Store }

var _ CallRepository = callRepo{}

func (s *Store) Calls() CallRepository { return callRepo{s} }

func callFrom(c sqlcdb.Call, ps []sqlcdb.CallParticipant) Call {
	out := Call{
		ID: c.ID, Kind: ChatType(c.Kind), InitiatorID: c.InitiatorID, State: c.State, Epoch: c.Epoch,
		Version: c.Version, MaxParticipants: int(c.MaxParticipants), CreatedAt: c.CreatedAt, StartedAt: c.StartedAt,
		EndedAt: c.EndedAt, RingExpiresAt: c.RingExpiresAt, EmptySince: c.EmptySince,
		Participants: make([]CallParticipant, 0, len(ps)),
	}
	if c.ChatID != nil {
		out.ChatID = *c.ChatID
	}
	if c.EndReason != nil {
		out.EndReason = *c.EndReason
	}
	for _, p := range ps {
		cp := CallParticipant{
			UserID: p.UserID, Position: int(p.Position), State: p.State, SessionID: p.SessionID,
			JoinedAt: p.JoinedAt, LeftAt: p.LeftAt, DisconnectedAt: p.DisconnectedAt,
		}
		if p.IdentityKey != nil {
			cp.IdentityKey = *p.IdentityKey
		}
		out.Participants = append(out.Participants, cp)
	}
	return out
}

func (r callRepo) load(ctx context.Context, q *sqlcdb.Queries, c sqlcdb.Call) (Call, error) {
	ps, err := q.ListCallParticipants(ctx, c.ID)
	if err != nil {
		return Call{}, err
	}
	return callFrom(c, ps), nil
}

func (r callRepo) StartRequest(ctx context.Context, userID int64, requestID uuid.UUID) (CallStart, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	req, err := r.s.q.GetCallStartRequest(ctx, sqlcdb.GetCallStartRequestParams{UserID: userID, ClientRequestID: requestID})
	if err != nil {
		return CallStart{}, mapError(err)
	}
	c, err := r.s.q.GetCall(ctx, req.CallID)
	if err != nil {
		return CallStart{}, mapError(err)
	}
	call, err := r.load(ctx, r.s.q, c)
	if err != nil {
		return CallStart{}, mapError(err)
	}
	return CallStart{Call: call, Created: req.Created}, nil
}

var (
	errCallExists      = errors.New("the chat has a call")
	errJoinedElsewhere = errors.New("joined to another call")
)

func isUniqueViolation(err error, constraint string) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == codeUniqueViolation && pg.ConstraintName == constraint
}

func (r callRepo) Start(ctx context.Context, n NewCall) (CallStart, error) {
	var out CallStart
	var err error
	for range 2 {
		err = r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
			out = CallStart{}
			rows, err := q.ListCallCandidates(ctx, sqlcdb.ListCallCandidatesParams{InitiatorID: n.InitiatorID, ChatID: n.ChatID})
			if err != nil {
				return err
			}
			member := false
			others := make([]CallCandidate, 0, len(rows))
			for _, row := range rows {
				if row.UserID == n.InitiatorID {
					member = true
					continue
				}
				others = append(others, CallCandidate{UserID: row.UserID, CallsEnabled: row.CallsEnabled, Blocked: row.Blocked})
			}
			if !member {
				return ErrNotFound
			}
			var ring []int64
			if n.Ring != nil {
				if ring, err = n.Ring(others); err != nil {
					return err
				}
			}
			started, err := r.startIn(ctx, q, n, ring)
			if err != nil {
				return err
			}
			out = started
			return q.SaveCallStartRequest(ctx, sqlcdb.SaveCallStartRequestParams{
				UserID: n.InitiatorID, ClientRequestID: n.ClientRequestID, CallID: out.Call.ID, Created: out.Created,
			})
		})
		if !errors.Is(err, errCallExists) {
			break
		}
	}
	if err != nil {
		return CallStart{}, mapError(err)
	}
	return out, nil
}

func (r callRepo) startIn(ctx context.Context, q *sqlcdb.Queries, n NewCall, ring []int64) (CallStart, error) {
	chatID := n.ChatID
	existing, err := q.OpenCallInChat(ctx, &chatID)
	if err == nil {
		call, err := r.load(ctx, q, existing)
		return CallStart{Call: call}, err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return CallStart{}, err
	}
	c, err := q.InsertCall(ctx, sqlcdb.InsertCallParams{
		ChatID: &chatID, Kind: string(n.Kind), InitiatorID: n.InitiatorID,
		MaxParticipants: int32(min(n.MaxParticipants, 1<<20)), RingExpiresAt: n.RingExpiresAt, //nolint:gosec
	})
	if isUniqueViolation(err, "calls_one_open_per_chat") {
		return CallStart{}, errCallExists
	}
	if err != nil {
		return CallStart{}, err
	}
	users := append([]int64{n.InitiatorID}, ring...)
	positions := make([]int32, len(users))
	states := make([]string, len(users))
	for i := range users {
		positions[i] = int32(min(i, 1<<20)) //nolint:gosec
		states[i] = CallParticipantRinging
	}
	states[0] = CallParticipantLeft
	if err := q.InsertCallParticipants(ctx, sqlcdb.InsertCallParticipantsParams{CallID: c.ID, UserIds: users, Positions: positions, States: states}); err != nil {
		return CallStart{}, err
	}
	call, err := r.load(ctx, q, c)
	return CallStart{Call: call, Created: true}, err
}

func (r callRepo) Get(ctx context.Context, id int64) (Call, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	c, err := r.s.q.GetCall(ctx, id)
	if err != nil {
		return Call{}, mapError(err)
	}
	call, err := r.load(ctx, r.s.q, c)
	return call, mapError(err)
}

func (r callRepo) OpenInChat(ctx context.Context, chatID int64) (Call, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	c, err := r.s.q.OpenCallInChat(ctx, &chatID)
	if err != nil {
		return Call{}, mapError(err)
	}
	call, err := r.load(ctx, r.s.q, c)
	return call, mapError(err)
}

func (r callRepo) ForUser(ctx context.Context, userID int64) ([]Call, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	cs, err := r.s.q.ListUserCalls(ctx, userID)
	if err != nil {
		return nil, mapError(err)
	}
	ids := make([]int64, len(cs))
	for i, c := range cs {
		ids[i] = c.ID
	}
	ps, err := r.s.q.ListParticipantsOfCalls(ctx, ids)
	if err != nil {
		return nil, mapError(err)
	}
	byCall := map[int64][]sqlcdb.CallParticipant{}
	for _, p := range ps {
		byCall[p.CallID] = append(byCall[p.CallID], p)
	}
	out := make([]Call, len(cs))
	for i, c := range cs {
		out[i] = callFrom(c, byCall[c.ID])
	}
	return out, nil
}

func sameTime(x, y *time.Time) bool { return (x == nil) == (y == nil) && (x == nil || x.Equal(*y)) }

func sameParticipant(a, b CallParticipant) bool {
	sameSession := (a.SessionID == nil) == (b.SessionID == nil) && (a.SessionID == nil || *a.SessionID == *b.SessionID)
	return a.UserID == b.UserID && a.Position == b.Position && a.State == b.State && a.IdentityKey == b.IdentityKey &&
		sameSession && sameTime(a.JoinedAt, b.JoinedAt) && sameTime(a.LeftAt, b.LeftAt) && sameTime(a.DisconnectedAt, b.DisconnectedAt)
}

func optString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (r callRepo) Update(ctx context.Context, id int64, fn func(c *Call) (bool, error)) (Call, bool, error) {
	return r.update(ctx, id, func(_ context.Context, _ *sqlcdb.Queries, c *Call) (bool, error) { return fn(c) })
}

func (r callRepo) UpdateAsMember(ctx context.Context, id int64, m CallMember, fn func(c *Call, g CallGuard) (bool, error)) (Call, bool, error) {
	return r.update(ctx, id, func(ctx context.Context, q *sqlcdb.Queries, c *Call) (bool, error) {
		if c.ChatID != m.ChatID {
			return false, ErrNotFound
		}
		var g CallGuard
		key, err := q.ShareCallIdentity(ctx, sqlcdb.ShareCallIdentityParams{SessionID: m.SessionID, UserID: m.UserID})
		switch {
		case err == nil:
			g.IdentityKey = key
		case !errors.Is(err, pgx.ErrNoRows):
			return false, err
		}
		if c.Kind == ChatDirect {
			var others []int64
			for _, p := range c.Participants {
				if p.UserID != m.UserID {
					others = append(others, p.UserID)
				}
			}
			if g.Blocked, err = q.BlockedAmong(ctx, sqlcdb.BlockedAmongParams{UserID: m.UserID, Others: others}); err != nil {
				return false, err
			}
		}
		return fn(c, g)
	}, func(ctx context.Context, q *sqlcdb.Queries) error {
		_, err := q.ShareChatMember(ctx, sqlcdb.ShareChatMemberParams{ChatID: m.ChatID, UserID: m.UserID})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
}

func (r callRepo) update(ctx context.Context, id int64, fn func(ctx context.Context, q *sqlcdb.Queries, c *Call) (bool, error), before ...func(ctx context.Context, q *sqlcdb.Queries) error) (Call, bool, error) {
	var (
		out     Call
		changed bool
	)
	err := r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		out, changed = Call{}, false
		for _, b := range before {
			if err := b(ctx, q); err != nil {
				return err
			}
		}
		row, err := q.LockCall(ctx, id)
		if err != nil {
			return err
		}
		orig, err := r.load(ctx, q, row)
		if err != nil {
			return err
		}
		c := orig
		c.Participants = append([]CallParticipant(nil), orig.Participants...)
		ok, err := fn(ctx, q, &c)
		if err != nil {
			return err
		}
		if !ok {
			out = orig
			return nil
		}
		if err := r.save(ctx, q, orig, c); err != nil {
			return err
		}
		out, changed = c, true
		return nil
	})
	if errors.Is(err, errJoinedElsewhere) {
		return Call{}, false, ErrConflict
	}
	if err != nil {
		return Call{}, false, err
	}
	return out, changed, nil
}

func (r callRepo) save(ctx context.Context, q *sqlcdb.Queries, orig, c Call) error {
	if err := q.UpdateCall(ctx, sqlcdb.UpdateCallParams{
		ID: c.ID, State: c.State, EndReason: optString(c.EndReason), Epoch: c.Epoch, Version: c.Version,
		StartedAt: c.StartedAt, EndedAt: c.EndedAt, EmptySince: c.EmptySince,
	}); err != nil {
		return err
	}
	before := make(map[int64]CallParticipant, len(orig.Participants))
	for _, p := range orig.Participants {
		before[p.UserID] = p
	}
	for _, p := range c.Participants {
		if old, ok := before[p.UserID]; ok && sameParticipant(old, p) {
			continue
		}
		err := q.UpsertCallParticipant(ctx, sqlcdb.UpsertCallParticipantParams{
			CallID: c.ID, UserID: p.UserID, Position: int32(min(p.Position, 1<<20)), State: p.State, SessionID: p.SessionID, //nolint:gosec
			IdentityKey: optString(p.IdentityKey), JoinedAt: p.JoinedAt, LeftAt: p.LeftAt, DisconnectedAt: p.DisconnectedAt,
		})
		if isUniqueViolation(err, "call_participants_one_joined") {
			return errJoinedElsewhere
		}
		if err != nil {
			return err
		}
	}
	switch {
	case c.State == CallEnded && orig.State != CallEnded:
		return q.DeleteCallEnvelopes(ctx, c.ID)
	case c.Epoch > orig.Epoch:
		return q.DeleteCallEnvelopesBefore(ctx, sqlcdb.DeleteCallEnvelopesBeforeParams{CallID: c.ID, Epoch: c.Epoch - 1})
	}
	return nil
}

func (r callRepo) JoinedCallOf(ctx context.Context, userID int64) (int64, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	id, err := r.s.q.JoinedCallOfUser(ctx, userID)
	return id, mapError(err)
}

func (r callRepo) CallsOfSessions(ctx context.Context, sessions []uuid.UUID) ([]int64, error) {
	if len(sessions) == 0 {
		return nil, nil
	}
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	ids, err := r.s.q.JoinedCallsOfSessions(ctx, sessions)
	return ids, mapError(err)
}

func (r callRepo) JoinedCount(ctx context.Context) (int, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	n, err := r.s.q.CountJoinedParticipants(ctx)
	return int(n), mapError(err)
}

func (r callRepo) Due(ctx context.Context, now time.Time, grace, staleAfter time.Duration, limit int) ([]int64, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	graceBefore := now.Add(-grace)
	ids, err := r.s.q.DueCalls(ctx, sqlcdb.DueCallsParams{
		StaleBefore: now.Add(-staleAfter), Now: now, GraceBefore: &graceBefore, MaxRows: clampLimit(limit),
	})
	return ids, mapError(err)
}

func (r callRepo) OpenIDs(ctx context.Context, limit int) ([]int64, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	ids, err := r.s.q.OpenCallIDs(ctx, clampLimit(limit))
	return ids, mapError(err)
}

func (r callRepo) ClaimSummary(ctx context.Context, id, messageID int64) (int64, bool, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	got, err := r.s.q.ClaimCallSummary(ctx, sqlcdb.ClaimCallSummaryParams{ID: id, MessageID: messageID})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, mapError(err)
	}
	return got, true, nil
}

func (r callRepo) FinishSummary(ctx context.Context, id int64) error {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	return mapError(r.s.q.FinishCallSummary(ctx, id))
}

func (r callRepo) ReleaseSummary(ctx context.Context, id int64) error {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	return mapError(r.s.q.ReleaseCallSummary(ctx, id))
}

func (r callRepo) PendingSummaries(ctx context.Context, since, until time.Time, limit int) ([]int64, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	ids, err := r.s.q.PendingCallSummaries(ctx, sqlcdb.PendingCallSummariesParams{Since: &since, Until: &until, MaxRows: clampLimit(limit)})
	return ids, mapError(err)
}

func (r callRepo) DeleteOrphans(ctx context.Context) (int64, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	n, err := r.s.q.DeleteOrphanedCalls(ctx)
	return n, mapError(err)
}

func (r callRepo) Purge(ctx context.Context, retention time.Duration) (CallPurge, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	var (
		out CallPurge
		err error
	)
	before := time.Now().Add(-retention)
	if out.Calls, err = r.s.q.PurgeEndedCalls(ctx, &before); err != nil {
		return out, mapError(err)
	}
	if out.StartRequests, err = r.s.q.PurgeCallStartRequests(ctx, time.Now().Add(-24*time.Hour)); err != nil {
		return out, mapError(err)
	}
	if out.Envelopes, err = r.s.q.PurgeEndedCallEnvelopes(ctx); err != nil {
		return out, mapError(err)
	}
	out.Identities, err = r.s.q.PurgeCallIdentities(ctx)
	return out, mapError(err)
}

func (r callRepo) SetIdentity(ctx context.Context, userID int64, sessionID uuid.UUID, publicKey string) (bool, error) {
	var changed bool
	err := r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		changed = false
		old, err := q.LockCallIdentity(ctx, sessionID)
		switch {
		case err == nil && old.UserID != userID:
			return ErrNotFound
		case err == nil && old.PublicKey == publicKey:
			return nil
		case err != nil && !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		n, err := q.UpsertCallIdentity(ctx, sqlcdb.UpsertCallIdentityParams{PublicKey: publicKey, SessionID: sessionID, UserID: userID})
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		changed = true
		return nil
	})
	return changed, err
}

func (r callRepo) Identity(ctx context.Context, sessionID uuid.UUID) (string, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	k, err := r.s.q.GetCallIdentity(ctx, sessionID)
	return k, mapError(err)
}

func (r callRepo) DeleteIdentities(ctx context.Context, sessions []uuid.UUID) error {
	if len(sessions) == 0 {
		return nil
	}
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	return mapError(r.s.q.DeleteCallIdentities(ctx, sessions))
}

func (r callRepo) PutEnvelopes(ctx context.Context, callID int64, check func(Call) error, sender CallKeyEnvelope, recipients []CallKeyEnvelope) (Call, error) {
	var out Call
	err := r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		out = Call{}
		row, err := q.ShareCall(ctx, callID)
		if err != nil {
			return err
		}
		c, err := r.load(ctx, q, row)
		if err != nil {
			return err
		}
		if err := check(c); err != nil {
			return err
		}
		p := sqlcdb.SaveCallEnvelopesParams{
			CallID: callID, Epoch: sender.Epoch, SenderSessionID: sender.SenderSessionID, SenderUserID: sender.SenderUserID,
			Recipients: make([]uuid.UUID, len(recipients)), Encs: make([]string, len(recipients)), Ciphertexts: make([]string, len(recipients)),
		}
		for i, e := range recipients {
			p.Recipients[i], p.Encs[i], p.Ciphertexts[i] = e.RecipientSessionID, e.Enc, e.Ciphertext
		}
		if err := q.SaveCallEnvelopes(ctx, p); err != nil {
			return err
		}
		out = c
		return nil
	})
	return out, err
}

func (r callRepo) Envelopes(ctx context.Context, callID, epoch int64, recipient uuid.UUID) ([]CallKeyEnvelope, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	rows, err := r.s.q.ListCallEnvelopes(ctx, sqlcdb.ListCallEnvelopesParams{CallID: callID, Epoch: epoch, RecipientSessionID: recipient})
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]CallKeyEnvelope, len(rows))
	for i, e := range rows {
		out[i] = CallKeyEnvelope{
			Epoch: e.Epoch, SenderUserID: e.SenderUserID, SenderSessionID: e.SenderSessionID,
			RecipientSessionID: e.RecipientSessionID, Enc: e.Enc, Ciphertext: e.Ciphertext,
		}
	}
	return out, nil
}

func (r callRepo) PushDevices(ctx context.Context, userIDs []int64) ([]CallPushDevice, error) {
	if len(userIDs) == 0 {
		return nil, nil
	}
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	rows, err := r.s.q.ListCallPushDevices(ctx, userIDs)
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]CallPushDevice, len(rows))
	for i, d := range rows {
		out[i] = CallPushDevice{
			NotificationDevice: NotificationDevice{ID: d.ID, Platform: "web", Destination: d.Destination, P256dh: d.P256dh, Auth: d.Auth, SessionID: d.SessionID},
			UserID:             d.UserID, Enabled: d.Enabled, Previews: d.Previews,
		}
	}
	return out, nil
}
