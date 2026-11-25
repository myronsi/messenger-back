package maps

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/myronsi/messenger-back/internal/media"
)

type memStore struct {
	mu   sync.Mutex
	data map[string][]byte
}

func (m *memStore) Put(_ context.Context, key string, r io.Reader, _ int64, _ string) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = b
	return nil
}

func (m *memStore) Get(_ context.Context, key string) (media.Object, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.data[key]
	if !ok {
		return media.Object{}, media.ErrNotFound
	}
	return media.Object{Body: io.NopCloser(bytes.NewReader(b)), Size: int64(len(b))}, nil
}

func pngTile(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, TileSize, TileSize))
	for y := range TileSize {
		for x := range TileSize {
			img.Set(x, y, color.RGBA{0x40, 0x80, 0xc0, 0xff})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

type upstream struct {
	calls       atomic.Int32
	conditional atomic.Int32
	down        atomic.Bool
	agent       atomic.Value
	tile        []byte
}

func newUpstream(t *testing.T) (*upstream, *httptest.Server) {
	u := &upstream{tile: pngTile(t)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.calls.Add(1)
		u.agent.Store(r.Header.Get("User-Agent"))
		if u.down.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if r.Header.Get("If-None-Match") == `"v1"` {
			u.conditional.Add(1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Cache-Control", "max-age=60")
		_, _ = w.Write(u.tile)
	}))
	t.Cleanup(srv.Close)
	return u, srv
}

func TestTilesAreCachedRevalidatedAndServedStale(t *testing.T) {
	up, srv := newUpstream(t)
	now := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	clock := &now
	s := New(Options{TileURL: srv.URL + "/{z}/{x}/{y}.png", UserAgent: "MessengerMaps/test", Store: &memStore{data: map[string][]byte{}}, Now: func() time.Time { return *clock }})
	ctx := context.Background()

	if _, err := s.Tile(ctx, 3, 8, 1); err != ErrBadTile {
		t.Fatalf("tile outside the grid: %v", err)
	}
	for range 3 {
		if b, err := s.Tile(ctx, 3, 2, 1); err != nil || !bytes.Equal(b, up.tile) {
			t.Fatalf("tile: %v", err)
		}
	}
	if up.calls.Load() != 1 || up.agent.Load() != "MessengerMaps/test" {
		t.Fatalf("upstream calls %d, agent %v", up.calls.Load(), up.agent.Load())
	}

	later := now.Add(MinTileLifetime + time.Hour)
	clock = &later
	if _, err := s.Tile(ctx, 3, 2, 1); err != nil || up.conditional.Load() != 1 {
		t.Fatalf("revalidation: %v, conditional %d", err, up.conditional.Load())
	}
	muchLater := later.Add(MinTileLifetime + time.Hour)
	clock = &muchLater
	up.down.Store(true)
	if b, err := s.Tile(ctx, 3, 2, 1); err != nil || !bytes.Equal(b, up.tile) {
		t.Fatalf("stale tile when the provider is down: %v", err)
	}
	if _, err := s.Tile(ctx, 3, 3, 1); err == nil {
		t.Fatal("an uncached tile while the provider is down must fail")
	}
}

func TestStaticMapNeedsASignatureAndIsCached(t *testing.T) {
	up, srv := newUpstream(t)
	s := New(Options{TileURL: srv.URL + "/{z}/{x}/{y}.png", UserAgent: "x", Key: DeriveKey("secret"), Store: &memStore{data: map[string][]byte{}}})
	ctx := context.Background()
	lat, lon := 52.520008, 13.404954

	if _, err := s.Static(ctx, lat, lon, "forged"); err != ErrBadSignature {
		t.Fatalf("forged signature: %v", err)
	}
	signer := NewSigner(DeriveKey("secret"))
	sig := signer.Sign(lat, lon)
	if !strings.EqualFold(sig, s.Sign(lat, lon)) || signer.Verify(lat, lon+0.000001, sig) {
		t.Fatal("signatures must match the point exactly")
	}
	img, err := s.Static(ctx, lat, lon, sig)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := jpeg.Decode(bytes.NewReader(img))
	if err != nil || decoded.Bounds().Dx() != StaticWidth || decoded.Bounds().Dy() != StaticHeight {
		t.Fatalf("static image: %v %v", decoded.Bounds(), err)
	}
	calls := up.calls.Load()
	if again, err := s.Static(ctx, lat, lon, sig); err != nil || !bytes.Equal(again, img) || up.calls.Load() != calls {
		t.Fatalf("second request must come from the cache: %v, calls %d → %d", err, calls, up.calls.Load())
	}
	if _, err := s.Static(ctx, 89.9, -179.99, s.Sign(89.9, -179.99)); err != nil {
		t.Fatalf("a point near the edge of the map: %v", err)
	}
}
