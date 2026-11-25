package postgres

import (
	"context"
	"errors"
	"math"
	"slices"
	"time"

	"github.com/myronsi/messenger-back/internal/store/postgres/sqlcdb"
)

var (
	ErrPollClosed   = errors.New("poll closed")
	ErrQuizAnswered = errors.New("quiz answered")
)

type Poll struct {
	MessageID       int64
	ChatID          int64
	CreatorID       int64
	Question        string
	Options         []string
	MultipleAnswers bool
	Anonymous       bool
	CorrectOption   *int
	Explanation     *string
	CloseAt         *time.Time
	ClosedAt        *time.Time
	ClosedBy        *int64
	Version         int
	CreatedAt       time.Time
	Counts          []int
	Voters          int
}

func (p Poll) Quiz() bool { return p.CorrectOption != nil }

func smallInt(v int) int16 {
	if v < math.MinInt16 || v > math.MaxInt16 {
		return -1
	}
	return int16(v)
}

func rowLimit(v int) int32 {
	if v < 0 || v > math.MaxInt32 {
		return 0
	}
	return int32(v)
}

func (p Poll) IsClosed(now time.Time) bool {
	return p.ClosedAt != nil || (p.CloseAt != nil && !now.Before(*p.CloseAt))
}

type PollVoter struct {
	UserID  int64
	VotedAt time.Time
}

type PollRepository interface {
	CreatePoll(ctx context.Context, p Poll) error
	Polls(ctx context.Context, messageIDs []int64) (map[int64]Poll, error)
	UserVotes(ctx context.Context, userID int64, messageIDs []int64) (map[int64][]int, error)
	AllVotes(ctx context.Context, messageID int64) (map[int64][]int, error)
	Vote(ctx context.Context, messageID, userID int64, options []int, now time.Time) (Poll, bool, error)
	ClosePoll(ctx context.Context, messageID int64, by *int64, now time.Time) (Poll, bool, error)
	CloseDuePolls(ctx context.Context, now time.Time, limit int) ([]Poll, error)
	PollVoters(ctx context.Context, messageID int64, option int, after *PollVoter, limit int) ([]PollVoter, error)
	DeletePolls(ctx context.Context, chatID int64, messageIDs []int64) error
	DeletePollsUpTo(ctx context.Context, chatID, upTo int64) error
}

func (s *Store) Polls() PollRepository { return pollRepo{s} }

type pollRepo struct{ s *Store }

var _ PollRepository = pollRepo{}

func pollFrom(p sqlcdb.Poll) Poll {
	out := Poll{
		MessageID: p.MessageID, ChatID: p.ChatID, CreatorID: p.CreatorID, Question: p.Question, Options: p.Options,
		MultipleAnswers: p.MultipleAnswers, Anonymous: p.Anonymous, Explanation: p.Explanation, CloseAt: p.CloseAt,
		ClosedAt: p.ClosedAt, ClosedBy: p.ClosedBy, Version: int(p.Version), CreatedAt: p.CreatedAt,
		Counts: make([]int, len(p.Options)),
	}
	if p.CorrectOption != nil {
		c := int(*p.CorrectOption)
		out.CorrectOption = &c
	}
	return out
}

func (r pollRepo) CreatePoll(ctx context.Context, p Poll) error {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	params := sqlcdb.InsertPollParams{
		MessageID: p.MessageID, ChatID: p.ChatID, CreatorID: p.CreatorID, Question: p.Question, Options: p.Options,
		MultipleAnswers: p.MultipleAnswers, Anonymous: p.Anonymous, Explanation: p.Explanation, CloseAt: p.CloseAt,
	}
	if p.CorrectOption != nil {
		c := smallInt(*p.CorrectOption)
		params.CorrectOption = &c
	}
	return mapError(r.s.q.InsertPoll(ctx, params))
}

func withCounts(ctx context.Context, q *sqlcdb.Queries, polls map[int64]Poll) error {
	ids := make([]int64, 0, len(polls))
	for id := range polls {
		ids = append(ids, id)
	}
	counts, err := q.PollCounts(ctx, ids)
	if err != nil {
		return err
	}
	for _, c := range counts {
		p := polls[c.MessageID]
		if int(c.Option) < len(p.Counts) {
			p.Counts[c.Option] = int(c.Votes)
		}
	}
	voters, err := q.PollVoterCounts(ctx, ids)
	if err != nil {
		return err
	}
	for _, v := range voters {
		p := polls[v.MessageID]
		p.Voters = int(v.Voters)
		polls[v.MessageID] = p
	}
	return nil
}

func (r pollRepo) Polls(ctx context.Context, messageIDs []int64) (map[int64]Poll, error) {
	out := map[int64]Poll{}
	if len(messageIDs) == 0 {
		return out, nil
	}
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	rows, err := r.s.q.GetPolls(ctx, messageIDs)
	if err != nil {
		return nil, mapError(err)
	}
	for _, p := range rows {
		out[p.MessageID] = pollFrom(p)
	}
	if len(out) == 0 {
		return out, nil
	}
	return out, mapError(withCounts(ctx, r.s.q, out))
}

func (r pollRepo) UserVotes(ctx context.Context, userID int64, messageIDs []int64) (map[int64][]int, error) {
	out := map[int64][]int{}
	if len(messageIDs) == 0 {
		return out, nil
	}
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	rows, err := r.s.q.UserPollVotes(ctx, sqlcdb.UserPollVotesParams{MessageIds: messageIDs, UserID: userID})
	if err != nil {
		return nil, mapError(err)
	}
	for _, v := range rows {
		out[v.MessageID] = append(out[v.MessageID], int(v.Option))
	}
	return out, nil
}

func (r pollRepo) AllVotes(ctx context.Context, messageID int64) (map[int64][]int, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	rows, err := r.s.q.AllPollVotes(ctx, messageID)
	if err != nil {
		return nil, mapError(err)
	}
	out := map[int64][]int{}
	for _, v := range rows {
		out[v.UserID] = append(out[v.UserID], int(v.Option))
	}
	return out, nil
}

func (r pollRepo) Vote(ctx context.Context, messageID, userID int64, options []int, now time.Time) (Poll, bool, error) {
	var poll Poll
	var changed bool
	err := r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		poll, changed = Poll{}, false
		row, err := q.LockPoll(ctx, messageID)
		if err != nil {
			return err
		}
		p := pollFrom(row)
		if p.IsClosed(now) {
			return ErrPollClosed
		}
		current, err := q.UserPollVotes(ctx, sqlcdb.UserPollVotesParams{MessageIds: []int64{messageID}, UserID: userID})
		if err != nil {
			return err
		}
		had := make([]int, 0, len(current))
		for _, v := range current {
			had = append(had, int(v.Option))
		}
		want := slices.Clone(options)
		slices.Sort(want)
		if p.Quiz() && len(had) > 0 {
			if slices.Equal(had, want) {
				poll = p
				return withCounts(ctx, q, map[int64]Poll{messageID: p})
			}
			return ErrQuizAnswered
		}
		if slices.Equal(had, want) {
			polls := map[int64]Poll{messageID: p}
			if err := withCounts(ctx, q, polls); err != nil {
				return err
			}
			poll = polls[messageID]
			return nil
		}
		if _, err := q.DeleteUserPollVotes(ctx, sqlcdb.DeleteUserPollVotesParams{MessageID: messageID, UserID: userID}); err != nil {
			return err
		}
		if len(want) > 0 {
			opts := make([]int16, len(want))
			for i, o := range want {
				opts[i] = smallInt(o)
			}
			if err := q.InsertPollVotes(ctx, sqlcdb.InsertPollVotesParams{MessageID: messageID, UserID: userID, Options: opts, VotedAt: now}); err != nil {
				return err
			}
		}
		bumped, err := q.BumpPollVersion(ctx, messageID)
		if err != nil {
			return err
		}
		polls := map[int64]Poll{messageID: pollFrom(bumped)}
		if err := withCounts(ctx, q, polls); err != nil {
			return err
		}
		poll, changed = polls[messageID], true
		return nil
	})
	if errors.Is(err, ErrPollClosed) || errors.Is(err, ErrQuizAnswered) {
		return Poll{}, false, err
	}
	return poll, changed, mapError(err)
}

func (r pollRepo) ClosePoll(ctx context.Context, messageID int64, by *int64, now time.Time) (Poll, bool, error) {
	var poll Poll
	var closed bool
	err := r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		poll, closed = Poll{}, false
		row, err := q.LockPoll(ctx, messageID)
		if err != nil {
			return err
		}
		if row.ClosedAt == nil {
			if row, err = q.ClosePoll(ctx, sqlcdb.ClosePollParams{MessageID: messageID, ClosedAt: &now, ClosedBy: by}); err != nil {
				return err
			}
			closed = true
		}
		polls := map[int64]Poll{messageID: pollFrom(row)}
		if err := withCounts(ctx, q, polls); err != nil {
			return err
		}
		poll = polls[messageID]
		return nil
	})
	return poll, closed, mapError(err)
}

func (r pollRepo) CloseDuePolls(ctx context.Context, now time.Time, limit int) ([]Poll, error) {
	var out []Poll
	err := r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		out = nil
		rows, err := q.CloseDuePolls(ctx, sqlcdb.CloseDuePollsParams{Now: &now, MaxRows: rowLimit(limit)})
		if err != nil || len(rows) == 0 {
			return err
		}
		polls := map[int64]Poll{}
		for _, p := range rows {
			polls[p.MessageID] = pollFrom(p)
		}
		if err := withCounts(ctx, q, polls); err != nil {
			return err
		}
		for _, p := range rows {
			out = append(out, polls[p.MessageID])
		}
		return nil
	})
	return out, mapError(err)
}

func (r pollRepo) PollVoters(ctx context.Context, messageID int64, option int, after *PollVoter, limit int) ([]PollVoter, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	params := sqlcdb.PollVotersParams{MessageID: messageID, Option: smallInt(option), MaxRows: rowLimit(limit)}
	if after != nil {
		params.AfterVotedAt, params.AfterUserID = &after.VotedAt, &after.UserID
	}
	rows, err := r.s.q.PollVoters(ctx, params)
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]PollVoter, len(rows))
	for i, v := range rows {
		out[i] = PollVoter{UserID: v.UserID, VotedAt: v.VotedAt}
	}
	return out, nil
}

func (r pollRepo) DeletePolls(ctx context.Context, chatID int64, messageIDs []int64) error {
	if len(messageIDs) == 0 {
		return nil
	}
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	return mapError(r.s.q.DeletePolls(ctx, sqlcdb.DeletePollsParams{ChatID: chatID, MessageIds: messageIDs}))
}

func (r pollRepo) DeletePollsUpTo(ctx context.Context, chatID, upTo int64) error {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	return mapError(r.s.q.DeletePollsUpTo(ctx, sqlcdb.DeletePollsUpToParams{ChatID: chatID, UpTo: upTo}))
}
