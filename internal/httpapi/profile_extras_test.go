package httpapi

import (
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/store/postgres"
)

func TestValidBirthday(t *testing.T) {
	year := func(y int) *int { return &y }
	cases := []struct {
		b    postgres.Birthday
		want bool
	}{
		{postgres.Birthday{Day: 29, Month: 2}, true},
		{postgres.Birthday{Day: 29, Month: 2, Year: year(2024)}, true},
		{postgres.Birthday{Day: 29, Month: 2, Year: year(2023)}, false},
		{postgres.Birthday{Day: 31, Month: 4}, false},
		{postgres.Birthday{Day: 0, Month: 1}, false},
		{postgres.Birthday{Day: 1, Month: 13}, false},
		{postgres.Birthday{Day: 1, Month: 1, Year: year(1899)}, false},
		{postgres.Birthday{Day: 15, Month: 7, Year: year(1990)}, true},
	}
	for _, c := range cases {
		if got := validBirthday(c.b); got != c.want {
			t.Errorf("%+v: got %v", c.b, got)
		}
	}
}

func TestBackgroundFromRequest(t *testing.T) {
	id := uuid.New()
	preset, badPreset := "sunset", "Sun Set"
	one, three, upper, bad := []string{"#336699"}, []string{"#000000", "#111111", "#222222"}, []string{"#AABBCC"}, []string{"red"}
	cases := []struct {
		req SetBackgroundRequest
		ok  bool
	}{
		{SetBackgroundRequest{Kind: Media, AttachmentId: &id}, true},
		{SetBackgroundRequest{Kind: Media}, false},
		{SetBackgroundRequest{Kind: Media, AttachmentId: &id, Preset: &preset}, false},
		{SetBackgroundRequest{Kind: Preset, Preset: &preset}, true},
		{SetBackgroundRequest{Kind: Preset, Preset: &badPreset}, false},
		{SetBackgroundRequest{Kind: Color, Colors: &one}, true},
		{SetBackgroundRequest{Kind: Color, Colors: &upper}, true},
		{SetBackgroundRequest{Kind: Color, Colors: &three}, false},
		{SetBackgroundRequest{Kind: Color, Colors: &bad}, false},
		{SetBackgroundRequest{Kind: "video"}, false},
	}
	for i, c := range cases {
		bg, problem := backgroundFromRequest(c.req)
		if (problem == nil) != c.ok {
			t.Errorf("case %d: problem %v", i, problem)
		}
		if c.ok && c.req.Kind == Color && bg.Colors[0] != "#336699" && bg.Colors[0] != "#aabbcc" {
			t.Errorf("case %d: colors %v", i, bg.Colors)
		}
	}
}

func TestProfileExtrasOverHTTP(t *testing.T) {
	e := newAccountEnv(t)
	alice, bob := e.register(t, "alice"), e.register(t, "bob")
	aliceID := e.id(t, alice)

	r := e.do(t, request{method: http.MethodPatch, path: "/me", token: alice.access, body: map[string]any{
		"pronouns": " she/her ", "birthday": map[string]any{"day": 29, "month": 2, "year": nil}, "time_zone": "Europe/Berlin",
	}})
	if r.Code != http.StatusOK {
		t.Fatalf("patch me: %d %s", r.Code, r.Body)
	}
	me := r.json()
	if me["pronouns"] != "she/her" || me["time_zone"] != "Europe/Berlin" || me["birthday"].(map[string]any)["day"] != float64(29) {
		t.Fatalf("extras: %v", me)
	}
	for _, bad := range []map[string]any{
		{"birthday": map[string]any{"day": 29, "month": 2, "year": 2023}},
		{"birthday": map[string]any{"day": 31, "month": 4}},
		{"time_zone": "Mars/Olympus"},
		{"time_zone": "Local"},
		{"pronouns": "this is far too long to be a set of pronouns at all"},
	} {
		if r := e.do(t, request{method: http.MethodPatch, path: "/me", token: alice.access, body: bad}); r.Code != http.StatusUnprocessableEntity {
			t.Errorf("patch %v: %d", bad, r.Code)
		}
	}

	seen := e.do(t, request{method: http.MethodGet, path: "/users/" + aliceID, token: bob.access}).json()
	if seen["pronouns"] != "she/her" || seen["birthday"] != nil {
		t.Fatalf("a stranger sees pronouns but not the birthday by default: %v", seen)
	}
	if r := e.do(t, request{method: http.MethodPatch, path: "/me/privacy", token: alice.access, body: map[string]any{"birthday_visibility": "everyone"}}); r.Code != http.StatusOK || r.json()["birthday_visibility"] != "everyone" {
		t.Fatalf("birthday privacy: %d %s", r.Code, r.Body)
	}
	seen = e.do(t, request{method: http.MethodGet, path: "/users/" + aliceID, token: bob.access}).json()
	if b, ok := seen["birthday"].(map[string]any); !ok || b["month"] != float64(2) || b["year"] != nil {
		t.Fatalf("birthday for everyone: %v", seen)
	}
	if r := e.do(t, request{method: http.MethodPut, path: "/me/privacy/exceptions/birthday_visibility/deny", token: alice.access, body: map[string]any{"user_ids": []string{e.id(t, bob)}}}); r.Code != http.StatusOK {
		t.Fatalf("deny exception: %d %s", r.Code, r.Body)
	}
	if seen = e.do(t, request{method: http.MethodGet, path: "/users/" + aliceID, token: bob.access}).json(); seen["birthday"] != nil {
		t.Fatalf("a deny exception hides the birthday: %v", seen)
	}

	r = e.do(t, request{method: http.MethodPatch, path: "/me", token: alice.access, body: map[string]any{"pronouns": "", "birthday": nil}})
	if r.Code != http.StatusOK || r.json()["pronouns"] != nil || r.json()["birthday"] != nil {
		t.Fatalf("clear extras: %d %s", r.Code, r.Body)
	}
}

func TestProfileBackgroundOverHTTP(t *testing.T) {
	e := newAccountEnv(t)
	alice, bob := e.register(t, "alice"), e.register(t, "bob")
	aliceID := e.id(t, alice)

	r := e.do(t, request{method: http.MethodPut, path: "/me/background", token: alice.access, body: map[string]any{"kind": "color", "colors": []string{"#112233", "#AABBCC"}}})
	if r.Code != http.StatusOK {
		t.Fatalf("color background: %d %s", r.Code, r.Body)
	}
	bg := r.json()["background"].(map[string]any)
	if bg["kind"] != "color" || bg["colors"].([]any)[1] != "#aabbcc" {
		t.Fatalf("background: %v", bg)
	}
	seen := e.do(t, request{method: http.MethodGet, path: "/users/" + aliceID, token: bob.access}).json()
	if seen["background"].(map[string]any)["kind"] != "color" {
		t.Fatalf("others see the background: %v", seen)
	}
	if r := e.do(t, request{method: http.MethodPatch, path: "/me/privacy", token: alice.access, body: map[string]any{"profile_visibility": "nobody"}}); r.Code != http.StatusOK {
		t.Fatalf("profile privacy: %d", r.Code)
	}
	if seen = e.do(t, request{method: http.MethodGet, path: "/users/" + aliceID, token: bob.access}).json(); seen["background"] != nil {
		t.Fatalf("the background follows profile_visibility: %v", seen)
	}

	for _, bad := range []map[string]any{
		{"kind": "preset"},
		{"kind": "media"},
		{"kind": "media", "attachment_id": uuid.NewString()},
		{"kind": "color", "colors": []string{"#112233"}, "preset": "sunset"},
	} {
		if r := e.do(t, request{method: http.MethodPut, path: "/me/background", token: alice.access, body: bad}); r.Code != http.StatusUnprocessableEntity {
			t.Errorf("background %v: %d %s", bad, r.Code, r.Body)
		}
	}
	r = e.do(t, request{method: http.MethodPut, path: "/me/background", token: alice.access, body: map[string]any{"kind": "preset", "preset": "sunset"}})
	if r.Code != http.StatusOK || r.json()["background"].(map[string]any)["preset"] != "sunset" {
		t.Fatalf("preset background: %d %s", r.Code, r.Body)
	}
	r = e.do(t, request{method: http.MethodDelete, path: "/me/background", token: alice.access})
	if r.Code != http.StatusOK || r.json()["background"] != nil {
		t.Fatalf("delete background: %d %s", r.Code, r.Body)
	}

	if r := e.do(t, request{method: http.MethodPut, path: "/me/music", token: alice.access, body: map[string]any{"attachment_id": uuid.NewString()}}); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("music needs an own upload: %d %s", r.Code, r.Body)
	}
	if r := e.do(t, request{method: http.MethodDelete, path: "/me/music", token: alice.access}); r.Code != http.StatusOK || r.json()["music"] != nil {
		t.Fatalf("delete music: %d %s", r.Code, r.Body)
	}
}

func TestProfileExtrasBumpTheVersion(t *testing.T) {
	e := newAccountEnv(t)
	alice := e.register(t, "alice")
	steps := []request{
		{method: http.MethodPatch, path: "/me", body: map[string]any{"pronouns": "they/them"}},
		{method: http.MethodPatch, path: "/me", body: map[string]any{"birthday": map[string]any{"day": 1, "month": 5}}},
		{method: http.MethodPatch, path: "/me", body: map[string]any{"time_zone": "Europe/Berlin"}},
		{method: http.MethodPut, path: "/me/background", body: map[string]any{"kind": "preset", "preset": "dawn"}},
	}
	for _, s := range steps {
		s.token = alice.access
		if r := e.do(t, s); r.Code != http.StatusOK {
			t.Fatalf("%s %s: %d %s", s.method, s.path, r.Code, r.Body)
		}
	}
	e.mu.Lock()
	got := append([]int64(nil), e.profiles...)
	e.mu.Unlock()
	if len(got) != 3 || got[2] != 3 {
		t.Fatalf("pronouns, birthday and background announce versions, the time zone does not: %v", got)
	}
}

func TestGroupBackgroundOverHTTP(t *testing.T) {
	e := newChatEnv(t)
	owner, bob := e.register(t, "owner"), e.register(t, "bob")
	e.openInvites(t, bob)
	gid := e.do(t, request{method: http.MethodPost, path: "/groups", token: owner.access, body: map[string]any{"name": "Team", "member_ids": []string{e.uid(t, bob)}}}).json()["id"].(string)

	r := e.do(t, request{method: http.MethodPut, path: "/groups/" + gid + "/background", token: owner.access, body: map[string]any{"kind": "preset", "preset": "ocean"}})
	if r.Code != http.StatusOK || r.json()["background"].(map[string]any)["preset"] != "ocean" {
		t.Fatalf("owner sets the background: %d %s", r.Code, r.Body)
	}
	if g := e.do(t, request{method: http.MethodGet, path: "/groups/" + gid, token: bob.access}).json(); g["background"].(map[string]any)["kind"] != "preset" {
		t.Fatalf("members see it: %v", g)
	}
	if r := e.do(t, request{method: http.MethodPut, path: "/groups/" + gid + "/background", token: bob.access, body: map[string]any{"kind": "color", "colors": []string{"#000000"}}}); r.Code != http.StatusForbidden {
		t.Fatalf("a member may not change it: %d %s", r.Code, r.Body)
	}
	if r := e.do(t, request{method: http.MethodPut, path: "/groups/" + gid + "/background", token: owner.access, body: map[string]any{"kind": "media", "attachment_id": uuid.NewString()}}); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("media needs an own upload: %d %s", r.Code, r.Body)
	}
	r = e.do(t, request{method: http.MethodDelete, path: "/groups/" + gid + "/background", token: owner.access})
	if r.Code != http.StatusOK || r.json()["background"] != nil {
		t.Fatalf("delete: %d %s", r.Code, r.Body)
	}
}

func TestProfileBackgroundDownloadFollowsTheProfile(t *testing.T) {
	e := newMediaEnv(t)
	alice, bob := e.register(t, "alice"), e.register(t, "bob")
	aliceID := e.userID(t, alice)

	up := e.upload(t, alice.access, map[string]string{"purpose": "profile_background"}, "bg.png", smallPNG(t))
	if up.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", up.Code, up.Body)
	}
	url := up.json()["url"].(string)
	path := url[len(apiBase):]
	if r := e.do(t, request{method: http.MethodGet, path: path, token: bob.access}); r.Code != http.StatusNotFound {
		t.Fatalf("an unused upload is the uploader's only: %d", r.Code)
	}
	id := uuid.MustParse(up.json()["id"].(string))
	if _, err := e.store.Users().SetBackground(t.Context(), aliceID, &postgres.Background{Kind: postgres.BackgroundMedia, AttachmentID: &id}); err != nil {
		t.Fatal(err)
	}
	if r := e.do(t, request{method: http.MethodGet, path: path, token: bob.access}); r.Code != http.StatusOK {
		t.Fatalf("others load a background in use: %d", r.Code)
	}
	if _, err := e.store.Social().PatchPrivacy(t.Context(), aliceID, postgres.PrivacyPatch{ProfileVisibility: new(postgres.VisibleNobody)}); err != nil {
		t.Fatal(err)
	}
	if r := e.do(t, request{method: http.MethodGet, path: path, token: bob.access}); r.Code != http.StatusNotFound {
		t.Fatalf("a hidden profile hides its background: %d", r.Code)
	}
	if r := e.do(t, request{method: http.MethodGet, path: path, token: alice.access}); r.Code != http.StatusOK {
		t.Fatalf("the owner still loads it: %d", r.Code)
	}
}
