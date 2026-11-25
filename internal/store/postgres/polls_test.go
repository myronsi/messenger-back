package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestPollVotesCountsAndClosing(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	owner, member := mustUser(t, s, "owner"), mustUser(t, s, "member")
	chat, err := s.Chats().CreateGroup(ctx, NewGroup{OwnerID: owner.ID, Name: "G", MemberIDs: []int64{member.ID}})
	if err != nil {
		t.Fatal(err)
	}
	polls := s.Polls()
	now := time.Now().UTC().Truncate(time.Second)
	due := now.Add(time.Hour)
	if err := polls.CreatePoll(ctx, Poll{MessageID: 10, ChatID: chat.ID, CreatorID: owner.ID, Question: "Q", Options: []string{"A", "B", "C"}, MultipleAnswers: true, Anonymous: true, CloseAt: &due}); err != nil {
		t.Fatal(err)
	}
	if err := polls.CreatePoll(ctx, Poll{MessageID: 10, ChatID: chat.ID, CreatorID: owner.ID, Question: "again", Options: []string{"A", "B"}}); err != nil {
		t.Fatalf("a retried create must be harmless: %v", err)
	}

	var wg sync.WaitGroup
	for i := range 5 {
		wg.Go(func() {
			if _, _, err := polls.Vote(ctx, 10, int64(1000+i), []int{0, 2}, now); err != nil {
				t.Errorf("vote: %v", err)
			}
		})
	}
	wg.Wait()
	p, changed, err := polls.Vote(ctx, 10, member.ID, []int{2}, now)
	if err != nil || !changed {
		t.Fatalf("vote: %v %v", changed, err)
	}
	if p.Question != "Q" || p.Counts[0] != 5 || p.Counts[1] != 0 || p.Counts[2] != 6 || p.Voters != 6 || p.Version != 6 {
		t.Fatalf("counts after votes: %+v", p)
	}
	if _, changed, err := polls.Vote(ctx, 10, member.ID, []int{2}, now); err != nil || changed {
		t.Fatalf("the same vote again changes nothing: %v %v", changed, err)
	}
	mine, err := polls.UserVotes(ctx, member.ID, []int64{10})
	if err != nil || len(mine[10]) != 1 || mine[10][0] != 2 {
		t.Fatalf("own votes: %v %v", mine, err)
	}

	if closed, err := polls.CloseDuePolls(ctx, now, 10); err != nil || len(closed) != 0 {
		t.Fatalf("closed before its time: %v %v", closed, err)
	}
	closed, err := polls.CloseDuePolls(ctx, due.Add(time.Second), 10)
	if err != nil || len(closed) != 1 || closed[0].ClosedAt == nil || !closed[0].ClosedAt.Equal(due) || closed[0].Counts[2] != 6 {
		t.Fatalf("close due polls: %+v %v", closed, err)
	}
	if again, err := polls.CloseDuePolls(ctx, due.Add(time.Minute), 10); err != nil || len(again) != 0 {
		t.Fatalf("closed twice: %v %v", again, err)
	}
	if _, _, err := polls.Vote(ctx, 10, member.ID, []int{0}, now); !errors.Is(err, ErrPollClosed) {
		t.Fatalf("vote in a closed poll: %v", err)
	}
}

func TestQuizAnswersAreFinal(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	owner := mustUser(t, s, "owner")
	chat, err := s.Chats().CreateGroup(ctx, NewGroup{OwnerID: owner.ID, Name: "G"})
	if err != nil {
		t.Fatal(err)
	}
	polls := s.Polls()
	correct := 1
	if err := polls.CreatePoll(ctx, Poll{MessageID: 20, ChatID: chat.ID, CreatorID: owner.ID, Question: "Q", Options: []string{"A", "B"}, CorrectOption: &correct}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if _, _, err := polls.Vote(ctx, 20, 7, []int{0}, now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := polls.Vote(ctx, 20, 7, []int{1}, now); !errors.Is(err, ErrQuizAnswered) {
		t.Fatalf("change a quiz answer: %v", err)
	}
	if _, _, err := polls.Vote(ctx, 20, 7, []int{}, now); !errors.Is(err, ErrQuizAnswered) {
		t.Fatalf("take back a quiz answer: %v", err)
	}
	if err := polls.DeletePollsUpTo(ctx, chat.ID, 20); err != nil {
		t.Fatal(err)
	}
	if found, err := polls.Polls(ctx, []int64{20}); err != nil || len(found) != 0 {
		t.Fatalf("polls after clearing: %v %v", found, err)
	}
}
