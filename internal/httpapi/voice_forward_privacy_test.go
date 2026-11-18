package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/chats"
	"github.com/myronsi/messenger-back/internal/store/postgres"
)

func TestVoiceVideoAndForwardLinkSettings(t *testing.T) {
	e := newChatEnv(t)
	alice := e.register(t, "alice")
	p := e.do(t, request{method: http.MethodGet, path: "/me/privacy", token: alice.access}).json()
	exceptions := p["exceptions"].(map[string]any)
	if p["voice_video_messages"] != "everyone" || p["forward_link"] != "everyone" || exceptions["voice_video_messages"] == nil || exceptions["forward_link"] == nil {
		t.Fatalf("defaults: %v", p)
	}
	r := e.do(t, request{method: http.MethodPatch, path: "/me/privacy", token: alice.access, body: map[string]any{"voice_video_messages": "contacts", "forward_link": "nobody"}})
	if r.Code != http.StatusOK || r.json()["voice_video_messages"] != "contacts" || r.json()["forward_link"] != "nobody" {
		t.Fatalf("patch: %d %s", r.Code, r.Body)
	}
	aliceID := e.uid(t, alice)
	e.waitPeers(t, aliceID)
	for _, body := range []map[string]any{{"voice_video_messages": "nobody"}, {"forward_link": "wait_approval"}} {
		if r := e.do(t, request{method: http.MethodPatch, path: "/me/privacy", token: alice.access, body: body}); r.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%v: %d", body, r.Code)
		}
	}
	bobID := e.uid(t, e.register(t, "bob"))
	for _, key := range []string{"voice_video_messages", "forward_link"} {
		r := e.do(t, request{method: http.MethodPut, path: "/me/privacy/exceptions/" + key + "/allow", token: alice.access, body: map[string]any{"user_ids": []string{bobID}}})
		if r.Code != http.StatusOK || len(r.json()["exceptions"].(map[string]any)[key].(map[string]any)["allow"].([]any)) != 1 {
			t.Fatalf("%s exceptions: %d %s", key, r.Code, r.Body)
		}
	}
	e.waitPeers(t, aliceID)
	select {
	case id := <-e.peersDone:
		t.Fatalf("a forward_link exception refreshed the peers of %d", id)
	default:
	}
}

func TestVoiceToANonContactIsRefusedAndThePeerSaysSo(t *testing.T) {
	e := newChatEnv(t)
	bob, stranger := e.register(t, "bob"), e.register(t, "stranger")
	bobID, strangerID := e.uid(t, bob), e.uid(t, stranger)
	if r := e.do(t, request{method: http.MethodPatch, path: "/me/privacy", token: bob.access, body: map[string]any{"voice_video_messages": "contacts"}}); r.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", r.Code, r.Body)
	}
	chat := e.create(t, stranger, bobID, "").json()["id"].(string)
	e.waitPeers(t, bobID)
	peer := func(s session) any {
		t.Helper()
		r := e.do(t, request{method: http.MethodGet, path: "/chats/" + chat, token: s.access})
		if r.Code != http.StatusOK {
			t.Fatalf("chat: %d %s", r.Code, r.Body)
		}
		return r.json()["peer"].(map[string]any)["can_send_voice_video"]
	}
	if got := peer(stranger); got != false {
		t.Fatalf("stranger may send voice to bob: %v", got)
	}
	if got := peer(bob); got != true {
		t.Fatalf("bob may send voice to the stranger: %v", got)
	}

	chatID, _ := strconv.ParseInt(chat, 10, 64)
	uploader, _ := strconv.ParseInt(strangerID, 10, 64)
	sendVoice := func() reply {
		a, err := e.store.Attachments().Create(context.Background(), postgres.NewAttachment{UploaderID: &uploader, ChatID: &chatID, Kind: "voice", StorageKey: "attachments/" + uuid.NewString(), MimeType: "audio/ogg", Size: 10})
		if err != nil {
			t.Fatal(err)
		}
		return e.do(t, request{method: http.MethodPost, path: "/chats/" + chat + "/messages", token: stranger.access,
			body: map[string]any{"client_temp_id": uuid.NewString(), "type": "voice", "attachment_id": a.ID.String()}})
	}
	r := sendVoice()
	if r.Code != http.StatusForbidden || r.json()["code"] != "forbidden" {
		t.Fatalf("voice to a non-contact: %d %s", r.Code, r.Body)
	}
	if errs := r.json()["errors"].([]any); len(errs) != 1 || errs[0].(map[string]any)["field"] != "voice_video_messages" {
		t.Fatalf("problem fields: %s", r.Body)
	}
	if r := e.do(t, request{method: http.MethodPut, path: "/me/privacy/exceptions/voice_video_messages/allow", token: bob.access, body: map[string]any{"user_ids": []string{strangerID}}}); r.Code != http.StatusOK {
		t.Fatalf("exception: %d %s", r.Code, r.Body)
	}
	e.waitPeers(t, bobID)
	if v, ok := e.events.updatedView(uploader); !ok || v.Entry.Chat.ID != chatID || v.Peer == nil || !v.PeerVoice {
		t.Fatalf("the stranger's chat entry was not refreshed: %+v %v", v.Peer, ok)
	}
	if got := peer(stranger); got != true {
		t.Fatalf("allowed stranger: %v", got)
	}
	if r := sendVoice(); r.Code != http.StatusCreated {
		t.Fatalf("voice with an allow exception: %d %s", r.Code, r.Body)
	}
}

func (e *chatEnv) waitPeers(t *testing.T, userID string) {
	t.Helper()
	want, _ := strconv.ParseInt(userID, 10, 64)
	select {
	case got := <-e.peersDone:
		if got != want {
			t.Fatalf("peers refreshed for %d, want %d", got, want)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the peers of the changed settings were not refreshed")
	}
}

func (c *chatEvents) updatedView(userID int64) (chats.View, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.updatedViews[userID]
	return v, ok
}

func TestForwardedCopiesHideTheLinkFromViewersTheSenderExcludes(t *testing.T) {
	e := newChatEnv(t)
	alice, bob, carol, dave := e.register(t, "alice"), e.register(t, "bob"), e.register(t, "carol"), e.register(t, "dave")
	aliceID, bobID, carolID, daveID := e.uid(t, alice), e.uid(t, bob), e.uid(t, carol), e.uid(t, dave)
	if r := e.do(t, request{method: http.MethodPatch, path: "/me/privacy", token: alice.access, body: map[string]any{"forward_link": "contacts"}}); r.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", r.Code, r.Body)
	}
	if r := e.do(t, request{method: http.MethodPut, path: "/me/privacy/exceptions/forward_link/allow", token: alice.access, body: map[string]any{"user_ids": []string{daveID}}}); r.Code != http.StatusOK {
		t.Fatalf("exception: %d %s", r.Code, r.Body)
	}
	direct := e.create(t, alice, bobID, "").json()["id"].(string)
	original := e.send(t, alice, direct, "a-1", "hello").json()["id"].(string)
	id := func(s string) int64 {
		n, _ := strconv.ParseInt(s, 10, 64)
		return n
	}
	group, err := e.store.Chats().CreateGroup(context.Background(), postgres.NewGroup{OwnerID: id(bobID), Name: "g", MemberIDs: []int64{id(carolID), id(daveID)}})
	if err != nil {
		t.Fatal(err)
	}
	target := strconv.FormatInt(group.ID, 10)
	fw := e.do(t, request{method: http.MethodPost, path: "/messages/" + original + "/forward", token: bob.access, body: map[string]any{"chat_ids": []string{target}}})
	if fw.Code != http.StatusCreated {
		t.Fatalf("forward: %d %s", fw.Code, fw.Body)
	}
	if got := fw.json()["items"].([]any)[0].(map[string]any)["forwarded_from"].(map[string]any)["sender_id"]; got != aliceID {
		t.Fatalf("bob is alice's contact and sees the link: %v", got)
	}
	from := func(s session) map[string]any {
		t.Helper()
		got, _ := e.history(t, s, target, "")
		if len(got) != 1 {
			t.Fatalf("history: %v", got)
		}
		return got[0]["forwarded_from"].(map[string]any)
	}
	if f := from(bob); f["sender_id"] != aliceID || f["sender_name"] != "Display alice" {
		t.Fatalf("bob: %v", f)
	}
	if f := from(carol); f["sender_id"] != nil || f["sender_name"] != "Display alice" {
		t.Fatalf("carol is no contact of alice: %v", f)
	}
	if f := from(dave); f["sender_id"] != aliceID {
		t.Fatalf("dave has an allow exception: %v", f)
	}
	chats := items(t, e.do(t, request{method: http.MethodGet, path: "/chats", token: carol.access}))
	if len(chats) != 1 || chats[0]["last_message"].(map[string]any)["forwarded_from"].(map[string]any)["sender_id"] != nil {
		t.Fatalf("carol's chat list shows the link: %v", chats)
	}
	chats = items(t, e.do(t, request{method: http.MethodGet, path: "/chats", token: dave.access}))
	if len(chats) != 1 || chats[0]["last_message"].(map[string]any)["forwarded_from"].(map[string]any)["sender_id"] != aliceID {
		t.Fatalf("dave's chat list hides the link: %v", chats)
	}
}
