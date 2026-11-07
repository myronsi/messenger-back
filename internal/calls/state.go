package calls

import (
	"time"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/store/postgres"
)

const (
	reasonCompleted = "completed"
	reasonMissed    = "missed"
	reasonDeclined  = "declined"
	reasonCancelled = "cancelled"
	reasonFailed    = "failed"
)

type effects struct {
	changed    bool
	visible    bool
	ended      bool
	deleteRoom bool
	remove     []string
	cancel     []int64
}

func (fx *effects) show() { fx.changed, fx.visible = true, true }

func find(c *postgres.Call, userID int64) *postgres.CallParticipant {
	for i := range c.Participants {
		if c.Participants[i].UserID == userID {
			return &c.Participants[i]
		}
	}
	return nil
}

func joinedCount(c *postgres.Call) int {
	n := 0
	for _, p := range c.Participants {
		if p.State == postgres.CallParticipantJoined {
			n++
		}
	}
	return n
}

func joinedSession(p *postgres.CallParticipant, session uuid.UUID) bool {
	return p != nil && p.State == postgres.CallParticipantJoined && p.SessionID != nil && *p.SessionID == session
}

func outcome(c *postgres.Call) string {
	if c.StartedAt != nil {
		return reasonCompleted
	}
	return reasonCancelled
}

func at(t time.Time) *time.Time { return &t }

func setLeft(p *postgres.CallParticipant, now time.Time, fx *effects) {
	if p.SessionID != nil {
		fx.remove = append(fx.remove, Identity(p.UserID, *p.SessionID))
	}
	p.State, p.SessionID, p.IdentityKey, p.LeftAt, p.DisconnectedAt = postgres.CallParticipantLeft, nil, "", at(now), nil
}

func endCall(c *postgres.Call, reason string, now time.Time, fx *effects) {
	if c.State == postgres.CallEnded {
		return
	}
	for i := range c.Participants {
		p := &c.Participants[i]
		switch p.State {
		case postgres.CallParticipantJoined:
			setLeft(p, now, fx)
		case postgres.CallParticipantRinging:
			p.State = postgres.CallParticipantMissed
			fx.cancel = append(fx.cancel, p.UserID)
		}
	}
	c.State, c.EndReason, c.EndedAt, c.EmptySince = postgres.CallEnded, reason, at(now), nil
	fx.show()
	fx.ended, fx.deleteRoom = true, true
}

func leaveParticipant(c *postgres.Call, p *postgres.CallParticipant, now time.Time, fx *effects) {
	setLeft(p, now, fx)
	c.Epoch++
	fx.show()
	switch {
	case c.Kind == postgres.ChatDirect:
		endCall(c, outcome(c), now, fx)
	case c.StartedAt == nil && p.UserID == c.InitiatorID:
		endCall(c, reasonCancelled, now, fx)
	case joinedCount(c) == 0:
		c.EmptySince = at(now)
	}
}

type joinRequest struct {
	userID          int64
	session         uuid.UUID
	identityKey     string
	takeover        bool
	maxParticipants int
}

func join(c *postgres.Call, r joinRequest, now time.Time, fx *effects) error {
	if c.State == postgres.CallEnded {
		return ErrEnded
	}
	p := find(c, r.userID)
	if p != nil && p.State == postgres.CallParticipantJoined {
		if *p.SessionID == r.session {
			if p.IdentityKey != r.identityKey {
				p.IdentityKey = r.identityKey
				c.Epoch++
				fx.show()
			}
			return nil
		}
		if !r.takeover {
			return ErrAlreadyInCall
		}
		fx.remove = append(fx.remove, Identity(p.UserID, *p.SessionID))
		p.SessionID, p.IdentityKey, p.JoinedAt, p.DisconnectedAt = &r.session, r.identityKey, at(now), nil
		c.Epoch++
		fx.show()
		return nil
	}
	if joinedCount(c) >= r.maxParticipants {
		return ErrFull
	}
	if p == nil {
		pos := 0
		for _, q := range c.Participants {
			pos = max(pos, q.Position+1)
		}
		c.Participants = append(c.Participants, postgres.CallParticipant{UserID: r.userID, Position: pos})
		p = &c.Participants[len(c.Participants)-1]
	}
	if p.State == postgres.CallParticipantRinging {
		fx.cancel = append(fx.cancel, r.userID)
	}
	p.State, p.SessionID, p.IdentityKey = postgres.CallParticipantJoined, &r.session, r.identityKey
	p.JoinedAt, p.LeftAt, p.DisconnectedAt = at(now), nil, nil
	if c.State == postgres.CallRinging && r.userID != c.InitiatorID {
		c.State, c.StartedAt = postgres.CallActive, at(now)
	}
	c.EmptySince = nil
	c.Epoch++
	fx.show()
	return nil
}

func leave(c *postgres.Call, userID int64, now time.Time, fx *effects) {
	if c.State == postgres.CallEnded {
		return
	}
	p := find(c, userID)
	switch {
	case p != nil && p.State == postgres.CallParticipantJoined:
		leaveParticipant(c, p, now, fx)
	case p != nil && c.Kind == postgres.ChatDirect:
		endCall(c, outcome(c), now, fx)
	case userID == c.InitiatorID && c.StartedAt == nil:
		endCall(c, reasonCancelled, now, fx)
	}
}

func decline(c *postgres.Call, userID int64, now time.Time, fx *effects) {
	if c.State == postgres.CallEnded {
		return
	}
	p := find(c, userID)
	if p == nil || p.State != postgres.CallParticipantRinging {
		return
	}
	p.State = postgres.CallParticipantDeclined
	fx.cancel = append(fx.cancel, userID)
	fx.show()
	if c.Kind == postgres.ChatDirect {
		endCall(c, reasonDeclined, now, fx)
	}
}

func removeUser(c *postgres.Call, userID int64, now time.Time, fx *effects) {
	if c.State == postgres.CallEnded {
		return
	}
	p := find(c, userID)
	switch {
	case p != nil && p.State == postgres.CallParticipantJoined:
		leaveParticipant(c, p, now, fx)
	case p != nil && p.State == postgres.CallParticipantRinging:
		p.State = postgres.CallParticipantMissed
		fx.cancel = append(fx.cancel, userID)
		fx.show()
	}
	if c.State != postgres.CallEnded && userID == c.InitiatorID && c.StartedAt == nil {
		endCall(c, reasonCancelled, now, fx)
	}
}

func removeSessions(c *postgres.Call, sessions map[uuid.UUID]bool, now time.Time, fx *effects) {
	for _, uid := range joinedUsers(c) {
		p := find(c, uid)
		if c.State != postgres.CallEnded && p != nil && p.State == postgres.CallParticipantJoined && sessions[*p.SessionID] {
			leaveParticipant(c, p, now, fx)
		}
	}
}

func joinedUsers(c *postgres.Call) []int64 {
	var out []int64
	for _, p := range c.Participants {
		if p.State == postgres.CallParticipantJoined {
			out = append(out, p.UserID)
		}
	}
	return out
}

func sweep(c *postgres.Call, now time.Time, grace time.Duration, fx *effects) {
	if c.State == postgres.CallEnded {
		return
	}
	switch {
	case c.ChatID == 0:
		endCall(c, outcome(c), now, fx)
		return
	case now.Sub(c.CreatedAt) >= staleAfter:
		reason := outcome(c)
		if joinedCount(c) > 0 {
			reason = reasonFailed
		}
		endCall(c, reason, now, fx)
		return
	}
	for _, uid := range joinedUsers(c) {
		p := find(c, uid)
		if c.State != postgres.CallEnded && p.State == postgres.CallParticipantJoined && p.DisconnectedAt != nil && now.Sub(*p.DisconnectedAt) >= grace {
			leaveParticipant(c, p, now, fx)
		}
	}
	if c.State != postgres.CallEnded && !now.Before(c.RingExpiresAt) {
		for i := range c.Participants {
			if p := &c.Participants[i]; p.State == postgres.CallParticipantRinging {
				p.State = postgres.CallParticipantMissed
				fx.cancel = append(fx.cancel, p.UserID)
				fx.show()
			}
		}
		if c.StartedAt == nil {
			endCall(c, reasonMissed, now, fx)
		}
	}
	if c.State != postgres.CallEnded && joinedCount(c) == 0 && c.EmptySince != nil && now.Sub(*c.EmptySince) >= grace {
		endCall(c, outcome(c), now, fx)
	}
}

func roomFinished(c *postgres.Call, now time.Time, fx *effects) {
	if c.State == postgres.CallEnded {
		return
	}
	reason := outcome(c)
	for _, p := range c.Participants {
		if p.State == postgres.CallParticipantJoined && p.DisconnectedAt == nil {
			reason = reasonFailed
			break
		}
	}
	endCall(c, reason, now, fx)
}

func mediaJoined(c *postgres.Call, userID int64, session uuid.UUID, fx *effects) {
	p := find(c, userID)
	if c.State == postgres.CallEnded || !joinedSession(p, session) {
		fx.remove = append(fx.remove, Identity(userID, session))
		return
	}
	if p.DisconnectedAt != nil {
		p.DisconnectedAt = nil
		fx.changed = true
	}
}

func mediaLeft(c *postgres.Call, userID int64, session uuid.UUID, now time.Time, fx *effects) {
	p := find(c, userID)
	if c.State == postgres.CallEnded || !joinedSession(p, session) || p.DisconnectedAt != nil {
		return
	}
	p.DisconnectedAt = at(now)
	fx.changed = true
}

func reconcile(c *postgres.Call, present map[string]bool, now time.Time, grace time.Duration, fx *effects) {
	if c.State == postgres.CallEnded {
		return
	}
	known := map[string]bool{}
	for i := range c.Participants {
		p := &c.Participants[i]
		if p.State != postgres.CallParticipantJoined {
			continue
		}
		id := Identity(p.UserID, *p.SessionID)
		known[id] = true
		switch {
		case present[id] && p.DisconnectedAt != nil:
			p.DisconnectedAt = nil
			fx.changed = true
		case !present[id] && p.DisconnectedAt == nil && p.JoinedAt != nil && now.Sub(*p.JoinedAt) >= grace:
			p.DisconnectedAt = at(now)
			fx.changed = true
		}
	}
	for id := range present {
		if !known[id] {
			fx.remove = append(fx.remove, id)
		}
	}
}
