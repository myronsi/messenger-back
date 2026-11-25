package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/myronsi/messenger-back/internal/maps"
	"github.com/myronsi/messenger-back/internal/store/redis"
)

var RateMapTiles = redis.Rate{Name: "map_tiles", Rate: 600, Period: time.Minute, Burst: 200}

type MapServer struct {
	service *maps.Service
	limiter RateAllower
	log     *slog.Logger
}

func NewMapServer(service *maps.Service, limiter RateAllower, log *slog.Logger) *MapServer {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &MapServer{service: service, limiter: limiter, log: log}
}

func writeImage(w http.ResponseWriter, contentType, cache string, body []byte) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("Cache-Control", cache)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func mapError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, maps.ErrBadTile):
		WriteProblem(w, http.StatusBadRequest, ErrorCodeInvalidRequest, ProblemError{Message: "no such tile"})
	case errors.Is(err, maps.ErrBadSignature):
		WriteProblem(w, http.StatusForbidden, ErrorCodeForbidden)
	default:
		w.Header().Set("Retry-After", "30")
		WriteProblem(w, http.StatusServiceUnavailable, ErrorCodeInternalError)
	}
}

func (m *MapServer) GetMapTile(w http.ResponseWriter, r *http.Request, z, x, y int, _ GetMapTileParams) {
	p, ok := principalOr401(w, r)
	if !ok || !allow(w, r, m.limiter, RateMapTiles, strconv.FormatInt(p.UserID, 10), m.log) {
		return
	}
	body, err := m.service.Tile(r.Context(), z, x, y)
	if err != nil {
		mapError(w, err)
		return
	}
	writeImage(w, "image/png", "private, max-age=86400", body)
}

func (m *MapServer) GetStaticMap(w http.ResponseWriter, r *http.Request, params GetStaticMapParams) {
	if _, ok := principalOr401(w, r); !ok {
		return
	}
	body, err := m.service.Static(r.Context(), params.Lat, params.Lon, params.Sig)
	if err != nil {
		mapError(w, err)
		return
	}
	writeImage(w, "image/jpeg", "private, max-age=31536000, immutable", body)
}
