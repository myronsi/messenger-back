package httpapi

import (
	"bytes"
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/auth"
	"github.com/myronsi/messenger-back/internal/config"
	"github.com/myronsi/messenger-back/internal/emoji"
	"github.com/myronsi/messenger-back/internal/media"
	"github.com/myronsi/messenger-back/internal/observability"
	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/redis"
	"github.com/myronsi/messenger-back/internal/users"
)

type emojiEnv struct {
	*mediaEnv
}

func newEmojiEnv(t *testing.T) *emojiEnv {
	t.Helper()
	store, rdb := newPostgres(t), newRedis(t)
	log := observability.NewLogger(&bytes.Buffer{}, 0, "test")
	svc, err := auth.NewService(store, rdb, auth.Config{
		JWTSecret: bytes.Repeat([]byte("j"), 40), EncryptionKey: bytes.Repeat([]byte("k"), 32),
		RecoveryPepper: bytes.Repeat([]byte("p"), 32), Namespace: "t" + uuid.NewString(),
	}, log)
	if err != nil {
		t.Fatal(err)
	}
	disk, err := media.NewDisk(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rules := memberRules{store}
	msvc := media.NewService(media.Options{Storage: disk, Attachments: store.Attachments(), Visibility: rules, Membership: rules,
		Prober: media.NewProber("/none", "/none")})
	dir := users.NewDirectory(store, nil, apiBase)
	emo := emoji.New(store)
	api := NewServer(NewAuthServer(AuthOptions{Service: svc, Log: log, BasePath: apiBase, CookieSecure: true, Directory: dir}),
		NewMediaServer(MediaOptions{Service: msvc, Store: store, Directory: dir, Limiter: redis.NewRateLimiter(rdb, "t"+uuid.NewString()+":"), BasePath: apiBase, Log: log})).
		WithAccount(NewAccountServer(AccountOptions{Store: store, Directory: dir, Deleter: svc, Emoji: emo, BasePath: apiBase, Log: log})).
		WithEmoji(NewEmojiServer(EmojiOptions{Service: emo, Images: msvc, BasePath: apiBase, Log: log}))
	r := NewRouter(Options{
		HTTP: config.HTTP{BasePath: apiBase, RequestTimeout: 10 * time.Second, ReadinessTimeout: time.Second, MaxBodyBytes: 4096},
		Log:  log, Metrics: observability.NewMetrics(), API: api, Authenticator: svc, UploadMaxBytes: msvc.Limits().Max(),
	})
	return &emojiEnv{mediaEnv: &mediaEnv{authEnv: &authEnv{router: r}, store: store}}
}

func (e *emojiEnv) addEmoji(t *testing.T, token, packID, shortcode string) reply {
	t.Helper()
	up := e.upload(t, token, map[string]string{"purpose": "emoji"}, "e.png", smallPNG(t))
	if up.Code != http.StatusCreated {
		t.Fatalf("upload emoji: %d %s", up.Code, up.Body)
	}
	return e.do(t, request{method: http.MethodPost, path: "/emoji/packs/" + packID + "/emoji", token: token,
		body: map[string]any{"attachment_id": up.json()["id"], "shortcode": shortcode}})
}

func TestEmojiPacksOverHTTP(t *testing.T) {
	e := newEmojiEnv(t)
	alice, bob := e.register(t, "alice"), e.register(t, "bob")

	r := e.do(t, request{method: http.MethodPost, path: "/emoji/packs", token: alice.access, body: map[string]any{"title": " Cats "}})
	if r.Code != http.StatusCreated || r.json()["title"] != "Cats" || r.json()["slug"] == nil || r.json()["can_edit"] != true {
		t.Fatalf("create: %d %s", r.Code, r.Body)
	}
	packID, slug := r.json()["id"].(string), r.json()["slug"].(string)
	added := e.addEmoji(t, alice.access, packID, "cat")
	if added.Code != http.StatusCreated || added.json()["shortcode"] != "cat" {
		t.Fatalf("add: %d %s", added.Code, added.Body)
	}
	catID := added.json()["id"].(string)
	if r := e.addEmoji(t, alice.access, packID, "cat"); r.Code != http.StatusConflict {
		t.Fatalf("duplicate shortcode: %d", r.Code)
	}
	if r := e.addEmoji(t, alice.access, packID, "Bad Code"); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad shortcode: %d", r.Code)
	}
	if r := e.addEmoji(t, bob.access, packID, "dog"); r.Code != http.StatusNotFound {
		t.Fatalf("someone else's pack by id: %d", r.Code)
	}

	if r := e.do(t, request{method: http.MethodGet, path: "/emoji/packs/" + packID, token: bob.access}); r.Code != http.StatusNotFound {
		t.Fatalf("not findable by id: %d", r.Code)
	}
	if r := e.do(t, request{method: http.MethodGet, path: "/emoji/links/" + slug, token: bob.access}); r.Code != http.StatusOK || r.json()["can_edit"] != false {
		t.Fatalf("by link: %d %s", r.Code, r.Body)
	}
	if r := e.do(t, request{method: http.MethodPut, path: "/emoji/packs/" + packID + "/installation", token: bob.access}); r.Code != http.StatusNoContent {
		t.Fatalf("install: %d", r.Code)
	}
	list := e.do(t, request{method: http.MethodGet, path: "/emoji/packs", token: bob.access}).json()["items"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["is_installed"] != true {
		t.Fatalf("bob's packs: %v", list)
	}

	img := e.do(t, request{method: http.MethodGet, path: "/emoji/items/" + catID + "/image", token: bob.access})
	if img.Code != http.StatusOK || img.Header().Get("Content-Type") != "image/png" || img.Header().Get("Cache-Control") != cacheForever {
		t.Fatalf("image: %d %v", img.Code, img.Header())
	}
	one := e.do(t, request{method: http.MethodGet, path: "/emoji/items/" + catID, token: bob.access})
	if one.Code != http.StatusOK || one.json()["pack_slug"] != slug || one.json()["url"] != apiBase+"/emoji/items/"+catID+"/image" {
		t.Fatalf("one emoji: %d %s", one.Code, one.Body)
	}

	name := "Bob <:cat:" + catID + "> <:dog:999>"
	me := e.do(t, request{method: http.MethodPatch, path: "/me", token: bob.access, body: map[string]any{"display_name": name}})
	if me.Code != http.StatusOK || me.json()["display_name"] != "Bob <:cat:"+catID+"> :dog:" {
		t.Fatalf("display name: %d %s", me.Code, me.Body)
	}

	if r := e.do(t, request{method: http.MethodDelete, path: "/emoji/packs/" + packID + "/emoji/" + catID, token: alice.access}); r.Code != http.StatusNoContent {
		t.Fatalf("delete emoji: %d", r.Code)
	}
	if r := e.do(t, request{method: http.MethodGet, path: "/emoji/items/" + catID + "/image", token: bob.access}); r.Code != http.StatusNotFound {
		t.Fatalf("a deleted emoji has no image: %d", r.Code)
	}
}

func TestGroupEmojiOverHTTP(t *testing.T) {
	e := newEmojiEnv(t)
	owner, member, outsider := e.register(t, "owner"), e.register(t, "member"), e.register(t, "outsider")
	ownerID, memberID := e.userID(t, owner), e.userID(t, member)
	group, err := e.store.Chats().CreateGroup(context.Background(), postgres.NewGroup{OwnerID: ownerID, Name: "Team", MemberIDs: []int64{memberID}})
	if err != nil {
		t.Fatal(err)
	}
	gid := strconv.FormatInt(group.ID, 10)
	if r := e.do(t, request{method: http.MethodGet, path: "/groups/" + gid + "/emoji", token: outsider.access}); r.Code != http.StatusNotFound {
		t.Fatalf("outsider: %d", r.Code)
	}
	set := e.do(t, request{method: http.MethodGet, path: "/groups/" + gid + "/emoji", token: member.access})
	if set.Code != http.StatusOK || set.json()["chat_id"] != gid || set.json()["can_edit"] != false || set.json()["slug"] != nil {
		t.Fatalf("member: %d %s", set.Code, set.Body)
	}
	packID := set.json()["id"].(string)
	if r := e.addEmoji(t, member.access, packID, "wave"); r.Code != http.StatusForbidden {
		t.Fatalf("a member may not add: %d", r.Code)
	}
	if r := e.addEmoji(t, owner.access, packID, "wave"); r.Code != http.StatusCreated {
		t.Fatalf("owner adds: %d %s", r.Code, r.Body)
	}
	if r := e.do(t, request{method: http.MethodPatch, path: "/emoji/packs/" + packID, token: owner.access, body: map[string]any{"title": "x"}}); r.Code != http.StatusForbidden {
		t.Fatalf("group sets keep the group's name: %d", r.Code)
	}
	list := e.do(t, request{method: http.MethodGet, path: "/emoji/packs", token: member.access}).json()["items"].([]any)
	if len(list) != 1 || len(list[0].(map[string]any)["emoji"].([]any)) != 1 {
		t.Fatalf("member's packs: %v", list)
	}
}
