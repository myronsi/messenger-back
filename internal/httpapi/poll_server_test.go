package httpapi

import (
	"net/http"
	"testing"
	"time"
)

func (e *chatEnv) sendPoll(t *testing.T, s session, chatID, temp string, poll map[string]any) reply {
	t.Helper()
	return e.do(t, request{method: http.MethodPost, path: "/chats/" + chatID + "/messages", token: s.access,
		body: map[string]any{"client_temp_id": temp, "type": "poll", "poll": poll}})
}

func (e *chatEnv) vote(t *testing.T, s session, chatID, messageID string, options ...int) reply {
	t.Helper()
	if options == nil {
		options = []int{}
	}
	return e.do(t, request{method: http.MethodPut, path: "/chats/" + chatID + "/messages/" + messageID + "/poll/votes", token: s.access,
		body: map[string]any{"options": options}})
}

func pollOf(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	p, ok := m["poll"].(map[string]any)
	if !ok {
		t.Fatalf("message without poll: %v", m)
	}
	return p
}

func counts(p map[string]any) []any {
	out := []any{}
	for _, o := range p["options"].([]any) {
		out = append(out, o.(map[string]any)["voter_count"])
	}
	return out
}

func lastMessage(t *testing.T, e *chatEnv, s session, chatID string) map[string]any {
	t.Helper()
	all, _ := e.history(t, s, chatID, "")
	return all[len(all)-1]
}

func TestPollVotesAndResults(t *testing.T) {
	e := newChatEnv(t)
	alice, bob, carol := e.register(t, "alice"), e.register(t, "bob"), e.register(t, "carol")
	gid := e.group(t, alice, "Team", bob, carol)

	sent := e.sendPoll(t, alice, gid, "p-1", map[string]any{"question": " Where do we meet? ", "options": []string{"Café", "Park", "Office"}})
	if sent.Code != http.StatusCreated {
		t.Fatalf("send poll: %d %s", sent.Code, sent.Body)
	}
	msg := sent.json()
	id := msg["id"].(string)
	if msg["type"] != "poll" || msg["content"] != "📊 Where do we meet?" {
		t.Fatalf("poll message: %v", msg)
	}
	if p := pollOf(t, msg); p["question"] != "Where do we meet?" || p["anonymous"] != true || p["total_voters"] != float64(0) || p["can_close"] != true {
		t.Fatalf("creator's poll: %v", p)
	}
	if p := pollOf(t, lastMessage(t, e, bob, gid)); p["total_voters"] != nil || counts(p)[0] != nil || p["can_close"] != false {
		t.Fatalf("results before voting: %v", p)
	}

	if r := e.vote(t, bob, gid, id, 0, 1); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("two answers in a single-answer poll: %d %s", r.Code, r.Body)
	}
	if r := e.vote(t, bob, gid, id, 3); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing option: %d %s", r.Code, r.Body)
	}
	voted := e.vote(t, bob, gid, id, 1)
	if voted.Code != http.StatusOK {
		t.Fatalf("vote: %d %s", voted.Code, voted.Body)
	}
	if p := voted.json(); p["total_voters"] != float64(1) || counts(p)[1] != float64(1) || len(p["my_votes"].([]any)) != 1 {
		t.Fatalf("after voting: %v", p)
	}
	if p := pollOf(t, lastMessage(t, e, carol, gid)); p["total_voters"] != nil {
		t.Fatalf("carol sees results before voting: %v", p)
	}
	e.vote(t, carol, gid, id, 1)
	if r := e.vote(t, bob, gid, id); r.Code != http.StatusOK || r.json()["total_voters"] != nil || len(r.json()["my_votes"].([]any)) != 0 {
		t.Fatalf("take back vote: %d %s", r.Code, r.Body)
	}
	if r := e.do(t, request{method: http.MethodGet, path: "/chats/" + gid + "/messages/" + id + "/poll/voters?option=1", token: carol.access}); r.Code != http.StatusForbidden {
		t.Fatalf("voters of an anonymous poll: %d %s", r.Code, r.Body)
	}

	closePath := "/chats/" + gid + "/messages/" + id + "/poll/close"
	if r := e.do(t, request{method: http.MethodPost, path: closePath, token: bob.access}); r.Code != http.StatusForbidden {
		t.Fatalf("member closes: %d %s", r.Code, r.Body)
	}
	closed := e.do(t, request{method: http.MethodPost, path: closePath, token: alice.access})
	if closed.Code != http.StatusOK || closed.json()["closed_at"] == nil || closed.json()["can_close"] != false {
		t.Fatalf("close: %d %s", closed.Code, closed.Body)
	}
	if r := e.vote(t, bob, gid, id, 0); r.Code != http.StatusConflict || r.json()["code"] != "poll_closed" {
		t.Fatalf("vote in a closed poll: %d %s", r.Code, r.Body)
	}
	if p := pollOf(t, lastMessage(t, e, bob, gid)); p["total_voters"] != float64(1) || counts(p)[1] != float64(1) {
		t.Fatalf("results after closing: %v", p)
	}
}

func TestQuizAndPublicVoters(t *testing.T) {
	e := newChatEnv(t)
	alice, bob := e.register(t, "alice"), e.register(t, "bob")
	gid := e.group(t, alice, "Team", bob)

	bad := []map[string]any{
		{"question": "Q", "options": []string{"A", "a"}},
		{"question": "Q", "options": []string{"A", "B"}, "multiple_answers": true, "quiz": map[string]any{"correct_option": 0}},
		{"question": "Q", "options": []string{"A", "B"}, "quiz": map[string]any{"correct_option": 2}},
		{"question": "Q", "options": []string{"A", "B"}, "close_at": time.Now().Add(time.Minute).UTC().Format(time.RFC3339)},
	}
	for i, p := range bad {
		if r := e.sendPoll(t, alice, gid, "bad-"+string(rune('a'+i)), p); r.Code != http.StatusUnprocessableEntity {
			t.Fatalf("invalid poll %v: %d %s", p, r.Code, r.Body)
		}
	}

	quiz := e.sendPoll(t, alice, gid, "q-1", map[string]any{
		"question": "2 + 2?", "options": []string{"4", "5"}, "anonymous": false,
		"quiz": map[string]any{"correct_option": 0, "explanation": "Basic math"},
	})
	if quiz.Code != http.StatusCreated {
		t.Fatalf("send quiz: %d %s", quiz.Code, quiz.Body)
	}
	id := quiz.json()["id"].(string)
	before := pollOf(t, lastMessage(t, e, bob, gid))
	if before["quiz"] != true || before["explanation"] != nil || before["options"].([]any)[0].(map[string]any)["is_correct"] != nil {
		t.Fatalf("quiz answer leaks before voting: %v", before)
	}
	answered := e.vote(t, bob, gid, id, 1)
	if answered.Code != http.StatusOK {
		t.Fatalf("answer: %d %s", answered.Code, answered.Body)
	}
	if p := answered.json(); p["explanation"] != "Basic math" || p["options"].([]any)[0].(map[string]any)["is_correct"] != true {
		t.Fatalf("quiz after answering: %v", p)
	}
	if r := e.vote(t, bob, gid, id, 0); r.Code != http.StatusConflict || r.json()["code"] != "quiz_answered" {
		t.Fatalf("change a quiz answer: %d %s", r.Code, r.Body)
	}
	voters := e.do(t, request{method: http.MethodGet, path: "/chats/" + gid + "/messages/" + id + "/poll/voters?option=1", token: alice.access})
	if list := items(t, voters); len(list) != 1 || list[0]["username"] != "bob" {
		t.Fatalf("voters: %v", list)
	}
}

func TestPollForwardAndDelete(t *testing.T) {
	e := newChatEnv(t)
	alice, bob := e.register(t, "alice"), e.register(t, "bob")
	gid := e.group(t, alice, "Team", bob)
	other := e.group(t, alice, "Other", bob)
	id := e.sendPoll(t, alice, gid, "p-1", map[string]any{"question": "Lunch?", "options": []string{"Yes", "No"}}).json()["id"].(string)
	e.vote(t, bob, gid, id, 0)

	fw := e.do(t, request{method: http.MethodPost, path: "/chats/" + gid + "/messages/forward", token: bob.access,
		body: map[string]any{"message_ids": []string{id}, "chat_ids": []string{other}}})
	if fw.Code != http.StatusCreated {
		t.Fatalf("forward: %d %s", fw.Code, fw.Body)
	}
	copied := lastMessage(t, e, bob, other)
	if p := pollOf(t, copied); p["question"] != "Lunch?" || p["total_voters"] != float64(0) || len(p["my_votes"].([]any)) != 0 {
		t.Fatalf("forwarded poll keeps votes: %v", p)
	}

	del := e.do(t, request{method: http.MethodPost, path: "/chats/" + gid + "/messages/delete", token: alice.access,
		body: map[string]any{"message_ids": []string{id}, "scope": "everyone"}})
	if del.Code != http.StatusOK && del.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", del.Code, del.Body)
	}
	if r := e.vote(t, bob, gid, id, 1); r.Code != http.StatusNotFound {
		t.Fatalf("vote in a deleted poll: %d %s", r.Code, r.Body)
	}
	if m := lastMessage(t, e, bob, gid); m["poll"] != nil {
		t.Fatalf("deleted poll still shown: %v", m)
	}
}
