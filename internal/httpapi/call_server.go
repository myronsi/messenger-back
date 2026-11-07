package httpapi

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/myronsi/messenger-back/internal/calls"
	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/redis"
)

var (
	RateCallStart    = redis.Rate{Name: "call_start", Rate: 10, Period: time.Minute, Burst: 10}
	RateCallJoin     = redis.Rate{Name: "call_join", Rate: 30, Period: time.Minute, Burst: 30}
	RateCallKeys     = redis.Rate{Name: "call_keys", Rate: 60, Period: 10 * time.Second, Burst: 60}
	RateCallIdentity = redis.Rate{Name: "call_identity", Rate: 10, Period: time.Minute, Burst: 10}
	RateCallGrant    = redis.Rate{Name: "call_grant", Rate: 30, Period: time.Minute, Burst: 30}
)

type CallOptions struct {
	Service *calls.Service
	Limits  calls.Config
	Limiter RateAllower
	Log     *slog.Logger
}

type CallServer struct{ o CallOptions }

func NewCallServer(o CallOptions) *CallServer {
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	return &CallServer{o: o}
}

func (s *Server) WithCalls(c *CallServer) *Server { s.calls = c; return s }

func PresentCall(c postgres.Call) Call {
	out := Call{
		Id: FormatID(c.ID), ChatId: FormatID(c.ChatID), Kind: CallKind(c.Kind), State: CallState(c.State),
		InitiatorId: FormatID(c.InitiatorID), CreatedAt: utc(c.CreatedAt), StartedAt: utcPtr(c.StartedAt),
		EndedAt: utcPtr(c.EndedAt), RingExpiresAt: utc(c.RingExpiresAt), Epoch: int(c.Epoch), Version: int(c.Version),
		MaxParticipants: c.MaxParticipants, Participants: make([]CallParticipant, 0, len(c.Participants)),
	}
	if c.EndReason != "" {
		r := CallEndReason(c.EndReason)
		out.EndReason = &r
	}
	ps := slices.Clone(c.Participants)
	slices.SortStableFunc(ps, func(a, b postgres.CallParticipant) int {
		switch {
		case a.UserID == c.InitiatorID:
			return -1
		case b.UserID == c.InitiatorID:
			return 1
		}
		return a.Position - b.Position
	})
	for _, p := range ps {
		cp := CallParticipant{UserId: FormatID(p.UserID), State: CallParticipantState(p.State), JoinedAt: utcPtr(p.JoinedAt)}
		if p.State == postgres.CallParticipantJoined && p.SessionID != nil {
			sid, key := *p.SessionID, p.IdentityKey
			cp.SessionId, cp.IdentityKey = &sid, &key
		}
		out.Participants = append(out.Participants, cp)
	}
	return out
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func PresentCallKeys(c postgres.Call, envs []postgres.CallKeyEnvelope) CallKeyEnvelopeList {
	out := CallKeyEnvelopeList{CallId: FormatID(c.ID), Epoch: int(c.Epoch), Items: make([]CallKeyEnvelope, 0, len(envs))}
	for _, e := range envs {
		out.Items = append(out.Items, CallKeyEnvelope{
			Epoch: int(e.Epoch), SenderUserId: FormatID(e.SenderUserID), SenderSessionId: e.SenderSessionID,
			RecipientSessionId: e.RecipientSessionID, Enc: e.Enc, Ciphertext: e.Ciphertext,
		})
	}
	return out
}

func presentGrant(g calls.Grant) CallGrant {
	return CallGrant{Url: g.URL, Token: g.Token, Identity: g.Identity, ExpiresAt: g.ExpiresAt.UTC()}
}

func (c *CallServer) fail(w http.ResponseWriter, r *http.Request, what string, err error) {
	details := func() []ProblemError {
		_, msg, ok := strings.Cut(err.Error(), ": ")
		if !ok || msg == "" {
			return nil
		}
		return []ProblemError{{Message: msg}}
	}
	switch {
	case errors.Is(err, calls.ErrDisabled):
		WriteProblem(w, http.StatusServiceUnavailable, ErrorCodeCallsUnavailable)
	case errors.Is(err, calls.ErrBusy):
		w.Header().Set("Retry-After", "10")
		WriteProblem(w, http.StatusServiceUnavailable, ErrorCodeCallsUnavailable)
	case errors.Is(err, calls.ErrNotFound):
		WriteProblem(w, http.StatusNotFound, ErrorCodeNotFound)
	case errors.Is(err, calls.ErrNotAllowed):
		WriteProblem(w, http.StatusForbidden, ErrorCodeCallsNotAllowed)
	case errors.Is(err, calls.ErrForbidden):
		WriteProblem(w, http.StatusForbidden, ErrorCodeForbidden)
	case errors.Is(err, calls.ErrIdentityRequired):
		WriteProblem(w, http.StatusUnprocessableEntity, ErrorCodeCallIdentityRequired)
	case errors.Is(err, calls.ErrFull):
		WriteProblem(w, http.StatusConflict, ErrorCodeCallFull)
	case errors.Is(err, calls.ErrEnded):
		WriteProblem(w, http.StatusConflict, ErrorCodeCallEnded)
	case errors.Is(err, calls.ErrAlreadyInCall):
		WriteProblem(w, http.StatusConflict, ErrorCodeAlreadyInCall)
	case errors.Is(err, calls.ErrStaleEpoch):
		WriteProblem(w, http.StatusConflict, ErrorCodeStaleEpoch)
	case errors.Is(err, calls.ErrNotJoined):
		WriteProblem(w, http.StatusConflict, ErrorCodeConflict)
	case errors.Is(err, calls.ErrInvalid):
		WriteProblem(w, http.StatusUnprocessableEntity, ErrorCodeValidationFailed, details()...)
	case errors.Is(err, calls.ErrUnavailable):
		c.o.Log.WarnContext(r.Context(), what, "error", err)
		w.Header().Set("Retry-After", "2")
		WriteProblem(w, http.StatusServiceUnavailable, ErrorCodeInternalError)
	default:
		c.o.Log.ErrorContext(r.Context(), what, "error", err)
		WriteProblem(w, http.StatusInternalServerError, ErrorCodeInternalError)
	}
}

func (c *CallServer) enabled(w http.ResponseWriter) bool {
	if c.o.Service == nil {
		WriteProblem(w, http.StatusServiceUnavailable, ErrorCodeCallsUnavailable)
		return false
	}
	return true
}

func (c *CallServer) callID(w http.ResponseWriter, raw string) (int64, bool) {
	return parseID(w, "call_id", raw)
}

func (c *CallServer) GetCallConfig(w http.ResponseWriter, r *http.Request, _ GetCallConfigParams) {
	if _, ok := principalOr401(w, r); !ok {
		return
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, CallConfig{
		Enabled: c.o.Service != nil, MaxParticipants: max(c.o.Limits.MaxParticipants, 2),
		RingTimeoutSeconds: max(int(c.o.Limits.RingTimeout/time.Second), 1),
	})
}

func (c *CallServer) SetCallIdentity(w http.ResponseWriter, r *http.Request, _ SetCallIdentityParams) {
	p, ok := principalOr401(w, r)
	if !ok || !c.enabled(w) {
		return
	}
	var body CallIdentityRequest
	if !decode(w, r, &body) {
		return
	}
	if !allow(w, r, c.o.Limiter, RateCallIdentity, strconv.FormatInt(p.UserID, 10), c.o.Log) {
		return
	}
	if err := c.o.Service.SetIdentity(r.Context(), p.UserID, p.SessionID, body.PublicKey); err != nil {
		if errors.Is(err, calls.ErrInvalid) {
			WriteProblem(w, http.StatusUnprocessableEntity, ErrorCodeValidationFailed, ProblemError{Field: "public_key", Message: "not an uncompressed P-256 public key"})
			return
		}
		if errors.Is(err, calls.ErrNotFound) {
			writeUnauthorized(w, ErrorCodeUnauthenticated)
			return
		}
		c.fail(w, r, "set call identity", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *CallServer) ListMyCalls(w http.ResponseWriter, r *http.Request, _ ListMyCallsParams) {
	p, ok := principalOr401(w, r)
	if !ok || !c.enabled(w) {
		return
	}
	cs, err := c.o.Service.Mine(r.Context(), p.UserID)
	if err != nil {
		c.fail(w, r, "list calls", err)
		return
	}
	out := CallList{Items: make([]Call, len(cs))}
	for i, call := range cs {
		out.Items[i] = PresentCall(call)
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, out)
}

func (c *CallServer) GetChatCall(w http.ResponseWriter, r *http.Request, chatID ChatId, _ GetChatCallParams) {
	p, ok := principalOr401(w, r)
	if !ok || !c.enabled(w) {
		return
	}
	id, ok := parseID(w, "chat_id", chatID)
	if !ok {
		return
	}
	call, err := c.o.Service.ChatCall(r.Context(), p.UserID, id)
	if err != nil {
		c.fail(w, r, "get chat call", err)
		return
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, PresentCall(call))
}

func (c *CallServer) StartChatCall(w http.ResponseWriter, r *http.Request, chatID ChatId, _ StartChatCallParams) {
	p, ok := principalOr401(w, r)
	if !ok || !c.enabled(w) {
		return
	}
	id, ok := parseID(w, "chat_id", chatID)
	if !ok {
		return
	}
	var body StartCallRequest
	if !decode(w, r, &body) {
		return
	}
	if !allow(w, r, c.o.Limiter, RateCallStart, strconv.FormatInt(p.UserID, 10), c.o.Log) {
		return
	}
	call, created, err := c.o.Service.Start(r.Context(), p.UserID, id, body.ClientRequestId)
	if err != nil {
		c.fail(w, r, "start call", err)
		return
	}
	noStore(w)
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSONStatus(w, status, PresentCall(call))
}

func (c *CallServer) GetCall(w http.ResponseWriter, r *http.Request, callID CallId, _ GetCallParams) {
	p, ok := principalOr401(w, r)
	if !ok || !c.enabled(w) {
		return
	}
	id, ok := c.callID(w, callID)
	if !ok {
		return
	}
	call, err := c.o.Service.Get(r.Context(), p.UserID, id)
	if err != nil {
		c.fail(w, r, "get call", err)
		return
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, PresentCall(call))
}

func (c *CallServer) JoinCall(w http.ResponseWriter, r *http.Request, callID CallId, _ JoinCallParams) {
	p, ok := principalOr401(w, r)
	if !ok || !c.enabled(w) {
		return
	}
	id, ok := c.callID(w, callID)
	if !ok {
		return
	}
	var body JoinCallRequest
	if !decodeOptional(w, r, &body) {
		return
	}
	if !allow(w, r, c.o.Limiter, RateCallJoin, strconv.FormatInt(p.UserID, 10), c.o.Log) {
		return
	}
	call, grant, err := c.o.Service.Join(r.Context(), p.UserID, p.SessionID, id, body.Takeover != nil && *body.Takeover)
	if err != nil {
		c.fail(w, r, "join call", err)
		return
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, CallJoin{Call: PresentCall(call), Grant: presentGrant(grant)})
}

func decodeOptional(w http.ResponseWriter, r *http.Request, dst any) bool {
	if r.ContentLength == 0 {
		return true
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeBodyError(w, err)
		return false
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return true
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	return decode(w, r, dst)
}

func (c *CallServer) RenewCallGrant(w http.ResponseWriter, r *http.Request, callID CallId, _ RenewCallGrantParams) {
	p, ok := principalOr401(w, r)
	if !ok || !c.enabled(w) {
		return
	}
	id, ok := c.callID(w, callID)
	if !ok {
		return
	}
	if !allow(w, r, c.o.Limiter, RateCallGrant, strconv.FormatInt(p.UserID, 10), c.o.Log) {
		return
	}
	grant, err := c.o.Service.Grant(r.Context(), p.UserID, p.SessionID, id)
	if err != nil {
		c.fail(w, r, "renew call grant", err)
		return
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, presentGrant(grant))
}

func (c *CallServer) simple(w http.ResponseWriter, r *http.Request, callID, what string, fn func(userID, callID int64) error) {
	p, ok := principalOr401(w, r)
	if !ok || !c.enabled(w) {
		return
	}
	id, ok := c.callID(w, callID)
	if !ok {
		return
	}
	if err := fn(p.UserID, id); err != nil {
		c.fail(w, r, what, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *CallServer) DeclineCall(w http.ResponseWriter, r *http.Request, callID CallId, _ DeclineCallParams) {
	c.simple(w, r, callID, "decline call", func(uid, id int64) error { return c.o.Service.Decline(r.Context(), uid, id) })
}

func (c *CallServer) LeaveCall(w http.ResponseWriter, r *http.Request, callID CallId, _ LeaveCallParams) {
	c.simple(w, r, callID, "leave call", func(uid, id int64) error { return c.o.Service.Leave(r.Context(), uid, id) })
}

func (c *CallServer) EndCall(w http.ResponseWriter, r *http.Request, callID CallId, _ EndCallParams) {
	c.simple(w, r, callID, "end call", func(uid, id int64) error { return c.o.Service.End(r.Context(), uid, id) })
}

func (c *CallServer) GetCallKeys(w http.ResponseWriter, r *http.Request, callID CallId, _ GetCallKeysParams) {
	p, ok := principalOr401(w, r)
	if !ok || !c.enabled(w) {
		return
	}
	id, ok := c.callID(w, callID)
	if !ok {
		return
	}
	call, envs, err := c.o.Service.Keys(r.Context(), p.UserID, p.SessionID, id)
	if err != nil {
		c.fail(w, r, "get call keys", err)
		return
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, PresentCallKeys(call, envs))
}

func (c *CallServer) SendCallKeys(w http.ResponseWriter, r *http.Request, callID CallId, _ SendCallKeysParams) {
	p, ok := principalOr401(w, r)
	if !ok || !c.enabled(w) {
		return
	}
	id, ok := c.callID(w, callID)
	if !ok {
		return
	}
	var body SendCallKeysRequest
	if !decode(w, r, &body) {
		return
	}
	if body.Epoch < 0 {
		WriteProblem(w, http.StatusUnprocessableEntity, ErrorCodeValidationFailed, ProblemError{Field: "epoch", Message: "must not be negative"})
		return
	}
	if !allow(w, r, c.o.Limiter, RateCallKeys, strconv.FormatInt(p.UserID, 10), c.o.Log) {
		return
	}
	envs := make([]calls.OutgoingEnvelope, len(body.Envelopes))
	for i, e := range body.Envelopes {
		envs[i] = calls.OutgoingEnvelope{RecipientSessionID: e.RecipientSessionId, Enc: e.Enc, Ciphertext: e.Ciphertext}
	}
	if err := c.o.Service.SendKeys(r.Context(), p.UserID, p.SessionID, id, int64(body.Epoch), envs); err != nil {
		c.fail(w, r, "send call keys", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
