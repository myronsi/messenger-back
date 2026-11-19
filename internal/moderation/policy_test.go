package moderation

import (
	"testing"
	"time"

	"github.com/myronsi/messenger-back/internal/store/postgres"
)

var day = 24 * time.Hour

func TestWeightTable(t *testing.T) {
	old := 30 * day
	cases := []struct {
		name string
		in   WeightInput
		want float64
	}{
		{"target contacted first", WeightInput{Context: postgres.ContextTargetInitiated, ReporterAge: old, ReporterStatus: Good}, 1},
		{"only share a group", WeightInput{Context: postgres.ContextSharedChat, ReporterAge: old, ReporterStatus: Good}, 0.5},
		{"no connection", WeightInput{Context: postgres.ContextNone, ReporterAge: old, ReporterStatus: Good}, 0.25},
		{"reporter younger than 24 h", WeightInput{Context: postgres.ContextTargetInitiated, ReporterAge: 23 * time.Hour, ReporterStatus: Good}, 0},
		{"reporter exactly 24 h old", WeightInput{Context: postgres.ContextTargetInitiated, ReporterAge: day, ReporterStatus: Good}, 1},
		{"restricted reporter", WeightInput{Context: postgres.ContextTargetInitiated, ReporterAge: old, ReporterStatus: Restricted}, 0},
		{"suspended reporter", WeightInput{Context: postgres.ContextTargetInitiated, ReporterAge: old, ReporterStatus: Suspended}, 0},
		{"limited reporter counts", WeightInput{Context: postgres.ContextTargetInitiated, ReporterAge: old, ReporterStatus: Limited}, 1},
		{"busy reporter halves full", WeightInput{Context: postgres.ContextTargetInitiated, ReporterAge: old, ReportedUsers: 11}, 0.5},
		{"busy reporter halves shared", WeightInput{Context: postgres.ContextSharedChat, ReporterAge: old, ReportedUsers: 11}, 0.25},
		{"busy reporter halves none", WeightInput{Context: postgres.ContextNone, ReporterAge: old, ReportedUsers: 11}, 0.13},
		{"ten reported users is not busy", WeightInput{Context: postgres.ContextTargetInitiated, ReporterAge: old, ReportedUsers: 10}, 1},
		{"burst of unconnected reports", WeightInput{Context: postgres.ContextNone, ReporterAge: old, Burst: true}, 0},
		{"burst does not touch connected reports", WeightInput{Context: postgres.ContextSharedChat, ReporterAge: old, Burst: true}, 0.5},
	}
	for _, c := range cases {
		if got := Weight(c.in); got != c.want {
			t.Errorf("%s: weight %v, want %v", c.name, got, c.want)
		}
	}
}

func TestWeightBucket(t *testing.T) {
	for w, want := range map[float64]string{0: "zero", 0.13: "low", 0.25: "low", 0.5: "medium", 1: "full"} {
		if got := WeightBucket(w); got != want {
			t.Errorf("bucket(%v) = %s, want %s", w, got, want)
		}
	}
}

func TestDecayBoundaries(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		days int
		want float64
	}{{29, 1}, {30, 0.5}, {90, 0.5}, {91, 0}} {
		got := Score([]postgres.ReportFact{{ReporterID: 1, Weight: 1, CreatedAt: now.Add(-time.Duration(c.days) * day)}}, now)
		if got != c.want {
			t.Errorf("report %d days old counts %v, want %v", c.days, got, c.want)
		}
	}
}

func reportsOn(now time.Time, reporters, days int, weight float64) []postgres.ReportFact {
	var out []postgres.ReportFact
	for i := range reporters {
		out = append(out, postgres.ReportFact{ReporterID: int64(i + 1), Weight: weight, CreatedAt: now.Add(-time.Duration(i%days) * day)})
	}
	return out
}

func TestStatusThresholds(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		reports []postgres.ReportFact
		want    Status
	}{
		{"nothing", nil, Good},
		{"score 2.75", reportsOn(now, 11, 1, 0.25), Good},
		{"score 3", reportsOn(now, 3, 1, 1), Warned},
		{"score 5.5", reportsOn(now, 11, 1, 0.5), Warned},
		{"score 6", reportsOn(now, 6, 1, 1), Limited},
		{"score 10", reportsOn(now, 10, 1, 1), Restricted},
		{"score 15 on 3 days by 15 reporters", reportsOn(now, 15, 3, 1), Suspended},
		{"score 15 on 2 days", reportsOn(now, 15, 2, 1), Restricted},
		{"score 16 by 64 light reporters", reportsOn(now, 64, 3, 0.25), Restricted},
		{"score 15 by 7 heavy and many light reporters", append(reportsOn(now, 7, 3, 1), lightFrom(now, 100, 32)...), Restricted},
		{"score 15 by 8 heavy reporters", append(reportsOn(now, 8, 3, 1), lightFrom(now, 100, 28)...), Suspended},
	}
	for _, c := range cases {
		if got := StatusFor(c.reports, now); got != c.want {
			t.Errorf("%s: %s (score %v), want %s", c.name, got, Score(c.reports, now), c.want)
		}
	}
}

func lightFrom(now time.Time, firstID int64, n int) []postgres.ReportFact {
	out := make([]postgres.ReportFact, n)
	for i := range out {
		out[i] = postgres.ReportFact{ReporterID: firstID + int64(i), Weight: 0.25, CreatedAt: now.Add(-time.Duration(i%3) * day)}
	}
	return out
}

func TestNextFollowsNewReportsUp(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	cur := postgres.ModerationState{Status: string(Good)}
	next := Next(cur, reportsOn(now, 6, 1, 1), now)
	if next.Status != string(Limited) || next.Cause != CauseReports || !next.Since.Equal(now) {
		t.Fatalf("next = %+v, want limited from reports now", next)
	}
	again := Next(next, reportsOn(now, 6, 1, 1), now.Add(time.Hour))
	if again.Status != string(Limited) || !again.Since.Equal(now) {
		t.Fatalf("recomputing without change moved the status: %+v", again)
	}
}

func TestNextFollowsScoreDown(t *testing.T) {
	start := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	reports := reportsOn(start, 6, 1, 1)
	cur := Next(postgres.ModerationState{Status: string(Good)}, reports, start)
	later := start.Add(31 * day)
	next := Next(cur, reports, later)
	if next.Status != string(Warned) {
		t.Fatalf("after the reports halved the status is %s, want warned", next.Status)
	}
}

func TestNextStepsDownAfterQuietMonth(t *testing.T) {
	start := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	reports := append(reportsOn(start, 10, 1, 1), reportsOn(start.Add(20*day), 2, 1, 1)...)
	cur := postgres.ModerationState{Status: string(Limited), Since: start.Add(20 * day)}
	if next := Next(cur, reports, start.Add(49*day)); next.Status != string(Limited) {
		t.Fatalf("29 quiet days already stepped down to %s", next.Status)
	}
	next := Next(cur, reports, start.Add(50*day))
	if next.Status != string(Warned) || next.Cause != CauseQuiet {
		t.Fatalf("30 quiet days: %+v, want warned by quiet", next)
	}
	if again := Next(next, reports, start.Add(51*day)); again.Status != string(Warned) {
		t.Fatalf("the step down was undone without a new report: %s", again.Status)
	}
	fresh := append(reports, postgres.ReportFact{ReporterID: 99, Weight: 1, CreatedAt: start.Add(52 * day)})
	if back := Next(next, fresh, start.Add(52*day)); back.Status != string(Limited) {
		t.Fatalf("a new report after the step down: %s, want limited", back.Status)
	}
}

func TestProbationSucceeds(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	reports := reportsOn(now.Add(-10*day), 6, 1, 1)
	cur := postgres.ModerationState{Status: string(Limited), Since: now.Add(-10 * day)}
	if !CanAppeal(cur, now) {
		t.Fatal("a limited account without earlier appeals cannot appeal")
	}
	appealed := Appeal(cur, now)
	if appealed.Status != string(Warned) || appealed.ProbationUntil == nil || !appealed.ProbationUntil.Equal(now.Add(ProbationLength)) {
		t.Fatalf("appeal: %+v", appealed)
	}
	if CanAppeal(appealed, now.Add(day)) {
		t.Fatal("can appeal again during probation")
	}
	during := Next(appealed, reports, now.Add(7*day))
	if during.Status != string(Warned) || during.ProbationUntil == nil {
		t.Fatalf("during probation: %+v", during)
	}
	after := Next(during, reports, now.Add(14*day))
	if after.Status != string(Warned) || after.ProbationUntil != nil || after.Cause != CauseProbationPassed {
		t.Fatalf("after probation: %+v", after)
	}
	if CanAppeal(after, now.Add(29*day)) || !CanAppeal(after, now.Add(30*day)) {
		t.Fatal("the next appeal must wait 30 days")
	}
}

func TestProbationFails(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	reports := reportsOn(now.Add(-10*day), 6, 1, 1)
	appealed := Appeal(postgres.ModerationState{Status: string(Limited), Since: now.Add(-10 * day)}, now)
	fresh := append(reports, postgres.ReportFact{ReporterID: 50, Weight: 0.5, CreatedAt: now.Add(3 * day)})
	failed := Next(appealed, fresh, now.Add(3*day))
	if failed.Status != string(Limited) || failed.ProbationUntil != nil || failed.Cause != CauseProbationFailed {
		t.Fatalf("failed probation: %+v", failed)
	}
	if failed.AppealBlockedUntil == nil || !failed.AppealBlockedUntil.Equal(now.Add(3*day+AppealBlockAfter)) {
		t.Fatalf("appeals blocked until %v", failed.AppealBlockedUntil)
	}
	if CanAppeal(failed, now.Add(60*day)) || !CanAppeal(failed, now.Add(3*day+AppealBlockAfter)) {
		t.Fatal("appeal block of 90 days is not applied")
	}
	zero := append(reports, postgres.ReportFact{ReporterID: 51, Weight: 0, CreatedAt: now.Add(3 * day)})
	if kept := Next(appealed, zero, now.Add(3*day)); kept.ProbationUntil == nil {
		t.Fatal("a report without weight ended the probation")
	}
}

func TestCannotAppealGood(t *testing.T) {
	if CanAppeal(postgres.ModerationState{Status: string(Good)}, time.Now()) {
		t.Fatal("a good account can appeal")
	}
}

func TestRestrictionsPerStatus(t *testing.T) {
	for st, n := range map[Status]int{Good: 0, Warned: 0, Limited: 3, Restricted: 9, Suspended: 11} {
		if got := len(Restrictions(st)); got != n {
			t.Errorf("%s has %d restrictions, want %d", st, got, n)
		}
	}
}

func facts(at time.Time, n int, weight float64) []postgres.ReportFact {
	out := make([]postgres.ReportFact, n)
	for i := range out {
		out[i] = postgres.ReportFact{ReporterID: int64(i + 1), Weight: weight, CreatedAt: at.Add(time.Duration(i) * time.Minute)}
	}
	return out
}

func TestDecideSetsStaffDecision(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	probation := now.Add(day)
	cur := postgres.ModerationState{Status: "restricted", Since: now.Add(-day), ProbationUntil: &probation, DecidedBy: postgres.DecidedAutomatic}
	got := Decide(cur, facts(now.Add(-time.Hour), 10, 1), Decision{Status: Restricted, ReasonCode: "spam", ReasonText: "Spam links", StaffRef: "case-1"}, now)
	if got.Status != "restricted" || !got.Since.Equal(cur.Since) || got.ProbationUntil != nil || got.Score != 10 {
		t.Fatalf("confirming keeps the level and since, ends probation: %+v", got)
	}
	if got.DecidedBy != postgres.DecidedStaff || got.DecidedAt == nil || !got.DecidedAt.Equal(now) || *got.ReasonCode != "spam" || *got.ReasonText != "Spam links" || *got.StaffRef != "case-1" || got.Cause != CauseManual {
		t.Fatalf("decision fields %+v", got)
	}
	lower := Decide(cur, nil, Decision{Status: Good, ReasonCode: "other", ReasonText: "No violation"}, now)
	if lower.Status != "good" || !lower.Since.Equal(now) || lower.StaffRef != nil {
		t.Fatalf("reversing %+v", lower)
	}
	if CanAppeal(got, now) {
		t.Fatal("a staff decision must not be open to the automatic appeal")
	}
}

func TestNextAfterStaffDecision(t *testing.T) {
	decided := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	code, text := "spam", "Spam"
	cur := postgres.ModerationState{Status: "warned", Since: decided, DecidedBy: postgres.DecidedStaff, DecidedAt: &decided, ReasonCode: &code, ReasonText: &text}
	old := facts(decided.Add(-day), 12, 1)

	later := decided.Add(60 * day)
	if got := Next(cur, nil, later); got.Status != "warned" || got.DecidedBy != postgres.DecidedStaff {
		t.Fatalf("neither decay nor the quiet step lowers a staff decision: %+v", got)
	}
	if got := Next(cur, old, decided.Add(time.Hour)); got.Status != "warned" || got.Score != 12 {
		t.Fatalf("reports before the decision do not raise it: %+v", got)
	}
	newer := append(old, facts(decided.Add(time.Hour), 6, 1)...)
	got := Next(cur, newer, decided.Add(2*time.Hour))
	if got.Status != "limited" || got.Cause != CauseReports || got.DecidedBy != postgres.DecidedAutomatic || got.DecidedAt != nil || got.ReasonCode != nil || got.ReasonText != nil {
		t.Fatalf("new reports after the decision raise it automatically: %+v", got)
	}
}

func TestValidStatus(t *testing.T) {
	for _, s := range []string{"good", "warned", "limited", "restricted", "suspended"} {
		if !ValidStatus(s) {
			t.Errorf("%s is valid", s)
		}
	}
	if ValidStatus("banned") || ValidStatus("") {
		t.Error("unknown statuses are not valid")
	}
}
