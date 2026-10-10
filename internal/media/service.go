package media

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"os"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/store/postgres"
)

// Limits are the largest uploads per kind, in bytes.
type Limits struct {
	Image, Audio, Video, File, Avatar int64
}

// DefaultLimits are used for zero fields.
var DefaultLimits = Limits{Image: 20 << 20, Audio: 25 << 20, Video: 100 << 20, File: 100 << 20, Avatar: 10 << 20}

// Max is the largest limit, the most an upload request may carry.
func (l Limits) Max() int64 { return max(l.Image, l.Audio, l.Video, l.File, l.Avatar) }

func (l Limits) of(purpose, kind string) int64 {
	if purpose == PurposeAvatar {
		return l.Avatar
	}
	switch kind {
	case KindImage:
		return l.Image
	case KindAudio, KindVoice:
		return l.Audio
	case KindVideo:
		return l.Video
	}
	return l.File
}

// Errors of the service.
var (
	ErrTooLarge = errors.New("media: upload too large")
	// ErrNoAccess: the attachment does not exist or the viewer may not see it (not revealed which).
	ErrNoAccess = errors.New("media: no access")
	ErrInvalid  = errors.New("media: invalid upload")
)

// Visibility tells whether a viewer can see a message: it exists, is not deleted and not hidden for them.
type Visibility interface {
	Visible(ctx context.Context, viewerID, chatID, messageID int64) (bool, error)
}

// Membership tells whether a user is in a chat.
type Membership interface {
	IsMember(ctx context.Context, chatID, userID int64) (bool, error)
}

// Options configures the service.
type Options struct {
	Storage     Storage
	Attachments postgres.AttachmentRepository
	Visibility  Visibility
	Membership  Membership
	Prober      *Prober
	Limits      Limits
	// SignedURLs answers downloads with a redirect to object storage (when the backend can sign) instead of
	// streaming them.
	SignedURLs bool
	// SignedURLTTL is how long a redirect to object storage is valid (default 5 minutes).
	SignedURLTTL time.Duration
	Log          *slog.Logger
}

// Service stores uploads and serves them to the users allowed to see them.
type Service struct {
	o Options
}

// NewService returns the service.
func NewService(o Options) *Service {
	d := DefaultLimits
	for _, f := range []struct{ v, def *int64 }{{&o.Limits.Image, &d.Image}, {&o.Limits.Audio, &d.Audio}, {&o.Limits.Video, &d.Video}, {&o.Limits.File, &d.File}, {&o.Limits.Avatar, &d.Avatar}} {
		if *f.v <= 0 {
			*f.v = *f.def
		}
	}
	if o.SignedURLTTL <= 0 {
		o.SignedURLTTL = 5 * time.Minute
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	return &Service{o: o}
}

// Limits returns the upload limits.
func (s *Service) Limits() Limits { return s.o.Limits }

// Upload is one uploaded file.
type Upload struct {
	UploaderID int64
	Purpose    string // message or avatar
	// Kind is what the client says it sends (voice for a recorded message); the content decides in the end.
	Kind     string
	Filename string
	Body     io.Reader
	// DurationMS and Waveform are the client's measurements of a voice message, used only when the server
	// cannot measure it itself.
	DurationMS *int
	Waveform   []int
}

// cleanName keeps a display name for the file: the base name, printable, at most 255 bytes.
func cleanName(name, contentType string) string {
	name = path.Base(strings.ReplaceAll(name, "\\", "/"))
	var b strings.Builder
	for _, r := range name {
		if r >= 0x20 && r != 0x7f && r != '"' && r != '/' {
			b.WriteRune(r)
		}
	}
	name = strings.TrimSpace(b.String())
	for len(name) > 255 {
		_, size := utf8.DecodeLastRuneInString(name)
		name = name[:len(name)-size]
	}
	if name == "" || name == "." {
		name = "file"
		if exts, _ := mime.ExtensionsByType(contentType); len(exts) > 0 {
			name += exts[0]
		}
	}
	return name
}

// Upload checks and stores a file and records it. Nothing about the result depends on the file name or the
// client's content type.
func (s *Service) Upload(ctx context.Context, u Upload) (postgres.Attachment, error) {
	if u.Purpose != PurposeMessage && u.Purpose != PurposeAvatar {
		return postgres.Attachment{}, fmt.Errorf("%w: purpose must be message or avatar", ErrInvalid)
	}
	switch u.Kind {
	case "", KindImage, KindAudio, KindVoice, KindVideo, KindFile:
	default:
		return postgres.Attachment{}, fmt.Errorf("%w: unknown kind", ErrInvalid)
	}
	limit := s.o.Limits.Max()
	tmp, size, cleanup, err := spool(u.Body, limit)
	if err != nil {
		return postgres.Attachment{}, err
	}
	defer cleanup()
	if size > limit {
		return postgres.Attachment{}, ErrTooLarge
	}
	if size == 0 {
		return postgres.Attachment{}, fmt.Errorf("%w: empty file", ErrInvalid)
	}
	head := make([]byte, min(size, 512))
	if _, err := tmp.ReadAt(head, 0); err != nil && !errors.Is(err, io.EOF) {
		return postgres.Attachment{}, err
	}
	det, err := Detect(head, u.Purpose, u.Kind)
	if err != nil {
		return postgres.Attachment{}, err
	}
	if size > s.o.Limits.of(u.Purpose, det.Kind) {
		return postgres.Attachment{}, ErrTooLarge
	}

	id := uuid.New()
	prefix := "attachments/"
	if u.Purpose == PurposeAvatar {
		prefix = "avatars/"
	}
	rec := postgres.NewAttachment{
		UploaderID: &u.UploaderID, Purpose: u.Purpose, Kind: det.Kind, Filename: cleanName(u.Filename, det.ContentType),
		StorageKey: prefix + id.String(), MimeType: det.ContentType, Size: size,
	}
	var thumb []byte
	var body io.Reader
	switch det.Kind {
	case KindImage:
		data, err := os.ReadFile(tmp.Name())
		if err != nil {
			return postgres.Attachment{}, err
		}
		img, err := ProcessImage(data, det.ContentType)
		if err != nil {
			return postgres.Attachment{}, err
		}
		w, h := int32(img.Width), int32(img.Height) //nolint:gosec // bounded by MaxImageSide
		rec.Width, rec.Height, rec.MimeType, rec.Size = &w, &h, img.ContentType, int64(len(img.Data))
		body, thumb = bytes.NewReader(img.Data), img.Thumbnail
	case KindAudio, KindVoice:
		info := ClientAudio(u.DurationMS, u.Waveform)
		if s.o.Prober.Available() {
			if info, err = s.o.Prober.Probe(ctx, tmp.Name(), det.Kind == KindVoice); err != nil {
				if errors.Is(err, ErrNotAudio) {
					return postgres.Attachment{}, ErrUnsupported
				}
				return postgres.Attachment{}, err
			}
		}
		secs := info.Duration.Seconds()
		rec.Duration, rec.Waveform = &secs, info.Waveform
		fallthrough
	default:
		if _, err := tmp.Seek(0, io.SeekStart); err != nil {
			return postgres.Attachment{}, err
		}
		body = tmp
	}
	if err := s.o.Storage.Put(ctx, rec.StorageKey, body, rec.Size, rec.MimeType); err != nil {
		return postgres.Attachment{}, err
	}
	if thumb != nil {
		key := rec.StorageKey + ".thumb.jpg"
		if err := s.o.Storage.Put(ctx, key, bytes.NewReader(thumb), int64(len(thumb)), "image/jpeg"); err != nil {
			_ = s.o.Storage.Delete(context.WithoutCancel(ctx), rec.StorageKey)
			return postgres.Attachment{}, err
		}
		rec.ThumbnailKey = &key
	}
	a, err := s.o.Attachments.Create(ctx, rec)
	if err != nil {
		// The objects are unreferenced; remove them now rather than leaving them to nobody.
		for _, k := range append([]string{rec.StorageKey}, ptrList(rec.ThumbnailKey)...) {
			_ = s.o.Storage.Delete(context.WithoutCancel(ctx), k)
		}
		return postgres.Attachment{}, err
	}
	return a, nil
}

// spool gives the upload a file: a file the caller already spooled is used as it is (the caller removes it),
// anything else is copied into a temporary one (at most limit+1 bytes, so the caller can tell it was too large).
func spool(body io.Reader, limit int64) (*os.File, int64, func(), error) {
	if f, ok := body.(*os.File); ok {
		st, err := f.Stat()
		if err != nil {
			return nil, 0, nil, err
		}
		return f, st.Size(), func() {}, nil
	}
	tmp, err := os.CreateTemp("", "messenger-upload-*")
	if err != nil {
		return nil, 0, nil, err
	}
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}
	size, err := io.Copy(tmp, io.LimitReader(body, limit+1))
	if err != nil {
		cleanup()
		return nil, 0, nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return tmp, size, cleanup, nil
}

func ptrList(s *string) []string {
	if s == nil {
		return nil
	}
	return []string{*s}
}

// Download is how a client gets the bytes: a short-lived redirect to object storage, or the object to stream.
type Download struct {
	RedirectURL string
	Object      Object
	ContentType string
	// Disposition is the Content-Disposition header: inline only for images, audio and video.
	Disposition string
}

// CanSee reports whether the viewer may download the attachment: the uploader always; otherwise a member of a
// chat in which a message using it is visible to them (or, for a v1 attachment, a member of its chat).
func (s *Service) CanSee(ctx context.Context, viewerID int64, a postgres.Attachment) (bool, error) {
	if a.UploaderID != nil && *a.UploaderID == viewerID {
		return true, nil
	}
	if a.ChatID != nil {
		return s.o.Membership.IsMember(ctx, *a.ChatID, viewerID)
	}
	links, err := s.o.Attachments.LinksForViewer(ctx, a.ID, viewerID)
	if err != nil {
		return false, err
	}
	for _, l := range links {
		ok, err := s.o.Visibility.Visible(ctx, viewerID, l.ChatID, l.MessageID)
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

// Open returns the attachment's content (or its thumbnail) for the viewer. Avatars are served by the avatar
// endpoints, which apply the avatar visibility, so they are not reachable here except for their uploader.
func (s *Service) Open(ctx context.Context, viewerID int64, id uuid.UUID, thumbnail bool) (Download, error) {
	a, err := s.o.Attachments.Get(ctx, id)
	if errors.Is(err, postgres.ErrNotFound) {
		return Download{}, ErrNoAccess
	}
	if err != nil {
		return Download{}, err
	}
	var ok bool
	if a.Purpose == PurposeAvatar {
		ok, err = s.canSeeAvatar(ctx, viewerID, a)
	} else {
		ok, err = s.CanSee(ctx, viewerID, a)
	}
	if err != nil {
		return Download{}, err
	}
	if !ok {
		return Download{}, ErrNoAccess
	}
	return s.content(ctx, a, thumbnail)
}

// canSeeAvatar: the uploader, and the members of groups that use it. User avatars are served by the
// avatar endpoints instead, which apply the avatar visibility.
func (s *Service) canSeeAvatar(ctx context.Context, viewerID int64, a postgres.Attachment) (bool, error) {
	if a.UploaderID != nil && *a.UploaderID == viewerID {
		return true, nil
	}
	chats, err := s.o.Attachments.ChatsWithAvatar(ctx, a.ID)
	if err != nil {
		return false, err
	}
	for _, c := range chats {
		ok, err := s.o.Membership.IsMember(ctx, c, viewerID)
		if err != nil || ok {
			return ok, err
		}
	}
	return false, nil
}

// OpenUnchecked serves an attachment whose access was decided by the caller (avatars).
func (s *Service) OpenUnchecked(ctx context.Context, id uuid.UUID, thumbnail bool) (Download, error) {
	a, err := s.o.Attachments.Get(ctx, id)
	if errors.Is(err, postgres.ErrNotFound) {
		return Download{}, ErrNoAccess
	}
	if err != nil {
		return Download{}, err
	}
	return s.content(ctx, a, thumbnail)
}

func (s *Service) content(ctx context.Context, a postgres.Attachment, thumbnail bool) (Download, error) {
	key, ctype, name := a.StorageKey, a.MimeType, a.Filename
	if thumbnail {
		if a.ThumbnailKey == nil {
			return Download{}, ErrNoAccess
		}
		key, ctype, name = *a.ThumbnailKey, "image/jpeg", "thumbnail.jpg"
	}
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": name})
	if Inline(ctype) {
		disposition = mime.FormatMediaType("inline", map[string]string{"filename": name})
	} else {
		ctype = "application/octet-stream"
	}
	if disposition == "" { // a name FormatMediaType cannot encode
		disposition = "attachment"
	}
	if s.o.SignedURLs {
		url, err := s.o.Storage.SignedURL(ctx, key, s.o.SignedURLTTL, ctype, disposition)
		if err != nil {
			return Download{}, err
		}
		if url != "" {
			return Download{RedirectURL: url, ContentType: ctype, Disposition: disposition}, nil
		}
	}
	obj, err := s.o.Storage.Get(ctx, key)
	if errors.Is(err, ErrNotFound) {
		return Download{}, ErrNoAccess
	}
	if err != nil {
		return Download{}, err
	}
	return Download{Object: obj, ContentType: ctype, Disposition: disposition}, nil
}

// UnusedAfter is how long an upload may stay unused before it is deleted.
const UnusedAfter = 24 * time.Hour

// Collect deletes uploads nothing uses that are older than UnusedAfter: the row first (only if it is still
// unused), then the objects. It returns how many were deleted.
func (s *Service) Collect(ctx context.Context, batch int) (int, error) {
	list, err := s.o.Attachments.Unreferenced(ctx, time.Now().Add(-UnusedAfter), batch)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, a := range list {
		keys, deleted, err := s.o.Attachments.DeleteIfUnreferenced(ctx, a.ID)
		if err != nil {
			return n, err
		}
		if !deleted {
			continue
		}
		for _, k := range keys {
			if err := s.o.Storage.Delete(ctx, k); err != nil {
				s.o.Log.WarnContext(ctx, "delete unused object", "error", err)
			}
		}
		n++
	}
	return n, nil
}
