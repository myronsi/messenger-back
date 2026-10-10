package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"math/rand/v2"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/auth"
	"github.com/myronsi/messenger-back/internal/config"
	"github.com/myronsi/messenger-back/internal/media"
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

func (m memberRules) Visible(ctx context.Context, viewer, chatID, _ int64) (bool, error) {
	return m.IsMember(ctx, chatID, viewer)
}

type mediaEnv struct {
	*authEnv
	store *postgres.Store
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
		Prober: media.NewProber("/none", "/none"), Limits: media.Limits{Image: 1 << 20, Audio: 1 << 20, Video: 1 << 20, File: 1 << 20, Avatar: 1 << 20}})
	r := NewRouter(Options{
		HTTP:    config.HTTP{BasePath: apiBase, RequestTimeout: 10 * time.Second, ReadinessTimeout: time.Second, MaxBodyBytes: 4096},
		Log:     log,
		Metrics: observability.NewMetrics(),
		API: NewServer(NewAuthServer(AuthOptions{Service: svc, Log: log, BasePath: apiBase, CookieSecure: true}),
			NewMediaServer(MediaOptions{Service: msvc, Store: store, Directory: users.NewDirectory(store, nil, apiBase),
				Limiter: redis.NewRateLimiter(rdb, "t"+uuid.NewString()+":"), BasePath: apiBase, Log: log})),
		Authenticator:  svc,
		UploadMaxBytes: msvc.Limits().Max(),
	})
	return &mediaEnv{authEnv: &authEnv{router: r}, store: store}
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
	if err := e.store.Attachments().Link(context.Background(), uuid.MustParse(id), chat.ID, 1); err != nil {
		t.Fatal(err)
	}
	r := get(bob.access, "")
	if r.Code != http.StatusOK || r.Header().Get("Content-Type") != "image/png" || r.Header().Get("X-Content-Type-Options") != "nosniff" ||
		r.Header().Get("Content-Security-Policy") == "" || r.Body.Len() == 0 {
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

func TestTransferRoutes(t *testing.T) {
	long := transferRoute(apiBase)
	for path, want := range map[string]bool{
		"POST " + apiBase + "/attachments":             true,
		"GET " + apiBase + "/attachments/x/content":    true,
		"HEAD " + apiBase + "/attachments/x/content":   true,
		"GET " + apiBase + "/users/1/avatar":           true,
		"GET " + apiBase + "/chats/1/avatar":           true,
		"GET " + apiBase + "/users/1/avatars":          false,
		"GET " + apiBase + "/attachments":              false,
		"POST " + apiBase + "/attachments/x/content":   false,
		"GET " + apiBase + "/me":                       false,
		"GET /elsewhere" + apiBase + "/users/1/avatar": false,
	} {
		method, p, _ := strings.Cut(path, " ")
		if got := long(httptest.NewRequest(method, p, nil)); got != want {
			t.Errorf("%s: %v, want %v", path, got, want)
		}
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
	if me.Code != http.StatusOK || me.json()["avatar_url"] != apiBase+"/users/"+strconv.FormatInt(aliceID, 10)+"/avatar" {
		t.Fatalf("set avatar: %d %s", me.Code, me.Body)
	}
	got := e.do(t, request{method: http.MethodGet, path: "/users/" + strconv.FormatInt(aliceID, 10) + "/avatar", token: bob.access})
	if got.Code != http.StatusOK || bytes.Equal(got.Body.Bytes(), defaultBytes) {
		t.Fatal("bob does not see alice's avatar")
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
	hidden := e.do(t, request{method: http.MethodGet, path: "/users/" + strconv.FormatInt(aliceID, 10) + "/avatar", token: bob.access})
	if !bytes.Equal(hidden.Body.Bytes(), defaultBytes) {
		t.Fatal("hidden avatar was served")
	}
	if h := e.do(t, request{method: http.MethodGet, path: "/users/" + strconv.FormatInt(aliceID, 10) + "/avatars", token: bob.access}); !bytes.Contains(h.Body.Bytes(), []byte(`"items":[]`)) {
		t.Fatalf("hidden history: %s", h.Body)
	}
}
