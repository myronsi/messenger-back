package httpapi

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/myronsi/messenger-back/internal/moderation"
	"github.com/myronsi/messenger-back/internal/store/postgres"
)

func (e *chatEnv) setStatus(t *testing.T, id string, st moderation.Status) {
	t.Helper()
	uid := mustID(t, id)
	now := time.Now()
	_, _, err := e.store.Moderation().Transition(context.Background(), uid, now, now, func(cur postgres.ModerationState, _ []postgres.ReportFact) postgres.ModerationState {
		cur.Status, cur.Since, cur.Cause = string(st), now, "test"
		return cur
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.modCache.Invalidate(context.Background(), uid); err != nil {
		t.Fatal(err)
	}
}

func restrictedBy(t *testing.T, r reply, action string) {
	t.Helper()
	if r.Code != http.StatusForbidden {
		t.Fatalf("want 403 account_restricted, got %d %s", r.Code, r.Body)
	}
	body := r.json()
	if body["code"] != "account_restricted" || body["restriction"] != action {
		t.Fatalf("problem %v, want account_restricted with %s", body, action)
	}
}

func TestReportUserEndpoints(t *testing.T) {
	e := newChatEnv(t)
	alice, bob := e.register(t, "alice"), e.register(t, "bob")
	aliceID, bobID := e.uid(t, alice), e.uid(t, bob)
	report := func(s session, id string, body any) reply {
		return e.do(t, request{method: http.MethodPost, path: "/users/" + id + "/report", token: s.access, body: body})
	}
	if r := report(alice, bobID, map[string]any{"reason": "spam", "note": "links", "block": false}); r.Code != http.StatusNoContent {
		t.Fatalf("report: %d %s", r.Code, r.Body)
	}
	if blocked, _ := e.store.Social().BlockedEither(context.Background(), mustID(t, aliceID), mustID(t, bobID)); blocked {
		t.Fatal("block false blocked")
	}
	if r := report(alice, bobID, map[string]any{"reason": "harassment"}); r.Code != http.StatusNoContent {
		t.Fatalf("report again: %d %s", r.Code, r.Body)
	}
	if blocked, _ := e.store.Social().BlockedEither(context.Background(), mustID(t, aliceID), mustID(t, bobID)); !blocked {
		t.Fatal("block defaults to true")
	}
	if r := report(alice, aliceID, map[string]any{"reason": "spam"}); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("self report: %d %s", r.Code, r.Body)
	}
	if r := report(alice, bobID, map[string]any{"reason": "spam", "note": strings.Repeat("n", 501)}); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("long note: %d %s", r.Code, r.Body)
	}
	if r := report(alice, "999999999", map[string]any{"reason": "spam"}); r.Code != http.StatusNotFound {
		t.Fatalf("unknown user: %d %s", r.Code, r.Body)
	}
	if r := e.do(t, request{method: http.MethodDelete, path: "/users/" + bobID + "/report", token: alice.access}); r.Code != http.StatusNoContent {
		t.Fatalf("withdraw: %d %s", r.Code, r.Body)
	}
}

func TestAccountStatusAndAppeal(t *testing.T) {
	e := newChatEnv(t)
	alice := e.register(t, "alice")
	status := func() map[string]any {
		r := e.do(t, request{method: http.MethodGet, path: "/me/account-status", token: alice.access})
		if r.Code != http.StatusOK {
			t.Fatalf("status: %d %s", r.Code, r.Body)
		}
		return r.json()
	}
	if s := status(); s["status"] != "good" || s["since"] != nil || s["can_appeal"] != false || len(s["restrictions"].([]any)) != 0 {
		t.Fatalf("new account %v", s)
	}
	appeal := func() reply {
		return e.do(t, request{method: http.MethodPost, path: "/me/account-status/appeal", token: alice.access})
	}
	if r := appeal(); r.Code != http.StatusConflict {
		t.Fatalf("appeal of a good account: %d %s", r.Code, r.Body)
	}
	e.setStatus(t, e.uid(t, alice), moderation.Limited)
	if s := status(); s["status"] != "limited" || s["can_appeal"] != true || len(s["restrictions"].([]any)) != 3 {
		t.Fatalf("limited %v", s)
	}
	r := appeal()
	if r.Code != http.StatusOK || r.json()["status"] != "warned" || r.json()["probation_until"] == nil {
		t.Fatalf("appeal: %d %s", r.Code, r.Body)
	}
	if r := appeal(); r.Code != http.StatusConflict {
		t.Fatalf("second appeal: %d %s", r.Code, r.Body)
	}
}

func TestRestrictedActions(t *testing.T) {
	e := newChatEnv(t)
	alice, bob, carol := e.register(t, "alice"), e.register(t, "bob"), e.register(t, "carol")
	aliceID, bobID, carolID := e.uid(t, alice), e.uid(t, bob), e.uid(t, carol)
	chat := e.create(t, bob, aliceID, "hi")
	if chat.Code != http.StatusCreated && chat.Code != http.StatusOK {
		t.Fatalf("bob opens a chat: %d %s", chat.Code, chat.Body)
	}
	chatID := chat.json()["id"].(string)
	group := e.do(t, request{method: http.MethodPost, path: "/groups", token: alice.access, body: map[string]any{"name": "Mine", "member_ids": []string{bobID}}})
	if group.Code != http.StatusCreated {
		t.Fatalf("alice creates a group: %d %s", group.Code, group.Body)
	}
	groupID := group.json()["id"].(string)

	e.setStatus(t, aliceID, moderation.Limited)
	restrictedBy(t, e.create(t, alice, carolID, ""), "start_direct")
	if r := e.create(t, alice, bobID, ""); r.Code != http.StatusOK && r.Code != http.StatusCreated {
		t.Fatalf("limited opens the chat with a contact: %d %s", r.Code, r.Body)
	}
	restrictedBy(t, e.do(t, request{method: http.MethodPost, path: "/groups", token: alice.access, body: map[string]any{"name": "G", "member_ids": []string{carolID}}}), "invite_to_group")
	if r := e.do(t, request{method: http.MethodPatch, path: "/me", token: alice.access, body: map[string]any{"display_name": "New"}}); r.Code != http.StatusOK {
		t.Fatalf("limited renames: %d %s", r.Code, r.Body)
	}

	e.setStatus(t, aliceID, moderation.Restricted)
	restrictedBy(t, e.do(t, request{method: http.MethodPost, path: "/groups", token: alice.access, body: map[string]any{"name": "G", "member_ids": []string{bobID}}}), "create_group")
	restrictedBy(t, e.do(t, request{method: http.MethodPatch, path: "/me", token: alice.access, body: map[string]any{"display_name": "Other"}}), "edit_profile")
	if r := e.do(t, request{method: http.MethodPatch, path: "/me", token: alice.access, body: map[string]any{"bio": "still mine"}}); r.Code != http.StatusOK {
		t.Fatalf("restricted edits the bio: %d %s", r.Code, r.Body)
	}
	restrictedBy(t, e.do(t, request{method: http.MethodPost, path: "/stickers/packs", token: alice.access, body: map[string]any{"title": "Mine"}}), "publish_pack")
	search := e.do(t, request{method: http.MethodGet, path: "/users?q=alice", token: bob.access})
	if search.Code != http.StatusOK || len(search.json()["items"].([]any)) != 0 {
		t.Fatalf("restricted account found by search: %d %s", search.Code, search.Body)
	}
	if own := e.do(t, request{method: http.MethodGet, path: "/users?q=alice", token: alice.access}); len(own.json()["items"].([]any)) != 1 {
		t.Fatalf("restricted account hidden from itself: %s", own.Body)
	}
	send := map[string]any{"client_temp_id": "t1", "type": "text", "content": "still here"}
	if r := e.do(t, request{method: http.MethodPost, path: "/chats/" + chatID + "/messages", token: alice.access, body: send}); r.Code != http.StatusCreated && r.Code != http.StatusOK {
		t.Fatalf("restricted sends: %d %s", r.Code, r.Body)
	}
	if profile := e.do(t, request{method: http.MethodGet, path: "/users/" + aliceID, token: bob.access}); profile.json()["account_label"] != nil {
		t.Fatalf("restricted account labelled: %s", profile.Body)
	}

	e.setStatus(t, aliceID, moderation.Suspended)
	send["client_temp_id"] = "t2"
	restrictedBy(t, e.do(t, request{method: http.MethodPost, path: "/chats/" + chatID + "/messages", token: alice.access, body: send}), "send")
	if r := e.do(t, request{method: http.MethodGet, path: "/chats", token: alice.access}); r.Code != http.StatusOK {
		t.Fatalf("suspended reads the chat list: %d %s", r.Code, r.Body)
	}
	restrictedBy(t, e.do(t, request{method: http.MethodPatch, path: "/groups/" + groupID, token: alice.access, body: map[string]any{"name": "Renamed"}}), "manage_group")
	restrictedBy(t, e.do(t, request{method: http.MethodDelete, path: "/groups/" + groupID + "/participants/" + bobID, token: alice.access}), "manage_group")
	if r := e.do(t, request{method: http.MethodGet, path: "/me/account-status", token: alice.access}); r.Code != http.StatusOK || r.json()["status"] != "suspended" {
		t.Fatalf("suspended status page: %d %s", r.Code, r.Body)
	}
	if profile := e.do(t, request{method: http.MethodGet, path: "/users/" + aliceID, token: bob.access}); profile.json()["account_label"] != "restricted" {
		t.Fatalf("suspended account not labelled: %s", profile.Body)
	}
	chats := items(t, e.do(t, request{method: http.MethodGet, path: "/chats", token: bob.access}))
	if len(chats) == 0 || chats[0]["peer"].(map[string]any)["account_label"] != "restricted" {
		t.Fatalf("chat entry without the label: %v", chats)
	}
}

func TestLimitedChatsLandInRequests(t *testing.T) {
	e := newChatEnv(t)
	alice, dave, carol := e.register(t, "alice"), e.register(t, "dave"), e.register(t, "carol")
	aliceID, daveID, carolID := e.uid(t, alice), e.uid(t, dave), e.uid(t, carol)
	if _, err := e.store.Chats().CreateGroup(context.Background(), postgres.NewGroup{OwnerID: mustID(t, carolID), Name: "Club", MemberIDs: []int64{mustID(t, aliceID), mustID(t, daveID)}}); err != nil {
		t.Fatal(err)
	}
	folder := func(s session, query string) []string {
		t.Helper()
		out := []string{}
		for _, c := range items(t, e.do(t, request{method: http.MethodGet, path: "/chats" + query, token: s.access})) {
			out = append(out, c["id"].(string))
		}
		return out
	}
	e.setStatus(t, aliceID, moderation.Limited)
	limited := e.create(t, alice, carolID, "hello")
	if limited.Code != http.StatusCreated || limited.json()["is_request"] != false {
		t.Fatalf("limited opens a chat with a contact: %d %s", limited.Code, limited.Body)
	}
	limitedChat := limited.json()["id"].(string)
	good := e.create(t, dave, carolID, "hello")
	if good.Code != http.StatusCreated {
		t.Fatalf("good opens a chat with a contact: %d %s", good.Code, good.Body)
	}
	goodChat := good.json()["id"].(string)
	requests, main := folder(carol, "?folder=requests"), folder(carol, "")
	if !slices.Contains(requests, limitedChat) || slices.Contains(main, limitedChat) {
		t.Fatalf("the limited account's chat is not in the requests folder: requests %v, main %v", requests, main)
	}
	if slices.Contains(requests, goodChat) || !slices.Contains(main, goodChat) {
		t.Fatalf("a contact's chat landed in requests: requests %v, main %v", requests, main)
	}
}

func TestStaffDecisionAndReportOutcomes(t *testing.T) {
	e := newChatEnv(t)
	ctx := context.Background()
	alice, bob := e.register(t, "alice"), e.register(t, "bob")
	bobID := e.uid(t, bob)
	svc := moderation.New(moderation.Deps{Store: e.store, Cache: e.modCache})

	r := e.do(t, request{method: http.MethodGet, path: "/me/account-status", token: bob.access})
	if s := r.json(); s["decided_by"] != "automatic" || s["reason_code"] != nil || s["reason_text"] != nil {
		t.Fatalf("automatic status %v", s)
	}
	if r := e.do(t, request{method: http.MethodPost, path: "/users/" + bobID + "/report", token: alice.access, body: map[string]any{"reason": "spam", "block": false}}); r.Code != http.StatusNoContent {
		t.Fatalf("report: %d %s", r.Code, r.Body)
	}
	if _, err := svc.Decide(ctx, moderation.Decision{UserID: mustID(t, bobID), Status: moderation.Restricted, ReasonCode: "scam", ReasonText: "Asked people for money."}); err != nil {
		t.Fatal(err)
	}
	r = e.do(t, request{method: http.MethodGet, path: "/me/account-status", token: bob.access})
	if s := r.json(); s["status"] != "restricted" || s["decided_by"] != "staff" || s["reason_code"] != "scam" || s["reason_text"] != "Asked people for money." || s["can_appeal"] != false {
		t.Fatalf("staff status %v", s)
	}

	list := func(s session) []any {
		r := e.do(t, request{method: http.MethodGet, path: "/me/report-outcomes", token: s.access})
		if r.Code != http.StatusOK {
			t.Fatalf("outcomes: %d %s", r.Code, r.Body)
		}
		return r.json()["items"].([]any)
	}
	if items := list(bob); len(items) != 0 {
		t.Fatalf("the reported user sees no outcomes: %v", items)
	}
	items := list(alice)
	if len(items) != 1 {
		t.Fatalf("reporter outcomes %v", items)
	}
	o := items[0].(map[string]any)
	if o["subject_type"] != "user" || o["subject_id"] != bobID || o["outcome"] != "action_taken" || o["read"] != false {
		t.Fatalf("outcome %v", o)
	}
	id := o["id"].(string)
	read := func(s session, id string) reply {
		return e.do(t, request{method: http.MethodPost, path: "/me/report-outcomes/" + id + "/read", token: s.access})
	}
	if r := read(bob, id); r.Code != http.StatusNotFound {
		t.Fatalf("someone else's outcome: %d %s", r.Code, r.Body)
	}
	if r := read(alice, "x"); r.Code != http.StatusBadRequest {
		t.Fatalf("bad id: %d %s", r.Code, r.Body)
	}
	if r := read(alice, id); r.Code != http.StatusNoContent {
		t.Fatalf("read: %d %s", r.Code, r.Body)
	}
	if items := list(alice); items[0].(map[string]any)["read"] != true {
		t.Fatalf("after reading %v", items)
	}
}

func TestRequestAccountReview(t *testing.T) {
	e := newChatEnv(t)
	alice := e.register(t, "alice")
	review := func(body any) reply {
		return e.do(t, request{method: http.MethodPost, path: "/me/account-status/review", token: alice.access, body: body})
	}
	if r := review(map[string]any{"text": "please"}); r.Code != http.StatusConflict {
		t.Fatalf("review of a good account: %d %s", r.Code, r.Body)
	}
	e.setStatus(t, e.uid(t, alice), moderation.Restricted)
	s := e.do(t, request{method: http.MethodGet, path: "/me/account-status", token: alice.access}).json()
	if s["can_request_review"] != true || s["review_requested_at"] != nil {
		t.Fatalf("status before the request %v", s)
	}
	if r := review(map[string]any{"text": "   "}); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("blank text: %d %s", r.Code, r.Body)
	}
	if r := review(map[string]any{}); r.Code != http.StatusBadRequest && r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing text: %d %s", r.Code, r.Body)
	}
	r := review(map[string]any{"text": "I did not spam anyone."})
	if r.Code != http.StatusOK || r.json()["review_requested_at"] == nil || r.json()["can_request_review"] != false || r.json()["status"] != "restricted" {
		t.Fatalf("review: %d %s", r.Code, r.Body)
	}
	if r := review(map[string]any{"text": "again"}); r.Code != http.StatusConflict {
		t.Fatalf("second request: %d %s", r.Code, r.Body)
	}
	if r := e.do(t, request{method: http.MethodPost, path: "/me/account-status/review", body: map[string]any{"text": "x"}}); r.Code != http.StatusUnauthorized {
		t.Fatalf("without a token: %d %s", r.Code, r.Body)
	}
}
