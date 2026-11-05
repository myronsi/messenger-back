package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/myronsi/messenger-back/internal/linkpreview"
	"github.com/myronsi/messenger-back/internal/store/redis"
)

var (
	RateLinkPreview      = redis.Rate{Name: "link_preview", Rate: 60, Period: time.Minute, Burst: 30}
	RateLinkPreviewImage = redis.Rate{Name: "link_preview_image", Rate: 120, Period: time.Minute, Burst: 60}
)

type LinkPreviewOptions struct {
	Service  *linkpreview.Service
	Limiter  RateAllower
	BasePath string
	Log      *slog.Logger
}

type LinkPreviewServer struct {
	o LinkPreviewOptions
}

func NewLinkPreviewServer(o LinkPreviewOptions) *LinkPreviewServer {
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	return &LinkPreviewServer{o: o}
}

func (l *LinkPreviewServer) GetLinkPreview(w http.ResponseWriter, r *http.Request, params GetLinkPreviewParams) {
	p, ok := principalOr401(w, r)
	if !ok || !allow(w, r, l.o.Limiter, RateLinkPreview, strconv.FormatInt(p.UserID, 10), l.o.Log) {
		return
	}
	preview, err := l.o.Service.Preview(r.Context(), params.Url)
	switch {
	case errors.Is(err, linkpreview.ErrInvalidURL):
		WriteProblem(w, http.StatusUnprocessableEntity, ErrorCodeValidationFailed, ProblemError{Field: "url", Message: "a public http or https address"})
		return
	case err != nil:
		WriteProblem(w, http.StatusNotFound, ErrorCodeNotFound)
		return
	}
	out := LinkPreview{Url: preview.URL, Title: optional(preview.Title), Description: optional(preview.Description), SiteName: optional(preview.SiteName)}
	if preview.Image != nil {
		image := LinkPreviewImage{
			Url: l.o.BasePath + "/link-preview/image?url=" + url.QueryEscape(preview.Image.URL) + "&sig=" + l.o.Service.Sign(preview.Image.URL),
		}
		if preview.Image.Width > 0 && preview.Image.Height > 0 {
			image.Width, image.Height = &preview.Image.Width, &preview.Image.Height
		}
		out.Image = &image
	}
	w.Header().Set("Cache-Control", "private, max-age=3600")
	writeJSONStatus(w, http.StatusOK, out)
}

func (l *LinkPreviewServer) GetLinkPreviewImage(w http.ResponseWriter, r *http.Request, params GetLinkPreviewImageParams) {
	p, ok := principalOr401(w, r)
	if !ok || !allow(w, r, l.o.Limiter, RateLinkPreviewImage, strconv.FormatInt(p.UserID, 10), l.o.Log) {
		return
	}
	body, contentType, err := l.o.Service.Image(r.Context(), params.Url, params.Sig)
	switch {
	case errors.Is(err, linkpreview.ErrBadSignature):
		WriteProblem(w, http.StatusForbidden, ErrorCodeForbidden)
		return
	case err != nil:
		WriteProblem(w, http.StatusNotFound, ErrorCodeNotFound)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
