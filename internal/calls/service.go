package calls

import (
	"context"
	"crypto/ecdh"
	"encoding/base64"
	"errors"
	"maps"
	"regexp"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/store/postgres"
)

const (
	identityKeyLength = 87
	maxEnvelopes      = 64
	maxEnc            = 128
	maxCiphertext     = 256
	roomEmptyTimeout  = 120 * time.Second
	roomTimeoutMargin = time.Minute
	publishTimeout    = 5 * time.Second
	mediaCallTimeout  = 5 * time.Second
	roomDeleteTimeout = 10 * time.Second
	removeConcurrency = 4
)

var base64url = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

type Grant struct {
	URL       string
	Token     string
	Identity  string
	ExpiresAt time.Time
}

type OutgoingEnvelope struct {
	RecipientSessionID uuid.UUID
	Enc                string
	Ciphertext         string
}

func (s *Service) apply(ctx context.Context, callID int64, fn func(c *postgres.Call, now time.Time, fx *effects) error) (postgres.Call, error) {
	var fx effects
	c, changed, err := s.d.Store.Calls().Update(ctx, callID, func(c *postgres.Call) (bool, error) {
		fx = effects{}
		if err := fn(c, s.now(), &fx); err != nil {
			return false, err
		}
		if fx.visible {
			c.Version++
		}
		return fx.changed, nil
	})
	if err := storeError(err); err != nil {
		return postgres.Call{}, err
	}
	s.after(ctx, c, fx, changed)
	return c, nil
}

func (s *Service) applyAs(ctx context.Context, callID int64, m postgres.CallMember, fn func(c *postgres.Call, g postgres.CallGuard, now time.Time, fx, left *effects) error) (postgres.Call, error) {
	var fx, lfx effects
	res, err := s.d.Store.Calls().UpdateAsMember(ctx, callID, m, func(c *postgres.Call, g postgres.CallGuard) (bool, error) {
		fx, lfx = effects{}, effects{}
		if err := fn(c, g, s.now(), &fx, &lfx); err != nil {
			return false, err
		}
		if fx.visible {
			c.Version++
		}
		if g.Left != nil && lfx.visible {
			g.Left.Version++
		}
		return fx.changed, nil
	})
	if err := storeError(err); err != nil {
		return postgres.Call{}, err
	}
	if res.LeftChanged {
		s.after(ctx, res.Left, lfx, true)
	}
	s.after(ctx, res.Call, fx, res.Changed)
	return res.Call, nil
}

func storeError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, postgres.ErrNotFound):
		return ErrNotFound
	case errors.Is(err, postgres.ErrConflict):
		return ErrAlreadyInCall
	case isServiceError(err):
		return err
	}
	return unavailable(err)
}

func isServiceError(err error) bool {
	for _, e := range []error{ErrEnded, ErrAlreadyInCall, ErrFull, ErrNotFound, ErrForbidden, ErrNotJoined, ErrStaleEpoch, ErrInvalid, ErrNotAllowed, ErrIdentityRequired} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

func (s *Service) after(ctx context.Context, c postgres.Call, fx effects, changed bool) {
	if changed && fx.visible {
		s.publish(ctx, c)
	}
	if changed && fx.ended {
		s.d.Metrics.CallEnded(c.EndReason)
		if c.ChatID != 0 {
			s.background(ctx, func(ctx context.Context) { s.postSummary(ctx, c.ID) })
		}
	}
	if len(fx.cancel) > 0 && changed {
		users := append([]int64(nil), fx.cancel...)
		reasons := maps.Clone(fx.stopped)
		s.background(ctx, func(ctx context.Context) { s.push(ctx, c, users, actionCancel, reasons) })
	}
	switch {
	case fx.deleteRoom && changed:
		s.background(ctx, func(ctx context.Context) { s.deleteRoom(ctx, c.ID) })
	case len(fx.remove) > 0 && s.d.Media != nil:
		remove := append([]string(nil), fx.remove...)
		s.background(ctx, func(ctx context.Context) { s.removeIdentities(ctx, c.ID, remove) })
	}
}

func (s *Service) deleteRoom(ctx context.Context, callID int64) bool {
	if s.d.Media != nil {
		dctx, cancel := context.WithTimeout(ctx, roomDeleteTimeout)
		err := s.d.Media.DeleteRoom(dctx, RoomName(callID))
		cancel()
		if err != nil {
			s.d.Log.WarnContext(ctx, "delete media room", "call_id", callID, "error", err)
			return false
		}
	}
	if err := s.d.Store.Calls().RoomDeleted(context.WithoutCancel(ctx), callID); err != nil {
		s.d.Log.WarnContext(ctx, "mark media room as deleted", "call_id", callID, "error", err)
		return false
	}
	return true
}

func (s *Service) removeIdentities(ctx context.Context, callID int64, identities []string) {
	room := RoomName(callID)
	slots := make(chan struct{}, removeConcurrency)
	var wg sync.WaitGroup
	defer wg.Wait()
	for _, id := range identities {
		slots <- struct{}{}
		wg.Go(func() {
			defer func() { <-slots }()
			rctx, cancel := context.WithTimeout(ctx, mediaCallTimeout)
			defer cancel()
			if err := s.d.Media.RemoveParticipant(rctx, room, id); err != nil {
				s.d.Log.WarnContext(ctx, "remove call participant from the media room", "call_id", callID, "error", err)
			}
		})
	}
}

func (s *Service) publish(ctx context.Context, c postgres.Call) {
	if c.ChatID == 0 || s.d.Notifier == nil || s.d.Members == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), publishTimeout)
	defer cancel()
	members, err := s.d.Members(ctx, c.ChatID)
	if err != nil {
		s.d.Log.WarnContext(ctx, "call members", "call_id", c.ID, "error", err)
		return
	}
	s.d.Notifier.CallUpdated(ctx, c, members)
}

func (s *Service) visibleCall(ctx context.Context, userID, callID int64) (postgres.Call, postgres.Participant, error) {
	c, err := s.d.Store.Calls().Get(ctx, callID)
	if errors.Is(err, postgres.ErrNotFound) {
		return postgres.Call{}, postgres.Participant{}, ErrNotFound
	}
	if err != nil {
		return postgres.Call{}, postgres.Participant{}, unavailable(err)
	}
	if c.ChatID == 0 {
		return postgres.Call{}, postgres.Participant{}, ErrNotFound
	}
	m, err := s.d.Store.Chats().Participant(ctx, c.ChatID, userID)
	if errors.Is(err, postgres.ErrNotFound) {
		return postgres.Call{}, postgres.Participant{}, ErrNotFound
	}
	if err != nil {
		return postgres.Call{}, postgres.Participant{}, unavailable(err)
	}
	return c, m, nil
}

func (s *Service) Get(ctx context.Context, userID, callID int64) (postgres.Call, error) {
	c, _, err := s.visibleCall(ctx, userID, callID)
	return c, err
}

func (s *Service) ChatCall(ctx context.Context, userID, chatID int64) (postgres.Call, error) {
	if _, err := s.member(ctx, chatID, userID); err != nil {
		return postgres.Call{}, err
	}
	c, err := s.d.Store.Calls().OpenInChat(ctx, chatID)
	if errors.Is(err, postgres.ErrNotFound) {
		return postgres.Call{}, ErrNotFound
	}
	if err != nil {
		return postgres.Call{}, unavailable(err)
	}
	return c, nil
}

func (s *Service) Mine(ctx context.Context, userID int64) ([]postgres.Call, error) {
	cs, err := s.d.Store.Calls().ForUser(ctx, userID)
	if err != nil {
		return nil, unavailable(err)
	}
	return cs, nil
}

func (s *Service) member(ctx context.Context, chatID, userID int64) (postgres.Participant, error) {
	m, err := s.d.Store.Chats().Participant(ctx, chatID, userID)
	if errors.Is(err, postgres.ErrNotFound) {
		return postgres.Participant{}, ErrNotFound
	}
	if err != nil {
		return postgres.Participant{}, unavailable(err)
	}
	return m, nil
}

func (s *Service) Start(ctx context.Context, userID, chatID int64, requestID uuid.UUID) (postgres.Call, bool, error) {
	if _, err := s.member(ctx, chatID, userID); err != nil {
		return postgres.Call{}, false, err
	}
	prev, err := s.d.Store.Calls().StartRequest(ctx, userID, requestID)
	switch {
	case err == nil && prev.Call.ChatID == chatID:
		return prev.Call, prev.Created, nil
	case err == nil:
		return postgres.Call{}, false, invalid("client_request_id was used for another chat")
	case !errors.Is(err, postgres.ErrNotFound):
		return postgres.Call{}, false, unavailable(err)
	}
	chat, err := s.d.Store.Chats().Get(ctx, chatID)
	if errors.Is(err, postgres.ErrNotFound) {
		return postgres.Call{}, false, ErrNotFound
	}
	if err != nil {
		return postgres.Call{}, false, unavailable(err)
	}
	now := s.now()
	st, err := s.d.Store.Calls().Start(ctx, postgres.NewCall{
		ChatID: chatID, Kind: chat.Type, InitiatorID: userID, MaxParticipants: s.cfg.MaxParticipants,
		Ring:          func(others []postgres.CallCandidate) ([]int64, error) { return ringList(chat.Type, others) },
		RingExpiresAt: now.Add(s.cfg.RingTimeout), ClientRequestID: requestID,
	})
	if err := storeError(err); err != nil {
		return postgres.Call{}, false, err
	}
	if st.Created {
		s.d.Metrics.CallStarted()
		s.publish(ctx, st.Call)
		c := st.Call
		var ring []int64
		for _, p := range c.Participants {
			if p.State == postgres.CallParticipantRinging {
				ring = append(ring, p.UserID)
			}
		}
		s.background(ctx, func(ctx context.Context) { s.push(ctx, c, ring, actionRing, nil) })
	}
	return st.Call, st.Created, nil
}

func ringList(kind postgres.ChatType, others []postgres.CallCandidate) ([]int64, error) {
	if kind != postgres.ChatDirect {
		var ring []int64
		for _, o := range others {
			if o.CallsEnabled {
				ring = append(ring, o.UserID)
			}
		}
		return ring, nil
	}
	if len(others) != 1 || others[0].Blocked || !others[0].CallsEnabled {
		return nil, ErrNotAllowed
	}
	return []int64{others[0].UserID}, nil
}

func (s *Service) directAllowed(ctx context.Context, c postgres.Call, userID int64) error {
	if c.Kind != postgres.ChatDirect {
		return nil
	}
	for _, p := range c.Participants {
		if p.UserID == userID {
			continue
		}
		blocked, err := s.d.Store.Social().BlockedEither(ctx, userID, p.UserID)
		if err != nil {
			return unavailable(err)
		}
		if blocked {
			return ErrNotAllowed
		}
	}
	return nil
}

func (s *Service) Join(ctx context.Context, userID int64, session uuid.UUID, callID int64, takeover bool) (postgres.Call, Grant, error) {
	c, g, err := s.join(ctx, userID, session, callID, takeover)
	if err != nil {
		s.d.Metrics.CallJoinFailed(Code(err))
	}
	return c, g, err
}

func (s *Service) join(ctx context.Context, userID int64, session uuid.UUID, callID int64, takeover bool) (postgres.Call, Grant, error) {
	c, _, err := s.visibleCall(ctx, userID, callID)
	if err != nil {
		return postgres.Call{}, Grant{}, err
	}
	if c.State == postgres.CallEnded {
		return postgres.Call{}, Grant{}, ErrEnded
	}
	if err := s.directAllowed(ctx, c, userID); err != nil {
		return postgres.Call{}, Grant{}, err
	}
	_, err = s.d.Store.Calls().Identity(ctx, session)
	if errors.Is(err, postgres.ErrNotFound) {
		return postgres.Call{}, Grant{}, ErrIdentityRequired
	}
	if err != nil {
		return postgres.Call{}, Grant{}, unavailable(err)
	}
	if err := s.ensureRoom(ctx, c); err != nil {
		return postgres.Call{}, Grant{}, err
	}
	other, err := s.d.Store.Calls().JoinedCallOf(ctx, userID)
	switch {
	case errors.Is(err, postgres.ErrNotFound):
		other = 0
	case err != nil:
		return postgres.Call{}, Grant{}, unavailable(err)
	case other == callID:
		other = 0
	case !takeover:
		return postgres.Call{}, Grant{}, ErrAlreadyInCall
	}
	if p := find(&c, userID); p == nil || p.State != postgres.CallParticipantJoined {
		n, err := s.d.Store.Calls().JoinedCount(ctx)
		if err != nil {
			return postgres.Call{}, Grant{}, unavailable(err)
		}
		if other != 0 {
			n--
		}
		if n >= s.cfg.MaxActiveParticipants {
			return postgres.Call{}, Grant{}, ErrBusy
		}
	}
	m := postgres.CallMember{ChatID: c.ChatID, UserID: userID, SessionID: session, Leave: other}
	c, err = s.applyAs(ctx, callID, m, func(c *postgres.Call, g postgres.CallGuard, now time.Time, fx, left *effects) error {
		switch {
		case g.IdentityKey == "":
			return ErrIdentityRequired
		case g.Blocked:
			return ErrNotAllowed
		}
		r := joinRequest{userID: userID, session: session, identityKey: g.IdentityKey, takeover: takeover, maxParticipants: s.cfg.MaxParticipants}
		if err := join(c, r, now, fx); err != nil {
			return err
		}
		if g.Left != nil {
			if p := find(g.Left, userID); p != nil && p.State == postgres.CallParticipantJoined {
				leave(g.Left, userID, now, left)
			}
		}
		return nil
	})
	if err != nil {
		return postgres.Call{}, Grant{}, err
	}
	g, err := s.grant(c, userID, session)
	if err != nil {
		return postgres.Call{}, Grant{}, err
	}
	return c, g, nil
}

func (s *Service) roomTimeouts() (time.Duration, time.Duration) {
	departure := 2*s.cfg.EmptyGrace + roomTimeoutMargin
	return max(roomEmptyTimeout, departure), departure
}

func (s *Service) ensureRoom(ctx context.Context, c postgres.Call) error {
	if s.d.Media == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	empty, departure := s.roomTimeouts()
	if err := s.d.Media.CreateRoom(ctx, RoomName(c.ID), empty, departure, s.cfg.MaxParticipants); err != nil {
		s.d.Log.WarnContext(ctx, "create media room", "call_id", c.ID, "error", err)
		return unavailable(err)
	}
	return nil
}

func (s *Service) grant(c postgres.Call, userID int64, session uuid.UUID) (Grant, error) {
	if s.d.Grants == nil {
		return Grant{}, ErrDisabled
	}
	identity := Identity(userID, session)
	token, exp, err := s.d.Grants.Participant(RoomName(c.ID), identity, s.now())
	if err != nil {
		return Grant{}, unavailable(err)
	}
	return Grant{URL: s.cfg.PublicURL, Token: token, Identity: identity, ExpiresAt: exp}, nil
}

func (s *Service) Grant(ctx context.Context, userID int64, session uuid.UUID, callID int64) (Grant, error) {
	c, _, err := s.visibleCall(ctx, userID, callID)
	if err != nil {
		return Grant{}, err
	}
	if c.State == postgres.CallEnded {
		return Grant{}, ErrEnded
	}
	if !joinedSession(find(&c, userID), session) {
		return Grant{}, ErrNotJoined
	}
	if err := s.ensureRoom(ctx, c); err != nil {
		return Grant{}, err
	}
	return s.grant(c, userID, session)
}

func (s *Service) Decline(ctx context.Context, userID, callID int64) error {
	if _, _, err := s.visibleCall(ctx, userID, callID); err != nil {
		return err
	}
	_, err := s.apply(ctx, callID, func(c *postgres.Call, now time.Time, fx *effects) error {
		decline(c, userID, now, fx)
		return nil
	})
	return err
}

func (s *Service) Leave(ctx context.Context, userID, callID int64) error {
	if _, _, err := s.visibleCall(ctx, userID, callID); err != nil {
		return err
	}
	_, err := s.apply(ctx, callID, func(c *postgres.Call, now time.Time, fx *effects) error {
		leave(c, userID, now, fx)
		return nil
	})
	return err
}

func (s *Service) End(ctx context.Context, userID, callID int64) error {
	c, m, err := s.visibleCall(ctx, userID, callID)
	if err != nil {
		return err
	}
	allowed := c.InitiatorID == userID || c.Kind == postgres.ChatDirect ||
		m.Role == postgres.RoleOwner || m.Role == postgres.RoleAdmin || m.Role == postgres.RoleModerator
	if !allowed {
		return ErrForbidden
	}
	_, err = s.apply(ctx, callID, func(c *postgres.Call, now time.Time, fx *effects) error {
		endCall(c, outcome(c), now, fx)
		return nil
	})
	return err
}

func ValidIdentityKey(key string) bool {
	if len(key) != identityKeyLength || !base64url.MatchString(key) {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(key)
	if err != nil || len(raw) != 65 || raw[0] != 4 {
		return false
	}
	_, err = ecdh.P256().NewPublicKey(raw)
	return err == nil
}

func (s *Service) SetIdentity(ctx context.Context, userID int64, session uuid.UUID, key string) error {
	if !ValidIdentityKey(key) {
		return invalid("public_key must be an uncompressed P-256 point, base64url without padding")
	}
	_, err := s.d.Store.Calls().SetIdentity(ctx, userID, session, key)
	if errors.Is(err, postgres.ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return unavailable(err)
	}
	callID, err := s.d.Store.Calls().JoinedCallOf(ctx, userID)
	if errors.Is(err, postgres.ErrNotFound) {
		return nil
	}
	if err != nil {
		return unavailable(err)
	}
	c, err := s.d.Store.Calls().Get(ctx, callID)
	if errors.Is(err, postgres.ErrNotFound) {
		return nil
	}
	if err != nil {
		return unavailable(err)
	}
	if p := find(&c, userID); !joinedSession(p, session) || p.IdentityKey == key || c.ChatID == 0 {
		return nil
	}
	m := postgres.CallMember{ChatID: c.ChatID, UserID: userID, SessionID: session}
	_, err = s.applyAs(ctx, callID, m, func(c *postgres.Call, g postgres.CallGuard, _ time.Time, fx, _ *effects) error {
		if p := find(c, userID); joinedSession(p, session) && g.IdentityKey != "" && p.IdentityKey != g.IdentityKey {
			p.IdentityKey = g.IdentityKey
			c.Epoch++
			fx.show()
		}
		return nil
	})
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

func validEnvelopes(sender uuid.UUID, envs []OutgoingEnvelope) error {
	if len(envs) == 0 || len(envs) > maxEnvelopes {
		return invalid("envelopes must hold 1 to %d entries", maxEnvelopes)
	}
	seen := map[uuid.UUID]bool{}
	for _, e := range envs {
		switch {
		case e.RecipientSessionID == sender:
			return invalid("an envelope is addressed to the sending session")
		case seen[e.RecipientSessionID]:
			return invalid("two envelopes for one recipient")
		case len(e.Enc) == 0 || len(e.Enc) > maxEnc || !base64url.MatchString(e.Enc):
			return invalid("enc must be base64url, at most %d characters", maxEnc)
		case len(e.Ciphertext) == 0 || len(e.Ciphertext) > maxCiphertext || !base64url.MatchString(e.Ciphertext):
			return invalid("ciphertext must be base64url, at most %d characters", maxCiphertext)
		}
		seen[e.RecipientSessionID] = true
	}
	return nil
}

func (s *Service) SendKeys(ctx context.Context, userID int64, session uuid.UUID, callID, epoch int64, envs []OutgoingEnvelope) error {
	if err := validEnvelopes(session, envs); err != nil {
		return err
	}
	if _, _, err := s.visibleCall(ctx, userID, callID); err != nil {
		return err
	}
	sender := postgres.CallKeyEnvelope{Epoch: epoch, SenderUserID: userID, SenderSessionID: session}
	recipients := make([]postgres.CallKeyEnvelope, len(envs))
	for i, e := range envs {
		recipients[i] = postgres.CallKeyEnvelope{
			Epoch: epoch, SenderUserID: userID, SenderSessionID: session, RecipientSessionID: e.RecipientSessionID,
			Enc: e.Enc, Ciphertext: e.Ciphertext,
		}
	}
	owners := map[uuid.UUID]int64{}
	c, kept, err := s.d.Store.Calls().PutEnvelopes(ctx, callID, func(c postgres.Call, stale map[uuid.UUID]bool) error {
		clear(owners)
		if c.State == postgres.CallEnded {
			return ErrEnded
		}
		if !joinedSession(find(&c, userID), session) || stale[session] {
			return ErrNotJoined
		}
		if epoch != c.Epoch {
			return ErrStaleEpoch
		}
		for _, p := range c.Participants {
			if p.State == postgres.CallParticipantJoined {
				owners[*p.SessionID] = p.UserID
			}
		}
		for _, e := range envs {
			if _, ok := owners[e.RecipientSessionID]; !ok {
				return invalid("recipient_session_id is not on the roster of this epoch")
			}
		}
		return nil
	}, sender, recipients)
	if err := storeError(err); err != nil {
		return err
	}
	if s.d.Notifier != nil {
		for _, e := range kept {
			s.d.Notifier.CallKeys(ctx, c, owners[e.RecipientSessionID], e.RecipientSessionID, []postgres.CallKeyEnvelope{e})
		}
	}
	return nil
}

func (s *Service) Keys(ctx context.Context, userID int64, session uuid.UUID, callID int64) (postgres.Call, []postgres.CallKeyEnvelope, error) {
	c, _, err := s.visibleCall(ctx, userID, callID)
	if err != nil {
		return postgres.Call{}, nil, err
	}
	if c.State == postgres.CallEnded {
		return postgres.Call{}, nil, ErrEnded
	}
	if !joinedSession(find(&c, userID), session) {
		return postgres.Call{}, nil, ErrNotJoined
	}
	envs, err := s.d.Store.Calls().Envelopes(ctx, callID, c.Epoch, session)
	if err != nil {
		return postgres.Call{}, nil, unavailable(err)
	}
	return c, envs, nil
}
