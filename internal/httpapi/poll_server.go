package httpapi

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/myronsi/messenger-back/internal/messages"
	"github.com/myronsi/messenger-back/internal/store/redis"
)

var RateVote = redis.Rate{Name: "poll_vote", Rate: 30, Period: time.Minute, Burst: 15}

func (m *MessageServer) pollTarget(w http.ResponseWriter, r *http.Request, chatID ChatId, messageID MessageId) (int64, int64, int64, bool) {
	p, ok := principalOr401(w, r)
	if !ok {
		return 0, 0, 0, false
	}
	chat, ok := parseID(w, "chat_id", chatID)
	if !ok {
		return 0, 0, 0, false
	}
	msg, ok := parseID(w, "message_id", messageID)
	if !ok {
		return 0, 0, 0, false
	}
	return p.UserID, chat, msg, true
}

func (m *MessageServer) VotePoll(w http.ResponseWriter, r *http.Request, chatID ChatId, messageID MessageId, _ VotePollParams) {
	user, chat, msg, ok := m.pollTarget(w, r, chatID, messageID)
	if !ok {
		return
	}
	var body PollVoteRequest
	if !decode(w, r, &body) {
		return
	}
	if !allow(w, r, m.o.Limiter, RateVote, strconv.FormatInt(user, 10), m.o.Log) {
		return
	}
	view, err := m.o.Service.Vote(r.Context(), user, chat, msg, body.Options)
	if err != nil {
		serviceError(w, r, m.o.Log, "vote", err)
		return
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, PresentPoll(view))
}

func (m *MessageServer) ClosePoll(w http.ResponseWriter, r *http.Request, chatID ChatId, messageID MessageId, _ ClosePollParams) {
	user, chat, msg, ok := m.pollTarget(w, r, chatID, messageID)
	if !ok {
		return
	}
	view, err := m.o.Service.ClosePoll(r.Context(), user, chat, msg)
	if err != nil {
		serviceError(w, r, m.o.Log, "close poll", err)
		return
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, PresentPoll(view))
}

func parseVoterCursor(s string) (*messages.PollVoter, bool) {
	ts, id, found := strings.Cut(s, "_")
	nanos, err1 := strconv.ParseInt(ts, 10, 64)
	user, err2 := strconv.ParseInt(id, 10, 64)
	if !found || err1 != nil || err2 != nil {
		return nil, false
	}
	return &messages.PollVoter{VotedAt: time.Unix(0, nanos), UserID: user}, true
}

func (m *MessageServer) ListPollVoters(w http.ResponseWriter, r *http.Request, chatID ChatId, messageID MessageId, params ListPollVotersParams) {
	user, chat, msg, ok := m.pollTarget(w, r, chatID, messageID)
	if !ok {
		return
	}
	var after *messages.PollVoter
	if params.After != nil {
		if after, ok = parseVoterCursor(*params.After); !ok {
			WriteProblem(w, http.StatusBadRequest, ErrorCodeInvalidRequest, ProblemError{Field: "after", Message: "unknown cursor"})
			return
		}
	}
	limit := 50
	if params.Limit != nil {
		limit = *params.Limit
	}
	voters, err := m.o.Service.PollVoters(r.Context(), user, chat, msg, params.Option, after, limit+1)
	if err != nil {
		serviceError(w, r, m.o.Log, "poll voters", err)
		return
	}
	out := UserPage{Items: []User{}}
	if len(voters) > limit {
		last := voters[limit-1]
		c := strconv.FormatInt(last.VotedAt.UnixNano(), 10) + "_" + strconv.FormatInt(last.UserID, 10)
		out.NextCursor = &c
		voters = voters[:limit]
	}
	ids := make([]int64, len(voters))
	for i, v := range voters {
		ids[i] = v.UserID
	}
	views, err := m.o.Directory.ForViewer(r.Context(), user, ids)
	if err != nil {
		serviceError(w, r, m.o.Log, "render poll voters", err)
		return
	}
	for _, id := range ids {
		if v, found := views[id]; found {
			out.Items = append(out.Items, PresentUser(v))
		}
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, out)
}
