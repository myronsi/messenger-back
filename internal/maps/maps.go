package maps

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/myronsi/messenger-back/internal/media"
)

const (
	MaxZoom         = 19
	TileSize        = 256
	StaticZoom      = 16
	StaticWidth     = 640
	StaticHeight    = 320
	MinTileLifetime = 7 * 24 * time.Hour
	maxTileBytes    = 2 << 20
	signatureBytes  = 16
	DefaultTileURL  = "https://tile.openstreetmap.org/{z}/{x}/{y}.png"
)

var (
	ErrBadTile      = errors.New("maps: no such tile")
	ErrBadSignature = errors.New("maps: bad signature")
	ErrUnavailable  = errors.New("maps: tile provider unavailable")
)

type Store interface {
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	Get(ctx context.Context, key string) (media.Object, error)
}

type Options struct {
	TileURL   string
	UserAgent string
	Referer   string
	Key       []byte
	Store     Store
	Client    *http.Client
	Now       func() time.Time
}

type Service struct {
	Signer
	o     Options
	group singleflight.Group
}

type Signer struct{ key []byte }

func NewSigner(key []byte) Signer { return Signer{key: key} }

func New(o Options) *Service {
	if o.TileURL == "" {
		o.TileURL = DefaultTileURL
	}
	if o.Client == nil {
		o.Client = &http.Client{Timeout: 10 * time.Second}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Service{Signer: NewSigner(o.Key), o: o}
}

func DeriveKey(secret string) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("map-image"))
	return mac.Sum(nil)
}

func FormatCoordinate(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

func (s Signer) Sign(lat, lon float64) string {
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(FormatCoordinate(lat) + "," + FormatCoordinate(lon)))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:signatureBytes])
}

func (s Signer) Verify(lat, lon float64, sig string) bool {
	return len(s.key) > 0 && hmac.Equal([]byte(s.Sign(lat, lon)), []byte(sig))
}

func ValidTile(z, x, y int) bool {
	if z < 0 || z > MaxZoom || x < 0 || y < 0 {
		return false
	}
	n := 1 << z
	return x < n && y < n
}

type tileMeta struct {
	ETag    string    `json:"etag,omitempty"`
	Expires time.Time `json:"expires"`
}

func tileKey(z, x, y int) string { return fmt.Sprintf("maps/tiles/%d/%d/%d", z, x, y) }

func (s *Service) read(ctx context.Context, key string) ([]byte, error) {
	obj, err := s.o.Store.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	defer obj.Body.Close()
	return io.ReadAll(io.LimitReader(obj.Body, maxTileBytes+1))
}

func (s *Service) write(ctx context.Context, key, contentType string, data []byte) error {
	return s.o.Store.Put(ctx, key, bytes.NewReader(data), int64(len(data)), contentType)
}

func (s *Service) cached(ctx context.Context, z, x, y int) ([]byte, tileMeta, bool) {
	key := tileKey(z, x, y)
	raw, err := s.read(ctx, key+".json")
	if err != nil {
		return nil, tileMeta{}, false
	}
	var meta tileMeta
	if json.Unmarshal(raw, &meta) != nil {
		return nil, tileMeta{}, false
	}
	tile, err := s.read(ctx, key+".png")
	if err != nil {
		return nil, tileMeta{}, false
	}
	return tile, meta, true
}

func (s *Service) Tile(ctx context.Context, z, x, y int) ([]byte, error) {
	if !ValidTile(z, x, y) {
		return nil, ErrBadTile
	}
	tile, meta, ok := s.cached(ctx, z, x, y)
	if ok && s.o.Now().Before(meta.Expires) {
		return tile, nil
	}
	v, err, _ := s.group.Do(tileKey(z, x, y), func() (any, error) {
		return s.refresh(context.WithoutCancel(ctx), z, x, y, tile, meta, ok)
	})
	if err != nil {
		return nil, err
	}
	return v.([]byte), nil
}

func (s *Service) tileURL(z, x, y int) string {
	return strings.NewReplacer("{z}", strconv.Itoa(z), "{x}", strconv.Itoa(x), "{y}", strconv.Itoa(y)).Replace(s.o.TileURL)
}

func (s *Service) refresh(ctx context.Context, z, x, y int, stale []byte, meta tileMeta, have bool) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.tileURL(z, x, y), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", s.o.UserAgent)
	if s.o.Referer != "" {
		req.Header.Set("Referer", s.o.Referer)
	}
	if have && meta.ETag != "" {
		req.Header.Set("If-None-Match", meta.ETag)
	}
	resp, err := s.o.Client.Do(req)
	if err != nil {
		return staleOr(stale, have, err)
	}
	defer resp.Body.Close()
	expires := s.expiry(resp.Header)
	switch {
	case resp.StatusCode == http.StatusNotModified && have:
		meta.Expires = expires
		s.saveMeta(ctx, z, x, y, meta)
		return stale, nil
	case resp.StatusCode != http.StatusOK:
		return staleOr(stale, have, fmt.Errorf("tile %d/%d/%d: status %d", z, x, y, resp.StatusCode))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxTileBytes+1))
	if err != nil || len(data) > maxTileBytes {
		return staleOr(stale, have, fmt.Errorf("tile %d/%d/%d: read: %w", z, x, y, err))
	}
	if err := s.write(ctx, tileKey(z, x, y)+".png", "image/png", data); err == nil {
		s.saveMeta(ctx, z, x, y, tileMeta{ETag: resp.Header.Get("ETag"), Expires: expires})
	}
	return data, nil
}

func staleOr(stale []byte, have bool, err error) ([]byte, error) {
	if have {
		return stale, nil
	}
	return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
}

func (s *Service) saveMeta(ctx context.Context, z, x, y int, meta tileMeta) {
	if raw, err := json.Marshal(meta); err == nil {
		_ = s.write(ctx, tileKey(z, x, y)+".json", "application/json", raw)
	}
}

func (s *Service) expiry(h http.Header) time.Time {
	now := s.o.Now()
	lifetime := time.Duration(0)
	for _, part := range strings.Split(h.Get("Cache-Control"), ",") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(part), "max-age="); ok {
			if secs, err := strconv.Atoi(v); err == nil {
				lifetime = time.Duration(secs) * time.Second
			}
		}
	}
	if lifetime == 0 {
		if at, err := http.ParseTime(h.Get("Expires")); err == nil {
			lifetime = at.Sub(now)
		}
	}
	return now.Add(max(lifetime, MinTileLifetime))
}
