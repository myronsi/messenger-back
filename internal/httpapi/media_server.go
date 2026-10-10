package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/media"
	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/redis"
	"github.com/myronsi/messenger-back/internal/users"
)

// MediaStore is the PostgreSQL side of the media endpoints.
type MediaStore interface {
	Users() postgres.UserRepository
	Attachments() postgres.AttachmentRepository
}

// RateAllower limits an action per subject.
type RateAllower interface {
	Allow(ctx context.Context, r redis.Rate, subject string) (redis.Result, error)
}

// RateUpload limits uploads per user.
var RateUpload = redis.Rate{Name: "upload", Rate: 60, Period: time.Minute, Burst: 20}

// RateSearch limits user searches per user.
var RateSearch = redis.Rate{Name: "user_search", Rate: 60, Period: time.Minute, Burst: 20}

// allow applies a rate limit and answers the request when it is over: 429 with Retry-After, or 503 when the
// limiter itself failed (Redis), which is no reason to blame the client. A nil limiter allows everything.
func allow(w http.ResponseWriter, r *http.Request, l RateAllower, rate redis.Rate, subject string, log *slog.Logger) bool {
	if l == nil {
		return true
	}
	res, err := l.Allow(r.Context(), rate, subject)
	if err != nil {
		log.WarnContext(r.Context(), "rate limit", "rate", rate.Name, "error", err)
		w.Header().Set("Retry-After", "5")
		WriteProblem(w, http.StatusServiceUnavailable, ErrorCodeInternalError)
		return false
	}
	if !res.Allowed {
		w.Header().Set("Retry-After", strconv.Itoa(int(res.RetryAfter/time.Second)+1))
		WriteProblem(w, http.StatusTooManyRequests, ErrorCodeRateLimited)
		return false
	}
	return true
}

// MediaOptions configures the media endpoints.
type MediaOptions struct {
	Service   *media.Service
	Store     MediaStore
	Directory *users.Directory
	Limiter   RateAllower
	BasePath  string
	Log       *slog.Logger
}

// MediaServer implements uploads, downloads and avatars.
type MediaServer struct {
	o MediaOptions
}

// NewMediaServer returns the media endpoints.
func NewMediaServer(o MediaOptions) *MediaServer {
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	return &MediaServer{o: o}
}

// UploadPath is the route of uploads, which gets the upload size limit instead of HTTP_MAX_BODY_BYTES.
func UploadPath(basePath string) string { return basePath + "/attachments" }

// uploadForm is the multipart request: the file part is spooled to a temporary file.
type uploadForm struct {
	file       *os.File
	filename   string
	purpose    string
	kind       string
	durationMS *int
	waveform   []int
}

func (f *uploadForm) close() {
	if f.file != nil {
		_ = f.file.Close()
		_ = os.Remove(f.file.Name())
	}
}

var errFormInvalid = errors.New("invalid upload form")

// formError keeps a body over the size limit recognisable (413); any other failure to read the form is the
// client's (malformed multipart, a truncated body, an abort or a read timeout).
func formError(err error) error {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return err
	}
	return fmt.Errorf("%w: %w", errFormInvalid, err)
}

func readUploadForm(r *http.Request, limit int64) (*uploadForm, error) {
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, errFormInvalid
	}
	f := &uploadForm{}
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			f.close()
			return nil, formError(err)
		}
		name := part.FormName()
		if name == "file" {
			if f.file != nil {
				f.close()
				return nil, errFormInvalid
			}
			tmp, err := os.CreateTemp("", "messenger-upload-*")
			if err != nil {
				return nil, err
			}
			f.file, f.filename = tmp, part.FileName()
			n, err := io.Copy(tmp, io.LimitReader(part, limit+1))
			if err != nil {
				f.close()
				return nil, formError(err)
			}
			if n > limit {
				f.close()
				return nil, media.ErrTooLarge
			}
			if _, err := tmp.Seek(0, io.SeekStart); err != nil {
				f.close()
				return nil, err
			}
			continue
		}
		value, err := io.ReadAll(io.LimitReader(part, 4096))
		if err != nil {
			f.close()
			return nil, errFormInvalid
		}
		v := strings.TrimSpace(string(value))
		switch name {
		case "purpose":
			f.purpose = v
		case "kind":
			f.kind = v
		case "duration_ms":
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				f.close()
				return nil, errFormInvalid
			}
			f.durationMS = &n
		case "waveform":
			// A JSON array, or one value per field.
			if strings.HasPrefix(v, "[") {
				var arr []int
				if json.Unmarshal(value, &arr) != nil || len(arr) > 128 {
					f.close()
					return nil, errFormInvalid
				}
				f.waveform = arr
			} else if n, err := strconv.Atoi(v); err == nil && len(f.waveform) < 128 {
				f.waveform = append(f.waveform, n)
			}
		}
	}
	if f.file == nil || (f.purpose != string(UploadPurposeMessage) && f.purpose != string(UploadPurposeAvatar)) {
		f.close()
		return nil, errFormInvalid
	}
	return f, nil
}

// UploadAttachment implements POST /attachments.
func (m *MediaServer) UploadAttachment(w http.ResponseWriter, r *http.Request, _ UploadAttachmentParams) {
	p, ok := PrincipalFrom(r.Context())
	if !ok {
		writeUnauthorized(w, ErrorCodeUnauthenticated)
		return
	}
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "multipart/form-data" {
		WriteProblem(w, http.StatusUnsupportedMediaType, ErrorCodeUnsupportedMediaType)
		return
	}
	if !allow(w, r, m.o.Limiter, RateUpload, strconv.FormatInt(p.UserID, 10), m.o.Log) {
		return
	}
	form, err := readUploadForm(r, m.o.Service.Limits().Max())
	if err != nil {
		m.uploadFailed(w, r, err)
		return
	}
	defer form.close()
	a, err := m.o.Service.Upload(r.Context(), media.Upload{
		UploaderID: p.UserID, Purpose: form.purpose, Kind: form.kind, Filename: form.filename,
		Body: form.file, DurationMS: form.durationMS, Waveform: form.waveform,
	})
	if err != nil {
		m.uploadFailed(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusCreated, PresentAttachment(a, "", m.o.BasePath))
}

func (m *MediaServer) uploadFailed(w http.ResponseWriter, r *http.Request, err error) {
	var tooLarge *http.MaxBytesError
	switch {
	case errors.Is(err, media.ErrTooLarge), errors.Is(err, media.ErrImageTooLarge), errors.As(err, &tooLarge):
		WriteProblem(w, http.StatusRequestEntityTooLarge, ErrorCodePayloadTooLarge)
	case errors.Is(err, media.ErrUnsupported):
		WriteProblem(w, http.StatusUnsupportedMediaType, ErrorCodeUnsupportedMediaType)
	case errors.Is(err, media.ErrInvalid), errors.Is(err, errFormInvalid):
		WriteProblem(w, http.StatusBadRequest, ErrorCodeInvalidRequest)
	case errors.Is(err, media.ErrBusy):
		w.Header().Set("Retry-After", "10")
		WriteProblem(w, http.StatusServiceUnavailable, ErrorCodeInternalError)
	default:
		m.o.Log.ErrorContext(r.Context(), "upload failed", "error", err)
		WriteProblem(w, http.StatusInternalServerError, ErrorCodeInternalError)
	}
}

// serve writes a download: a redirect to object storage, or the bytes with headers no browser can turn
// into a page. Byte ranges are served (players need them to seek, and Safari to play at all); etag names
// the stored bytes, which never change.
func (m *MediaServer) serve(w http.ResponseWriter, r *http.Request, d media.Download, cache, etag string) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	if d.RedirectURL != "" {
		// The signed URL expires within minutes, so the redirect must not outlive it in a cache.
		h.Set("Cache-Control", "no-store")
		http.Redirect(w, r, d.RedirectURL, http.StatusFound)
		return
	}
	defer func() { _ = d.Object.Body.Close() }()
	h.Set("Cache-Control", cache)
	h.Set("Content-Type", d.ContentType)
	h.Set("Content-Disposition", d.Disposition)
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	if etag != "" {
		h.Set("ETag", `"`+etag+`"`)
	}
	if rs, ok := d.Object.Body.(io.ReadSeeker); ok {
		http.ServeContent(w, r, "", time.Time{}, rs)
		return
	}
	if d.Object.Size >= 0 {
		h.Set("Content-Length", strconv.FormatInt(d.Object.Size, 10))
	}
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = io.Copy(w, d.Object.Body)
	}
}

// GetAttachmentContent implements GET /attachments/{id}/content.
func (m *MediaServer) GetAttachmentContent(w http.ResponseWriter, r *http.Request, id AttachmentId, params GetAttachmentContentParams) {
	p, ok := PrincipalFrom(r.Context())
	if !ok {
		writeUnauthorized(w, ErrorCodeUnauthenticated)
		return
	}
	thumb := params.Variant != nil && *params.Variant == Thumbnail
	d, err := m.o.Service.Open(r.Context(), p.UserID, id, thumb)
	if errors.Is(err, media.ErrNoAccess) {
		WriteProblem(w, http.StatusNotFound, ErrorCodeNotFound)
		return
	}
	if err != nil {
		m.o.Log.ErrorContext(r.Context(), "open attachment", "error", err)
		WriteProblem(w, http.StatusInternalServerError, ErrorCodeInternalError)
		return
	}
	etag := id.String()
	if thumb {
		etag += "-thumb"
	}
	m.serve(w, r, d, "private, max-age=3600", etag)
}

// SetMyAvatar implements PUT /me/avatar.
func (m *MediaServer) SetMyAvatar(w http.ResponseWriter, r *http.Request, _ SetMyAvatarParams) {
	p, ok := PrincipalFrom(r.Context())
	if !ok {
		writeUnauthorized(w, ErrorCodeUnauthenticated)
		return
	}
	var body SetAvatarRequest
	if !decode(w, r, &body) {
		return
	}
	a, err := m.o.Store.Attachments().Get(r.Context(), body.AttachmentId)
	if errors.Is(err, postgres.ErrNotFound) || (err == nil && (a.Purpose != postgres.PurposeAvatar || a.UploaderID == nil || *a.UploaderID != p.UserID)) {
		WriteProblem(w, http.StatusBadRequest, ErrorCodeValidationFailed, ProblemError{Field: "attachment_id", Message: "not one of your avatar uploads"})
		return
	}
	if err == nil {
		id := body.AttachmentId
		err = m.o.Store.Attachments().SetUserAvatar(r.Context(), p.UserID, &id)
	}
	if err != nil {
		m.o.Log.ErrorContext(r.Context(), "set avatar", "error", err)
		WriteProblem(w, http.StatusInternalServerError, ErrorCodeInternalError)
		return
	}
	u, err := m.o.Store.Users().Get(r.Context(), p.UserID)
	if err != nil {
		WriteProblem(w, http.StatusInternalServerError, ErrorCodeInternalError)
		return
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, PresentMe(u, m.o.BasePath))
}

// avatarVisible tells whether the viewer may see the user's avatar, and returns the user.
func (m *MediaServer) avatarVisible(ctx context.Context, viewer, userID int64) (postgres.User, bool, error) {
	views, err := m.o.Directory.ForViewer(ctx, viewer, []int64{userID})
	if err != nil {
		return postgres.User{}, false, err
	}
	v := views[userID]
	if v.IsDeleted || v.AvatarURL == nil {
		return postgres.User{}, false, nil
	}
	u, err := m.o.Store.Users().Get(ctx, userID)
	if err != nil {
		return postgres.User{}, false, err
	}
	return u, true, nil
}

// GetUserAvatar implements GET /users/{id}/avatar.
func (m *MediaServer) GetUserAvatar(w http.ResponseWriter, r *http.Request, userID UserId, params GetUserAvatarParams) {
	p, ok := PrincipalFrom(r.Context())
	if !ok {
		writeUnauthorized(w, ErrorCodeUnauthenticated)
		return
	}
	uid, err := strconv.ParseInt(userID, 10, 64)
	if err != nil {
		WriteProblem(w, http.StatusNotFound, ErrorCodeNotFound)
		return
	}
	u, visible, err := m.avatarVisible(r.Context(), p.UserID, uid)
	if err != nil {
		m.o.Log.ErrorContext(r.Context(), "avatar visibility", "error", err)
		WriteProblem(w, http.StatusInternalServerError, ErrorCodeInternalError)
		return
	}
	var attachment *uuid.UUID
	if visible {
		attachment = u.AvatarAttachmentID
		if params.Version != nil {
			attachment = nil
			if vid, err := strconv.ParseInt(*params.Version, 10, 64); err == nil {
				if hist, err := m.o.Store.Attachments().AvatarHistory(r.Context(), uid, vid+1, 1); err == nil && len(hist) == 1 && hist[0].ID == vid {
					attachment = &hist[0].AttachmentID
				}
			}
		}
	}
	if attachment == nil {
		serveDefaultAvatar(w)
		return
	}
	d, err := m.o.Service.OpenUnchecked(r.Context(), *attachment, false)
	if errors.Is(err, media.ErrNoAccess) {
		serveDefaultAvatar(w)
		return
	}
	if err != nil {
		m.o.Log.ErrorContext(r.Context(), "open avatar", "error", err)
		WriteProblem(w, http.StatusInternalServerError, ErrorCodeInternalError)
		return
	}
	// Short: visibility can change, and the URL stays the same when the avatar does.
	m.serve(w, r, d, "private, max-age=60", attachment.String())
}

// ListUserAvatars implements GET /users/{id}/avatars.
func (m *MediaServer) ListUserAvatars(w http.ResponseWriter, r *http.Request, userID UserId, params ListUserAvatarsParams) {
	p, ok := PrincipalFrom(r.Context())
	if !ok {
		writeUnauthorized(w, ErrorCodeUnauthenticated)
		return
	}
	uid, err := strconv.ParseInt(userID, 10, 64)
	if err != nil {
		WriteProblem(w, http.StatusNotFound, ErrorCodeNotFound)
		return
	}
	limit := 50
	if params.Limit != nil {
		limit = min(max(*params.Limit, 1), 100)
	}
	var before int64
	if params.After != nil && *params.After != "" {
		if before, err = strconv.ParseInt(*params.After, 10, 64); err != nil || before <= 0 {
			WriteProblem(w, http.StatusBadRequest, ErrorCodeInvalidRequest)
			return
		}
	}
	type page struct {
		Items      []AvatarVersion `json:"items"`
		NextCursor *string         `json:"next_cursor"`
	}
	out := page{Items: []AvatarVersion{}}
	_, visible, err := m.avatarVisible(r.Context(), p.UserID, uid)
	if err != nil {
		WriteProblem(w, http.StatusInternalServerError, ErrorCodeInternalError)
		return
	}
	if visible {
		hist, err := m.o.Store.Attachments().AvatarHistory(r.Context(), uid, before, limit+1)
		if err != nil {
			WriteProblem(w, http.StatusInternalServerError, ErrorCodeInternalError)
			return
		}
		for i, h := range hist {
			if i == limit {
				next := strconv.FormatInt(hist[i-1].ID, 10)
				out.NextCursor = &next
				break
			}
			url := m.o.BasePath + "/users/" + userID + "/avatar?version=" + strconv.FormatInt(h.ID, 10)
			out.Items = append(out.Items, AvatarVersion{Id: FormatID(h.ID), Url: url, CreatedAt: h.CreatedAt.UTC()})
		}
	}
	writeJSONStatus(w, http.StatusOK, out)
}

var defaultAvatar = sync.OnceValue(func() []byte {
	const size = 128
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	bg, fg := color.RGBA{0xC8, 0xCE, 0xD6, 0xFF}, color.RGBA{0x8A, 0x94, 0xA3, 0xFF}
	for y := range size {
		for x := range size {
			c := bg
			dx, dy := x-size/2, y-size*2/5
			if dx*dx+dy*dy < (size/5)*(size/5) || (y > size*2/3 && dx*dx < (size*7/20)*(size*7/20)-(y-size)*(y-size)/2) {
				c = fg
			}
			img.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
})

func serveDefaultAvatar(w http.ResponseWriter) {
	data := defaultAvatar()
	h := w.Header()
	h.Set("Content-Type", "image/png")
	h.Set("Content-Length", strconv.Itoa(len(data)))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "private, max-age=60")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}
