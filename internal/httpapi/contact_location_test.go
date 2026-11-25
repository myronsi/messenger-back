package httpapi

import (
	"net/http"
	"strings"
	"testing"
)

func TestLocationMessages(t *testing.T) {
	e := newChatEnv(t)
	alice, bob := e.register(t, "alice"), e.register(t, "bob")
	chat := e.create(t, alice, e.uid(t, bob), "hi").json()["id"].(string)
	send := func(temp string, location map[string]any) reply {
		return e.do(t, request{method: http.MethodPost, path: "/chats/" + chat + "/messages", token: alice.access,
			body: map[string]any{"client_temp_id": temp, "type": "location", "location": location}})
	}

	if r := send("l-bad", map[string]any{"latitude": 91, "longitude": 0}); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("latitude out of range: %d %s", r.Code, r.Body)
	}
	if r := e.do(t, request{method: http.MethodPost, path: "/chats/" + chat + "/messages", token: alice.access,
		body: map[string]any{"client_temp_id": "l-content", "type": "location", "content": "x", "location": map[string]any{"latitude": 1, "longitude": 1}}}); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("location with content: %d %s", r.Code, r.Body)
	}
	sent := send("l-1", map[string]any{"latitude": 52.5200081234, "longitude": 13.404954, "accuracy_m": 20, "title": " Alexanderplatz "})
	if sent.Code != http.StatusCreated {
		t.Fatalf("send location: %d %s", sent.Code, sent.Body)
	}
	want := "📍 Alexanderplatz https://www.openstreetmap.org/?mlat=52.520008&mlon=13.404954#map=16/52.520008/13.404954"
	if c := sent.json()["content"]; c != want {
		t.Fatalf("content: %v", c)
	}
	loc := lastMessage(t, e, bob, chat)["location"].(map[string]any)
	if loc["latitude"] != 52.520008 || loc["title"] != "Alexanderplatz" || loc["accuracy_m"] != float64(20) {
		t.Fatalf("location: %v", loc)
	}
	img := loc["map"].(map[string]any)
	url := img["url"].(string)
	if !strings.HasPrefix(url, apiBase+"/maps/static?") || !strings.Contains(url, "lat=52.520008") || !strings.Contains(url, "sig=") || img["width"] != float64(640) {
		t.Fatalf("map image: %v", img)
	}
	chats := items(t, e.do(t, request{method: http.MethodGet, path: "/chats", token: alice.access}))
	if last := chats[0]["last_message"].(map[string]any); last["location"] == nil {
		t.Fatalf("chat-list preview of a location: %v", last)
	}
}

func TestContactMessages(t *testing.T) {
	e := newChatEnv(t)
	alice, bob, carol, dave := e.register(t, "alice"), e.register(t, "bob"), e.register(t, "carol"), e.register(t, "dave")
	carolID, daveID := e.uid(t, carol), e.uid(t, dave)
	chat := e.create(t, alice, e.uid(t, bob), "hi").json()["id"].(string)
	share := func(temp, userID string) reply {
		return e.do(t, request{method: http.MethodPost, path: "/chats/" + chat + "/messages", token: alice.access,
			body: map[string]any{"client_temp_id": temp, "type": "contact", "contact": map[string]any{"user_id": userID}}})
	}

	sent := share("c-1", carolID)
	if sent.Code != http.StatusCreated || sent.json()["content"] != "👤 @carol" {
		t.Fatalf("share contact: %d %s", sent.Code, sent.Body)
	}
	card := lastMessage(t, e, bob, chat)["contact"].(map[string]any)
	if card["user_id"] != carolID || card["user"].(map[string]any)["username"] != "carol" {
		t.Fatalf("contact card: %v", card)
	}
	chats := items(t, e.do(t, request{method: http.MethodGet, path: "/chats", token: alice.access}))
	if last := chats[0]["last_message"].(map[string]any); last["contact"] != nil || last["type"] != "contact" {
		t.Fatalf("chat-list preview of a contact: %v", last)
	}

	e.do(t, request{method: http.MethodPatch, path: "/me/privacy", token: dave.access, body: map[string]any{"search_visibility": "nobody"}})
	if r := share("c-2", daveID); r.Code != http.StatusForbidden {
		t.Fatalf("share a hidden user: %d %s", r.Code, r.Body)
	}
	if r := share("c-3", "999999999"); r.Code != http.StatusForbidden {
		t.Fatalf("share a missing user: %d %s", r.Code, r.Body)
	}
}
