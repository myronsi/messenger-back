package messages

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/myronsi/messenger-back/internal/moderation"
	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/scylla"
)

var (
	ErrPollClosed   = errors.New("poll closed")
	ErrQuizAnswered = errors.New("quiz answered")
)

const (
	MaxPollVoters  = 100
	PollCloseBatch = 100
	PollCloseEvery = 30 * time.Second
)

type PollView struct {
	Poll           postgres.Poll
	MyVotes        []int
	ResultsVisible bool
	CanClose       bool
	Closed         bool
}

func mayCloseAs(role postgres.Role) bool {
	return role == postgres.RoleOwner || role == postgres.RoleAdmin
}

func PollViewFor(p postgres.Poll, viewer int64, votes []int, role postgres.Role, group bool, now time.Time) PollView {
	closed := p.IsClosed(now)
	if closed && p.ClosedAt == nil {
		at := *p.CloseAt
		p.ClosedAt = &at
	}
	if votes == nil {
		votes = []int{}
	}
	creator := p.CreatorID == viewer
	return PollView{
		Poll: p, MyVotes: votes, Closed: closed,
		ResultsVisible: len(votes) > 0 || closed || creator,
		CanClose:       !closed && (creator || group && mayCloseAs(role)),
	}
}

func (s *Service) createPoll(ctx context.Context, m scylla.Message, p PollInput) error {
	err := s.d.Store.Polls().CreatePoll(ctx, postgres.Poll{
		MessageID: m.ID, ChatID: m.ChatID, CreatorID: *m.SenderID, Question: p.Question, Options: p.Options,
		MultipleAnswers: p.MultipleAnswers, Anonymous: p.Anonymous, CorrectOption: p.CorrectOption,
		Explanation: p.Explanation, CloseAt: p.CloseAt,
	})
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return nil
}

func (s *Service) viewerRole(ctx context.Context, chatID, userID int64) (postgres.Role, bool, error) {
	chat, err := s.d.Store.Chats().Get(ctx, chatID)
	if errors.Is(err, postgres.ErrNotFound) {
		return "", false, ErrNotFound
	}
	if err != nil {
		return "", false, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if chat.Type != postgres.ChatGroup {
		return "", false, nil
	}
	p, err := s.d.Store.Chats().Participant(ctx, chatID, userID)
	if errors.Is(err, postgres.ErrNotFound) {
		return "", true, nil
	}
	if err != nil {
		return "", true, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return p.Role, true, nil
}

func (s *Service) PollViews(ctx context.Context, viewer, chatID int64, msgs []scylla.Message) (map[int64]PollView, error) {
	ids := PollIDs(msgs...)
	if len(ids) == 0 {
		return nil, nil
	}
	polls, err := s.d.Store.Polls().Polls(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	votes, err := s.d.Store.Polls().UserVotes(ctx, viewer, ids)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	role, group, err := s.viewerRole(ctx, chatID, viewer)
	if err != nil {
		return nil, err
	}
	now := s.now()
	out := make(map[int64]PollView, len(polls))
	for id, p := range polls {
		out[id] = PollViewFor(p, viewer, votes[id], role, group, now)
	}
	return out, nil
}

func (s *Service) pollMessage(ctx context.Context, userID, chatID, messageID int64) (postgres.Poll, error) {
	if err := s.requireMember(ctx, chatID, userID); err != nil {
		return postgres.Poll{}, err
	}
	m, err := s.message(ctx, chatID, messageID)
	if err != nil {
		return postgres.Poll{}, err
	}
	if m.Deleted || m.Type != scylla.TypePoll {
		return postgres.Poll{}, ErrNotFound
	}
	polls, err := s.d.Store.Polls().Polls(ctx, []int64{messageID})
	if err != nil {
		return postgres.Poll{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	p, ok := polls[messageID]
	if !ok {
		return postgres.Poll{}, ErrNotFound
	}
	return p, nil
}

func validVote(p postgres.Poll, options []int) error {
	if len(options) > len(p.Options) {
		return invalid("options has more items than the poll")
	}
	seen := map[int]bool{}
	for _, o := range options {
		if o < 0 || o >= len(p.Options) {
			return invalid("options must be indexes of the poll's options")
		}
		if seen[o] {
			return invalid("options must not repeat")
		}
		seen[o] = true
	}
	if len(options) > 1 && !p.MultipleAnswers {
		return invalid("this poll takes one answer")
	}
	return nil
}

func (s *Service) Vote(ctx context.Context, userID, chatID, messageID int64, options []int) (PollView, error) {
	p, err := s.pollMessage(ctx, userID, chatID, messageID)
	if err != nil {
		return PollView{}, err
	}
	if err := s.allow(ctx, userID, moderation.Send); err != nil {
		return PollView{}, err
	}
	if err := s.requireNotBlocked(ctx, userID, chatID); err != nil {
		return PollView{}, err
	}
	if err := validVote(p, options); err != nil {
		return PollView{}, err
	}
	updated, changed, err := s.d.Store.Polls().Vote(ctx, messageID, userID, options, s.now().UTC())
	switch {
	case errors.Is(err, postgres.ErrPollClosed):
		return PollView{}, ErrPollClosed
	case errors.Is(err, postgres.ErrQuizAnswered):
		return PollView{}, ErrQuizAnswered
	case errors.Is(err, postgres.ErrNotFound):
		return PollView{}, ErrNotFound
	case err != nil:
		return PollView{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if changed {
		s.pollChanged(ctx, chatID, messageID)
	}
	return s.viewFor(ctx, updated, userID)
}

func (s *Service) viewFor(ctx context.Context, p postgres.Poll, userID int64) (PollView, error) {
	votes, err := s.d.Store.Polls().UserVotes(ctx, userID, []int64{p.MessageID})
	if err != nil {
		return PollView{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	role, group, err := s.viewerRole(ctx, p.ChatID, userID)
	if err != nil {
		return PollView{}, err
	}
	return PollViewFor(p, userID, votes[p.MessageID], role, group, s.now()), nil
}

func (s *Service) ClosePoll(ctx context.Context, userID, chatID, messageID int64) (PollView, error) {
	p, err := s.pollMessage(ctx, userID, chatID, messageID)
	if err != nil {
		return PollView{}, err
	}
	view, err := s.viewFor(ctx, p, userID)
	if err != nil {
		return PollView{}, err
	}
	if view.Closed && p.ClosedAt != nil {
		return view, nil
	}
	if !view.CanClose && !view.Closed {
		return PollView{}, ErrForbidden
	}
	by, at := &userID, s.now().UTC()
	if view.Closed {
		by, at = nil, *p.CloseAt
	}
	closed, changed, err := s.d.Store.Polls().ClosePoll(ctx, messageID, by, at)
	if err != nil {
		return PollView{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if changed {
		s.pollChanged(ctx, chatID, messageID)
	}
	return s.viewFor(ctx, closed, userID)
}

type PollVoter = postgres.PollVoter

func (s *Service) PollVoters(ctx context.Context, userID, chatID, messageID int64, option int, after *PollVoter, limit int) ([]PollVoter, error) {
	p, err := s.pollMessage(ctx, userID, chatID, messageID)
	if err != nil {
		return nil, err
	}
	if option < 0 || option >= len(p.Options) {
		return nil, invalid("option must be the index of an option")
	}
	view, err := s.viewFor(ctx, p, userID)
	if err != nil {
		return nil, err
	}
	if p.Anonymous || !view.ResultsVisible {
		return nil, ErrForbidden
	}
	if limit <= 0 || limit > MaxPollVoters {
		limit = MaxPollVoters
	}
	voters, err := s.d.Store.Polls().PollVoters(ctx, messageID, option, after, limit)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return voters, nil
}

func (s *Service) pollChanged(ctx context.Context, chatID, messageID int64) {
	if members, err := s.Members(ctx, chatID); err == nil {
		s.d.Notifier.PollChanged(ctx, chatID, messageID, members)
	}
	s.recordChange(ctx, postgres.SyncChange{ChatID: chatID, Kind: postgres.SyncPoll, MessageID: messageID})
}

func (s *Service) CloseDuePolls(ctx context.Context) (int, error) {
	closed, err := s.d.Store.Polls().CloseDuePolls(ctx, s.now().UTC(), PollCloseBatch)
	if err != nil {
		return 0, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	for _, p := range closed {
		s.pollChanged(ctx, p.ChatID, p.MessageID)
	}
	return len(closed), nil
}

func (s *Service) RunPollCloser(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for {
				n, err := s.CloseDuePolls(ctx)
				if err != nil {
					s.d.Log.WarnContext(ctx, "close due polls", "error", err)
					break
				}
				if n < PollCloseBatch {
					break
				}
			}
		}
	}
}

func (s *Service) forgetPolls(ctx context.Context, chatID int64, messageIDs []int64) {
	if err := s.d.Store.Polls().DeletePolls(context.WithoutCancel(ctx), chatID, messageIDs); err != nil {
		s.d.Log.WarnContext(ctx, "delete polls", "error", err, "chat_id", chatID)
	}
}

func (s *Service) copyPoll(ctx context.Context, src, dst scylla.Message) error {
	if src.Type != scylla.TypePoll {
		return nil
	}
	polls, err := s.d.Store.Polls().Polls(ctx, []int64{src.ID})
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	p, ok := polls[src.ID]
	if !ok {
		return ErrNotFound
	}
	return s.createPoll(ctx, dst, PollInput{
		Question: p.Question, Options: p.Options, MultipleAnswers: p.MultipleAnswers, Anonymous: p.Anonymous,
		CorrectOption: p.CorrectOption, Explanation: p.Explanation,
	})
}
