package moderation

import (
	"math"
	"time"

	"github.com/myronsi/messenger-back/internal/store/postgres"
)

type Status string

const (
	Good       Status = "good"
	Warned     Status = "warned"
	Limited    Status = "limited"
	Restricted Status = "restricted"
	Suspended  Status = "suspended"
)

var levels = []Status{Good, Warned, Limited, Restricted, Suspended}

func (s Status) Level() int {
	for i, l := range levels {
		if l == s {
			return i
		}
	}
	return 0
}

func atLevel(n int) Status {
	return levels[max(0, min(n, len(levels)-1))]
}

func ValidStatus(s string) bool {
	for _, l := range levels {
		if string(l) == s {
			return true
		}
	}
	return false
}

func ParseStatus(s string) Status {
	if st := Status(s); st.Level() > 0 || st == Good {
		return st
	}
	return Good
}

type Action string

const (
	StartDirect   Action = "start_direct"
	InviteToGroup Action = "invite_to_group"
	Call          Action = "call"
	CreateGroup   Action = "create_group"
	Upload        Action = "upload"
	Forward       Action = "forward"
	EditProfile   Action = "edit_profile"
	PublishPack   Action = "publish_pack"
	ManageGroup   Action = "manage_group"
	Send          Action = "send"
	Search        Action = "search"
)

const (
	WarnedAt     = 3.0
	LimitedAt    = 6.0
	RestrictedAt = 10.0
	SuspendedAt  = 15.0

	SuspendReporters  = 8
	SuspendDays       = 3
	SuspendMinWeight  = 0.5
	BurstWindow       = time.Hour
	BurstMax          = 5
	BusyReporterLimit = 10
	BusyWindow        = 30 * 24 * time.Hour
	NewAccountAge     = 24 * time.Hour
	FullWeightAge     = 30 * 24 * time.Hour
	HalfWeightAge     = 90 * 24 * time.Hour
	QuietStepDown     = 30 * 24 * time.Hour
	AppealEvery       = 30 * 24 * time.Hour
	ProbationLength   = 14 * 24 * time.Hour
	AppealBlockAfter  = 90 * 24 * time.Hour
	ReportRetention   = 180 * 24 * time.Hour
	DirectChatsPerDay = 5
)

func Restrictions(s Status) []Action {
	switch s {
	case Limited:
		return []Action{StartDirect, InviteToGroup, Call}
	case Restricted:
		return []Action{StartDirect, InviteToGroup, Call, CreateGroup, Upload, Forward, EditProfile, PublishPack, Search}
	case Suspended:
		return []Action{StartDirect, InviteToGroup, Call, CreateGroup, Upload, Forward, EditProfile, PublishPack, Search, Send, ManageGroup}
	}
	return []Action{}
}

type WeightInput struct {
	Context        string
	ReporterAge    time.Duration
	ReporterStatus Status
	ReportedUsers  int
	Burst          bool
}

func Weight(in WeightInput) float64 {
	var w float64
	switch in.Context {
	case postgres.ContextTargetInitiated:
		w = 1
	case postgres.ContextSharedChat:
		w = 0.5
	default:
		w = 0.25
	}
	if in.ReporterAge < NewAccountAge || in.ReporterStatus.Level() >= Restricted.Level() {
		return 0
	}
	if in.Context == postgres.ContextNone && in.Burst {
		return 0
	}
	if in.ReportedUsers > BusyReporterLimit {
		w /= 2
	}
	return math.Round(w*100) / 100
}

func WeightBucket(w float64) string {
	switch {
	case w <= 0:
		return "zero"
	case w < 0.5:
		return "low"
	case w < 1:
		return "medium"
	}
	return "full"
}

func decay(age time.Duration) float64 {
	switch {
	case age < FullWeightAge:
		return 1
	case age <= HalfWeightAge:
		return 0.5
	}
	return 0
}

func Score(reports []postgres.ReportFact, now time.Time) float64 {
	var sum float64
	for _, r := range reports {
		sum += r.Weight * decay(now.Sub(r.CreatedAt))
	}
	return math.Round(sum*100) / 100
}

func suspendGate(reports []postgres.ReportFact, now time.Time) bool {
	reporters := map[int64]bool{}
	days := map[string]bool{}
	for _, r := range reports {
		if r.Weight <= 0 || decay(now.Sub(r.CreatedAt)) == 0 {
			continue
		}
		days[r.CreatedAt.UTC().Format(time.DateOnly)] = true
		if r.Weight >= SuspendMinWeight {
			reporters[r.ReporterID] = true
		}
	}
	return len(reporters) >= SuspendReporters && len(days) >= SuspendDays
}

func StatusFor(reports []postgres.ReportFact, now time.Time) Status {
	score := Score(reports, now)
	switch {
	case score >= SuspendedAt && suspendGate(reports, now):
		return Suspended
	case score >= RestrictedAt:
		return Restricted
	case score >= LimitedAt:
		return Limited
	case score >= WarnedAt:
		return Warned
	}
	return Good
}

func lastWeighted(reports []postgres.ReportFact) time.Time {
	var last time.Time
	for _, r := range reports {
		if r.Weight > 0 && r.CreatedAt.After(last) {
			last = r.CreatedAt
		}
	}
	return last
}

const (
	CauseReports         = "reports"
	CauseDecay           = "decay"
	CauseQuiet           = "quiet"
	CauseAppeal          = "appeal"
	CauseProbationPassed = "probation_passed"
	CauseProbationFailed = "probation_failed"
	CauseManual          = postgres.CauseManual
	CauseDismissed       = "dismissed"
)

func Next(cur postgres.ModerationState, reports []postgres.ReportFact, now time.Time) postgres.ModerationState {
	if cur.DecidedBy == postgres.DecidedStaff && cur.DecidedAt != nil {
		return nextAfterDecision(cur, reports, now)
	}
	next := cur
	next.Score = Score(reports, now)
	current := ParseStatus(cur.Status)
	target := StatusFor(reports, now)
	last := lastWeighted(reports)
	var status Status
	cause := CauseDecay
	switch {
	case cur.ProbationUntil != nil && cur.LastAppealAt != nil && last.After(*cur.LastAppealAt):
		status = atLevel(max(target.Level(), current.Level()+1))
		next.ProbationUntil = nil
		blocked := now.Add(AppealBlockAfter)
		next.AppealBlockedUntil = &blocked
		cause = CauseProbationFailed
	case cur.ProbationUntil != nil && !now.Before(*cur.ProbationUntil):
		status = atLevel(min(current.Level(), target.Level()))
		next.ProbationUntil = nil
		cause = CauseProbationPassed
	case cur.ProbationUntil != nil:
		status = atLevel(min(current.Level(), target.Level()))
	case last.After(cur.Since) && target.Level() > current.Level():
		status = target
		cause = CauseReports
	default:
		status = atLevel(min(current.Level(), target.Level()))
		quietSince := cur.Since
		if last.After(quietSince) {
			quietSince = last
		}
		if status == current && current != Good && now.Sub(quietSince) >= QuietStepDown {
			status = atLevel(current.Level() - 1)
			cause = CauseQuiet
		}
	}
	next.Status = string(status)
	next.Cause = cause
	if status != current {
		next.Since = now
	}
	return next
}

func nextAfterDecision(cur postgres.ModerationState, reports []postgres.ReportFact, now time.Time) postgres.ModerationState {
	next := cur
	next.Score = Score(reports, now)
	newer := make([]postgres.ReportFact, 0, len(reports))
	for _, r := range reports {
		if r.CreatedAt.After(*cur.DecidedAt) {
			newer = append(newer, r)
		}
	}
	target := StatusFor(newer, now)
	if target.Level() <= ParseStatus(cur.Status).Level() {
		return next
	}
	next.Status = string(target)
	next.Since = now
	next.Cause = CauseReports
	next.DecidedBy = postgres.DecidedAutomatic
	next.DecidedAt = nil
	next.ReasonCode = nil
	next.ReasonText = nil
	return next
}

func Decide(cur postgres.ModerationState, reports []postgres.ReportFact, d Decision, now time.Time) postgres.ModerationState {
	next := cur
	next.Score = Score(reports, now)
	next.Status = string(d.Status)
	if d.Status != ParseStatus(cur.Status) || cur.Since.IsZero() {
		next.Since = now
	}
	at := now
	code, text := d.ReasonCode, d.ReasonText
	next.DecidedBy = postgres.DecidedStaff
	next.DecidedAt = &at
	next.ReasonCode = &code
	next.ReasonText = &text
	next.ProbationUntil = nil
	next.Cause = CauseManual
	next.StaffRef = nil
	if d.StaffRef != "" {
		ref := d.StaffRef
		next.StaffRef = &ref
	}
	return next
}

func CanAppeal(st postgres.ModerationState, now time.Time) bool {
	if ParseStatus(st.Status) == Good || st.ProbationUntil != nil || st.DecidedBy == postgres.DecidedStaff {
		return false
	}
	if st.AppealBlockedUntil != nil && now.Before(*st.AppealBlockedUntil) {
		return false
	}
	return st.LastAppealAt == nil || now.Sub(*st.LastAppealAt) >= AppealEvery
}

func Appeal(cur postgres.ModerationState, now time.Time) postgres.ModerationState {
	next := cur
	until := now.Add(ProbationLength)
	at := now
	next.Status = string(atLevel(ParseStatus(cur.Status).Level() - 1))
	next.ProbationUntil = &until
	next.LastAppealAt = &at
	next.Since = now
	next.Cause = CauseAppeal
	return next
}
