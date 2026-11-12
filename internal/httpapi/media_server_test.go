package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"math/rand/v2"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/auth"
	"github.com/myronsi/messenger-back/internal/config"
	"github.com/myronsi/messenger-back/internal/events"
	"github.com/myronsi/messenger-back/internal/media"
	"github.com/myronsi/messenger-back/internal/notifications"
	"github.com/myronsi/messenger-back/internal/observability"
	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/redis"
	"github.com/myronsi/messenger-back/internal/users"
)

// memberRules: every member of a chat sees every message (the message side is tested elsewhere).
type memberRules struct{ store *postgres.Store }

func (m memberRules) IsMember(ctx context.Context, chatID, userID int64) (bool, error) {
	_, err := m.store.Chats().Participant(ctx, chatID, userID)
	if errors.Is(err, postgres.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

func (m memberRules) VisibleAttachment(ctx context.Context, viewer, chatID, _ int64, _ uuid.UUID) (bool, error) {
	return m.IsMember(ctx, chatID, viewer)
}

type mediaEnv struct {
	*authEnv
	store  *postgres.Store
	tokens *notifications.ActionTokens
	events *mediaEvents
}

type mediaEvents struct {
	mu  sync.Mutex
	got []events.Event
}

func (l *mediaEvents) Emit(_ context.Context, e events.Event) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.got = append(l.got, e)
	return nil
}

func (l *mediaEvents) userUpdates(uid int64) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, e := range l.got {
		if e.Type == events.UserUpdated && e.UserID == uid {
			n++
		}
	}
	return n
}

func newMediaEnv(t *testing.T) *mediaEnv {
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
		Prober: media.NewProber("/none", "/none"), Limits: media.Limits{Image: 1 << 20, Audio: 1 << 20, Video: 1 << 20, File: 1 << 20, Avatar: 1 << 20, Sticker: 1 << 20}})
	tokens := notifications.NewActionTokens("test-secret")
	emitted := &mediaEvents{}
	r := NewRouter(Options{
		HTTP:    config.HTTP{BasePath: apiBase, RequestTimeout: 10 * time.Second, ReadinessTimeout: time.Second, MaxBodyBytes: 4096},
		Log:     log,
		Metrics: observability.NewMetrics(),
		API: NewServer(NewAuthServer(AuthOptions{Service: svc, Log: log, BasePath: apiBase, CookieSecure: true}),
			NewMediaServer(MediaOptions{Service: msvc, Store: store, Directory: users.NewDirectory(store, nil, apiBase),
				Limiter: redis.NewRateLimiter(rdb, "t"+uuid.NewString()+":"), Events: emitted, BasePath: apiBase, Log: log})).
			WithNotifications(NotificationOptions{Store: store.Notifications(), Tokens: tokens}),
		Authenticator:  svc,
		UploadMaxBytes: msvc.Limits().Max(),
	})
	return &mediaEnv{authEnv: &authEnv{router: r}, store: store, tokens: tokens, events: emitted}
}

func (e *mediaEnv) userID(t *testing.T, s session) int64 {
	t.Helper()
	rec := e.do(t, request{method: http.MethodGet, path: "/me", token: s.access})
	id, err := strconv.ParseInt(rec.json()["id"].(string), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (e *mediaEnv) upload(t *testing.T, token string, fields map[string]string, filename string, content []byte) reply {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	fw, _ := mw.CreateFormFile("file", filename)
	_, _ = fw.Write(content)
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, apiBase+"/attachments", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	checkContract(t, http.MethodPost, apiBase+"/attachments", rec.Code, rec.Header().Get("Content-Type"), rec.Body.Bytes())
	return reply{rec}
}

func smallPNG(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 8, 8))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestServeVariesByAuthorizationAndKeepsVary(t *testing.T) {
	w := httptest.NewRecorder()
	w.Header().Set("Vary", "Origin")
	(&MediaServer{}).serve(w, httptest.NewRequest(http.MethodGet, "/", nil), media.Download{RedirectURL: "https://storage.example/x"}, cacheForever, "")
	if got := w.Header().Values("Vary"); !slices.Equal(got, []string{"Origin", "Authorization"}) {
		t.Fatalf("Vary: %v", got)
	}
}

func TestUploadAndDownloadOverHTTP(t *testing.T) {
	e := newMediaEnv(t)
	alice, bob, eve := e.register(t, "alice"), e.register(t, "bob"), e.register(t, "eve")
	aliceID, bobID := e.userID(t, alice), e.userID(t, bob)
	chat, _, err := e.store.Chats().CreateDirect(context.Background(), aliceID, bobID)
	if err != nil {
		t.Fatal(err)
	}

	// Bigger than HTTP_MAX_BODY_BYTES (4 KiB) but within the upload limit.
	noisy := image.NewRGBA(image.Rect(0, 0, 96, 96))
	rng := rand.New(rand.NewPCG(1, 2))
	for i := range noisy.Pix {
		noisy.Pix[i] = byte(rng.IntN(256))
	}
	var bigBuf bytes.Buffer
	_ = png.Encode(&bigBuf, noisy)
	big := bigBuf.Bytes()
	if len(big) <= 4096 {
		t.Fatalf("test image of %d bytes does not exceed the body limit", len(big))
	}
	rec := e.upload(t, alice.access, map[string]string{"purpose": "message"}, "photo.png", big)
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	att := rec.json()
	id := att["id"].(string)
	if att["kind"] != "image" || att["content_type"] != "image/png" || att["url"] != apiBase+"/attachments/"+id+"/content" || att["thumbnail_url"] == nil {
		t.Fatalf("attachment: %v", att)
	}

	get := func(token, suffix string) reply {
		return e.do(t, request{method: http.MethodGet, path: "/attachments/" + id + "/content" + suffix, token: token})
	}
	if r := get(bob.access, ""); r.Code != http.StatusNotFound {
		t.Fatalf("bob before the message: %d", r.Code)
	}
	if err := e.store.Attachments().Link(context.Background(), uuid.MustParse(id), chat.ID, 1, nil); err != nil {
		t.Fatal(err)
	}
	r := get(bob.access, "")
	if r.Code != http.StatusOK || r.Header().Get("Content-Type") != "image/png" || r.Header().Get("X-Content-Type-Options") != "nosniff" ||
		r.Header().Get("Content-Security-Policy") == "" || r.Body.Len() == 0 || r.Header().Get("Cache-Control") != cacheForever ||
		!slices.Contains(r.Header().Values("Vary"), "Authorization") {
		t.Fatalf("download: %d %v", r.Code, r.Header())
	}
	// Players seek with byte ranges.
	part := e.do(t, request{method: http.MethodGet, path: "/attachments/" + id + "/content", token: bob.access, header: map[string]string{"Range": "bytes=0-9"}})
	if part.Code != http.StatusPartialContent || part.Body.Len() != 10 || part.Header().Get("Content-Range") == "" || part.Header().Get("ETag") == "" {
		t.Fatalf("range: %d %v", part.Code, part.Header())
	}
	if th := get(bob.access, "?variant=thumbnail"); th.Code != http.StatusOK || th.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("thumbnail: %d %v", th.Code, th.Header())
	}
	if att["display_url"] != nil {
		t.Fatalf("a 96 px image has a display_url: %v", att["display_url"])
	}
	if d := get(bob.access, "?variant=display"); d.Code != http.StatusOK || d.Header().Get("Content-Type") != "image/png" || d.Header().Get("ETag") != `"`+id+`-display"` {
		t.Fatalf("display of a small image: %d %v", d.Code, d.Header())
	}
	var wideBuf bytes.Buffer
	_ = png.Encode(&wideBuf, image.NewRGBA(image.Rect(0, 0, 2000, 100)))
	wide := e.upload(t, alice.access, map[string]string{"purpose": "message"}, "wide.png", wideBuf.Bytes()).json()
	wideURL := apiBase + "/attachments/" + wide["id"].(string) + "/content"
	if wide["display_url"] != wideURL+"?variant=display" {
		t.Fatalf("display_url: %v", wide["display_url"])
	}
	wd := e.do(t, request{method: http.MethodGet, path: "/attachments/" + wide["id"].(string) + "/content?variant=display", token: alice.access})
	if cfg, err := jpeg.DecodeConfig(wd.Body); wd.Code != http.StatusOK || err != nil || cfg.Width != media.DisplaySide {
		t.Fatalf("display copy: %d %v %+v", wd.Code, err, cfg)
	}
	if r := get(eve.access, ""); r.Code != http.StatusNotFound {
		t.Fatalf("outsider: %d", r.Code)
	}
	if r := get("", ""); r.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", r.Code)
	}

	// HTML is never served as such.
	rec = e.upload(t, alice.access, map[string]string{"purpose": "message"}, "page.html", []byte("<html><script>alert(1)</script></html>"))
	if rec.Code != http.StatusCreated || rec.json()["content_type"] != "application/octet-stream" {
		t.Fatalf("html upload: %d %s", rec.Code, rec.Body)
	}
	hid := rec.json()["id"].(string)
	h := e.do(t, request{method: http.MethodGet, path: "/attachments/" + hid + "/content", token: alice.access})
	if h.Header().Get("Content-Type") != "application/octet-stream" || h.Header().Get("Content-Disposition")[:10] != "attachment" {
		t.Fatalf("html download headers: %v", h.Header())
	}

	// Bad requests.
	if r := e.upload(t, alice.access, map[string]string{"purpose": "banner"}, "x.png", smallPNG(t)); r.Code != http.StatusBadRequest {
		t.Fatalf("bad purpose: %d", r.Code)
	}
	if r := e.upload(t, alice.access, map[string]string{"purpose": "avatar"}, "x.png", []byte("text")); r.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("avatar that is no image: %d", r.Code)
	}
	if r := e.upload(t, alice.access, map[string]string{"purpose": "message"}, "big.bin", bytes.Repeat([]byte{1}, 2<<20)); r.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("too large: %d", r.Code)
	}
	if r := e.do(t, request{method: http.MethodPost, path: "/attachments", token: alice.access, body: map[string]string{"purpose": "message"}}); r.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("json body: %d", r.Code)
	}
	// A body that ends in the middle of the form is the client's fault.
	cut := httptest.NewRequest(http.MethodPost, apiBase+"/attachments", strings.NewReader("--b\r\nContent-Disposition: form-data; name=\"file\"; filename=\"x\"\r\n\r\nabc"))
	cut.Header.Set("Content-Type", "multipart/form-data; boundary=b")
	cut.Header.Set("Authorization", "Bearer "+alice.access)
	cutRec := httptest.NewRecorder()
	e.router.ServeHTTP(cutRec, cut)
	checkContract(t, http.MethodPost, apiBase+"/attachments", cutRec.Code, cutRec.Header().Get("Content-Type"), cutRec.Body.Bytes())
	if cutRec.Code != http.StatusBadRequest {
		t.Fatalf("truncated form: %d %s", cutRec.Code, cutRec.Body)
	}
}

func TestTokenRoutesStayOutOfTraces(t *testing.T) {
	secret := tokenRoute()
	for path, want := range map[string]bool{
		apiBase + "/notifications/avatar/a.1.2.3.sig": true,
		apiBase + "/notifications/read/r.1.2.3.4.sig": true,
		apiBase + "//notifications/read/r.1":          true,
		apiBase + "/./notifications/read/r.1":         true,
		apiBase + "/x/../notifications/avatar/a.1":    true,
		apiBase + "/Notifications/READ/r.1":           true,
		apiBase + "/notifications%2Fread%2Fr.1":       true,
		apiBase + "/notifications%252Fread%252Fr.1":   true,
		apiBase + "/auth/recovery-requests/abc":       true,
		apiBase + "/auth/recovery-requests/abc/done":  true,
		apiBase + "/AUTH/qr-login/abc/poll":           true,
		apiBase + "/auth/qr-login%2Fabc":              true,
		apiBase + "/auth/recover/key":                 false,
		apiBase + "/me/notifications":                 false,
		apiBase + "/users/1/avatar":                   false,
		apiBase + "/notifications/readme":             false,
	} {
		if got := secret(httptest.NewRequest(http.MethodGet, path, nil)); got != want {
			t.Errorf("%s: %v, want %v", path, got, want)
		}
	}
}

func TestTransferRoutes(t *testing.T) {
	long := transferRoute(apiBase)
	for path, want := range map[string]bool{
		"POST " + apiBase + "/attachments":              true,
		"GET " + apiBase + "/attachments/x/content":     true,
		"HEAD " + apiBase + "/attachments/x/content":    true,
		"GET " + apiBase + "/users/1/avatar":            true,
		"GET " + apiBase + "/chats/1/avatar":            true,
		"GET " + apiBase + "/notifications/avatar/a.1":  true,
		"HEAD " + apiBase + "/notifications/avatar/a.1": true,
		"POST " + apiBase + "/notifications/read/r.1":   false,
		"GET " + apiBase + "/users/1/avatars":           false,
		"GET " + apiBase + "/attachments":               false,
		"POST " + apiBase + "/attachments/x/content":    false,
		"GET " + apiBase + "/me":                        false,
		"GET /elsewhere" + apiBase + "/users/1/avatar":  false,
	} {
		method, p, _ := strings.Cut(path, " ")
		if got := long(httptest.NewRequest(method, p, nil)); got != want {
			t.Errorf("%s: %v, want %v", path, got, want)
		}
	}
}

func TestNotificationAvatarSeesWhatItsRecipientSees(t *testing.T) {
	e := newMediaEnv(t)
	alice, bob, carol := e.register(t, "alice"), e.register(t, "bob"), e.register(t, "carol")
	aliceID, bobID, carolID := e.userID(t, alice), e.userID(t, bob), e.userID(t, carol)
	bobPush := pushDevice(t, e.store, bobID)
	carolPush := pushDevice(t, e.store, carolID)
	avatar := func(token string) reply {
		return e.do(t, request{method: http.MethodGet, path: "/notifications/avatar/" + token})
	}
	defaultBytes := avatar(e.tokens.Avatar(bobID, bobPush.ID, bobPush.SessionID, aliceID)).Body.Bytes()
	id := e.upload(t, alice.access, map[string]string{"purpose": "avatar"}, "me.png", smallPNG(t)).json()["id"].(string)
	if r := e.do(t, request{method: http.MethodPut, path: "/me/avatar", token: alice.access, body: map[string]string{"attachment_id": id}}); r.Code != http.StatusOK {
		t.Fatalf("set avatar: %d %s", r.Code, r.Body)
	}
	if got := avatar(e.tokens.Avatar(bobID, bobPush.ID, bobPush.SessionID, aliceID)); got.Code != http.StatusOK || bytes.Equal(got.Body.Bytes(), defaultBytes) {
		t.Fatalf("signed avatar without a session: %d", got.Code)
	}
	for _, bad := range []string{"nope", e.tokens.Read(bobID, bobPush.ID, bobPush.SessionID, 1, 1), notifications.NewActionTokens("other").Avatar(bobID, bobPush.ID, bobPush.SessionID, aliceID), e.tokens.Avatar(bobID, carolPush.ID, carolPush.SessionID, aliceID)} {
		if r := avatar(bad); r.Code != http.StatusNotFound {
			t.Fatalf("token %q: %d", bad, r.Code)
		}
	}
	p, _ := e.store.Social().Privacy(context.Background(), []int64{aliceID})
	s := p[aliceID]
	s.AvatarVisibility = postgres.VisibleSharedChats
	if _, err := e.store.Social().UpdatePrivacy(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if got := avatar(e.tokens.Avatar(carolID, carolPush.ID, carolPush.SessionID, aliceID)); !bytes.Equal(got.Body.Bytes(), defaultBytes) {
		t.Fatal("the avatar ignored the recipient's visibility")
	}
	if err := e.store.Social().Block(context.Background(), aliceID, bobID); err != nil {
		t.Fatal(err)
	}
	if got := avatar(e.tokens.Avatar(bobID, bobPush.ID, bobPush.SessionID, aliceID)); !bytes.Equal(got.Body.Bytes(), defaultBytes) {
		t.Fatal("a blocked recipient got the avatar")
	}
}

func TestAvatarsOverHTTP(t *testing.T) {
	e := newMediaEnv(t)
	alice, bob := e.register(t, "alice"), e.register(t, "bob")
	aliceID := e.userID(t, alice)

	// Without an avatar everybody gets the default image.
	def := e.do(t, request{method: http.MethodGet, path: "/users/" + strconv.FormatInt(aliceID, 10) + "/avatar", token: bob.access})
	if def.Code != http.StatusOK || def.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("default avatar: %d %v", def.Code, def.Header())
	}
	defaultBytes := def.Body.Bytes()

	rec := e.upload(t, alice.access, map[string]string{"purpose": "avatar"}, "me.png", smallPNG(t))
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload avatar: %d %s", rec.Code, rec.Body)
	}
	avatarID := rec.json()["id"].(string)
	// A message upload cannot become an avatar, and nobody else's upload either.
	msg := e.upload(t, alice.access, map[string]string{"purpose": "message"}, "x.png", smallPNG(t)).json()["id"].(string)
	if r := e.do(t, request{method: http.MethodPut, path: "/me/avatar", token: alice.access, body: map[string]string{"attachment_id": msg}}); r.Code != http.StatusBadRequest {
		t.Fatalf("message upload as avatar: %d", r.Code)
	}
	if r := e.do(t, request{method: http.MethodPut, path: "/me/avatar", token: bob.access, body: map[string]string{"attachment_id": avatarID}}); r.Code != http.StatusBadRequest {
		t.Fatalf("someone else's avatar: %d", r.Code)
	}
	me := e.do(t, request{method: http.MethodPut, path: "/me/avatar", token: alice.access, body: map[string]string{"attachment_id": avatarID}})
	avatarPath := "/users/" + strconv.FormatInt(aliceID, 10) + "/avatar"
	if me.Code != http.StatusOK || me.json()["avatar_url"] != apiBase+avatarPath+"?rev="+avatarID {
		t.Fatalf("set avatar: %d %s", me.Code, me.Body)
	}
	got := e.do(t, request{method: http.MethodGet, path: avatarPath + "?rev=" + avatarID, token: bob.access})
	if got.Code != http.StatusOK || bytes.Equal(got.Body.Bytes(), defaultBytes) {
		t.Fatal("bob does not see alice's avatar")
	}
	if c := got.Header().Get("Cache-Control"); c != cacheForever || !slices.Contains(got.Header().Values("Vary"), "Authorization") {
		t.Fatalf("current rev: %q %v", c, got.Header().Values("Vary"))
	}
	for _, query := range []string{"", "?rev=" + msg} {
		if c := e.do(t, request{method: http.MethodGet, path: avatarPath + query, token: bob.access}).Header().Get("Cache-Control"); c != "private, max-age=60" {
			t.Fatalf("avatar%s: %q", query, c)
		}
	}
	// The avatar itself is not downloadable as an attachment by others.
	if r := e.do(t, request{method: http.MethodGet, path: "/attachments/" + avatarID + "/content", token: bob.access}); r.Code != http.StatusNotFound {
		t.Fatalf("avatar as attachment: %d", r.Code)
	}

	hist := e.do(t, request{method: http.MethodGet, path: "/users/" + strconv.FormatInt(aliceID, 10) + "/avatars", token: bob.access})
	var page struct {
		Items []AvatarVersion `json:"items"`
	}
	if err := json.Unmarshal(hist.Body.Bytes(), &page); err != nil || len(page.Items) != 1 {
		t.Fatalf("history: %s %v", hist.Body, err)
	}

	// Alice hides her avatar from everybody but people she shares a chat with: bob gets the default.
	p, _ := e.store.Social().Privacy(context.Background(), []int64{aliceID})
	s := p[aliceID]
	s.AvatarVisibility = postgres.VisibleSharedChats
	if _, err := e.store.Social().UpdatePrivacy(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	hidden := e.do(t, request{method: http.MethodGet, path: avatarPath + "?rev=" + avatarID, token: bob.access})
	if !bytes.Equal(hidden.Body.Bytes(), defaultBytes) || hidden.Header().Get("Cache-Control") != "private, max-age=60" {
		t.Fatal("hidden avatar was served")
	}
	if h := e.do(t, request{method: http.MethodGet, path: "/users/" + strconv.FormatInt(aliceID, 10) + "/avatars", token: bob.access}); !bytes.Contains(h.Body.Bytes(), []byte(`"items":[]`)) {
		t.Fatalf("hidden history: %s", h.Body)
	}
}

func TestDeleteAvatarsOverHTTP(t *testing.T) {
	e := newMediaEnv(t)
	alice, bob := e.register(t, "alice"), e.register(t, "bob")
	aliceID := e.userID(t, alice)
	setAvatar := func(s session) string {
		t.Helper()
		id := e.upload(t, s.access, map[string]string{"purpose": "avatar"}, "me.png", smallPNG(t)).json()["id"].(string)
		if r := e.do(t, request{method: http.MethodPut, path: "/me/avatar", token: s.access, body: map[string]string{"attachment_id": id}}); r.Code != http.StatusOK {
			t.Fatalf("set avatar: %d %s", r.Code, r.Body)
		}
		return id
	}
	versions := func() []string {
		t.Helper()
		var page struct {
			Items []AvatarVersion `json:"items"`
		}
		r := e.do(t, request{method: http.MethodGet, path: "/users/" + strconv.FormatInt(aliceID, 10) + "/avatars", token: alice.access})
		if err := json.Unmarshal(r.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		out := make([]string, len(page.Items))
		for i, v := range page.Items {
			out[i] = v.Id
		}
		return out
	}
	current := func() *uuid.UUID {
		t.Helper()
		u, err := e.store.Users().Get(context.Background(), aliceID)
		if err != nil {
			t.Fatal(err)
		}
		return u.AvatarAttachmentID
	}
	remove := func(s session, version string) reply {
		return e.do(t, request{method: http.MethodDelete, path: "/me/avatars/" + version, token: s.access})
	}

	first, second, third := setAvatar(alice), setAvatar(alice), setAvatar(alice)
	if n := e.events.userUpdates(aliceID); n != 3 {
		t.Fatalf("%d user.updated events after three avatar changes", n)
	}
	v := versions()
	if len(v) != 3 {
		t.Fatalf("history: %v", v)
	}
	if r := remove(bob, v[0]); r.Code != http.StatusNotFound {
		t.Fatalf("someone else's avatar: %d", r.Code)
	}
	if r := remove(alice, "999999999"); r.Code != http.StatusNotFound {
		t.Fatalf("unknown avatar: %d", r.Code)
	}

	if r := remove(alice, v[1]); r.Code != http.StatusOK || current().String() != third {
		t.Fatalf("delete an older avatar: %d, current %v", r.Code, current())
	}
	r := remove(alice, v[0])
	if r.Code != http.StatusOK || current().String() != first || r.json()["avatar_url"] == nil {
		t.Fatalf("delete the current avatar: %d %s, current %v (second was %s)", r.Code, r.Body, current(), second)
	}
	if got := versions(); len(got) != 1 || got[0] != v[2] {
		t.Fatalf("history after deleting: %v", got)
	}
	r = remove(alice, v[2])
	if r.Code != http.StatusOK || current() != nil || r.json()["avatar_url"] != nil || len(versions()) != 0 {
		t.Fatalf("delete the last avatar: %d %s", r.Code, r.Body)
	}
	if n := e.events.userUpdates(aliceID); n != 6 {
		t.Fatalf("%d user.updated events, want one per avatar change", n)
	}
}

func TestConcurrentAvatarDeletesKeepTheOldest(t *testing.T) {
	e := newMediaEnv(t)
	alice := e.register(t, "alice")
	aliceID := e.userID(t, alice)
	setAvatar := func() string {
		t.Helper()
		id := e.upload(t, alice.access, map[string]string{"purpose": "avatar"}, "me.png", smallPNG(t)).json()["id"].(string)
		if r := e.do(t, request{method: http.MethodPut, path: "/me/avatar", token: alice.access, body: map[string]string{"attachment_id": id}}); r.Code != http.StatusOK {
			t.Fatalf("set avatar: %d %s", r.Code, r.Body)
		}
		return id
	}
	versions := func() []string {
		t.Helper()
		var page struct {
			Items []AvatarVersion `json:"items"`
		}
		r := e.do(t, request{method: http.MethodGet, path: "/users/" + strconv.FormatInt(aliceID, 10) + "/avatars", token: alice.access})
		if err := json.Unmarshal(r.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		out := make([]string, len(page.Items))
		for i, v := range page.Items {
			out[i] = v.Id
		}
		return out
	}

	for round := range 5 {
		first := setAvatar()
		setAvatar()
		setAvatar()
		v := versions()
		codes := make(chan int, 2)
		for _, version := range v[:2] {
			go func() {
				codes <- e.do(t, request{method: http.MethodDelete, path: "/me/avatars/" + version, token: alice.access}).Code
			}()
		}
		for range 2 {
			if code := <-codes; code != http.StatusOK {
				t.Fatalf("round %d: delete: %d", round, code)
			}
		}
		u, err := e.store.Users().Get(context.Background(), aliceID)
		if err != nil {
			t.Fatal(err)
		}
		if u.AvatarAttachmentID == nil || u.AvatarAttachmentID.String() != first {
			t.Fatalf("round %d: current avatar %v, want the oldest %s", round, u.AvatarAttachmentID, first)
		}
		if r := e.do(t, request{method: http.MethodDelete, path: "/me/avatars/" + v[2], token: alice.access}); r.Code != http.StatusOK {
			t.Fatalf("round %d: reset: %d", round, r.Code)
		}
	}
}

func TestStickerUploadOverHTTP(t *testing.T) {
	e := newMediaEnv(t)
	alice, bob := e.register(t, "alice"), e.register(t, "bob")
	r := e.upload(t, alice.access, map[string]string{"purpose": "sticker"}, "cat.png", smallPNG(t))
	if r.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", r.Code, r.Body)
	}
	a := r.json()
	if a["width"] != float64(512) || a["height"] != float64(512) || a["content_type"] != "image/png" || a["filename"] != "sticker.png" {
		t.Fatalf("sticker: %v", a)
	}
	path := "/attachments/" + a["id"].(string) + "/content"
	if got := e.do(t, request{method: http.MethodGet, path: path, token: alice.access}); got.Code != http.StatusOK {
		t.Fatalf("uploader: %d", got.Code)
	}
	if got := e.do(t, request{method: http.MethodGet, path: path, token: bob.access}); got.Code != http.StatusNotFound {
		t.Fatalf("someone else before it is in a pack: %d", got.Code)
	}
	if r := e.upload(t, alice.access, map[string]string{"purpose": "sticker"}, "notes.txt", []byte("not an image at all")); r.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("text as a sticker: %d %s", r.Code, r.Body)
	}
}
