package media

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/jpeg"
	"io"
	"log/slog"
	"mime"
	"os"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"

	"github.com/myronsi/messenger-back/internal/store/postgres"
)

// Limits are the largest uploads per kind, in bytes.
type Limits struct {
	Image, Audio, Video, File, Avatar, Sticker int64
}

// DefaultLimits are used for zero fields.
var DefaultLimits = Limits{Image: 20 << 20, Audio: 25 << 20, Video: 100 << 20, File: 100 << 20, Avatar: 10 << 20, Sticker: 5 << 20}

// Max is the largest limit, the most an upload request may carry.
func (l Limits) Max() int64 { return max(l.Image, l.Audio, l.Video, l.File, l.Avatar, l.Sticker) }

func (l Limits) of(purpose, kind string) int64 {
	switch purpose {
	case PurposeAvatar:
		return l.Avatar
	case PurposeSticker:
		return l.Sticker
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
	VisibleAttachment(ctx context.Context, viewerID, chatID, messageID int64, id uuid.UUID) (bool, error)
}

// Membership tells whether a user is in a chat.
type Membership interface {
	IsMember(ctx context.Context, chatID, userID int64) (bool, error)
}

type ProfileAccess interface {
	ProfileVisible(ctx context.Context, viewerID, subjectID int64) (bool, error)
}

// Options configures the service.
type Options struct {
	Storage     Storage
	Attachments postgres.AttachmentRepository
	Visibility  Visibility
	Membership  Membership
	Profiles    ProfileAccess
	Prober      *Prober
	Limits      Limits
	// SignedURLs answers downloads with a redirect to object storage (when the backend can sign) instead of
	// streaming them.
	SignedURLs bool
	// SignedURLTTL is how long a redirect to object storage is valid (default 5 minutes).
	SignedURLTTL time.Duration
	// ImageWorkers is how many images are decoded at once (default 2); each can take a few hundred MB.
	// Further uploads wait for a slot, within their request's deadline.
	ImageWorkers int
	Log          *slog.Logger
	Jobs         postgres.MediaJobRepository
	Sessions     postgres.UploadSessionRepository
}

// Service stores uploads and serves them to the users allowed to see them.
type Service struct {
	o      Options
	images chan struct{}
}

// ErrBusy is returned when an upload could not get a processing slot before its deadline.
var ErrBusy = errors.New("media: too many uploads are being processed")

// NewService returns the service.
func NewService(o Options) *Service {
	d := DefaultLimits
	for _, f := range []struct{ v, def *int64 }{{&o.Limits.Image, &d.Image}, {&o.Limits.Audio, &d.Audio}, {&o.Limits.Video, &d.Video}, {&o.Limits.File, &d.File}, {&o.Limits.Avatar, &d.Avatar}, {&o.Limits.Sticker, &d.Sticker}} {
		if *f.v <= 0 {
			*f.v = *f.def
		}
	}
	if o.SignedURLTTL <= 0 {
		o.SignedURLTTL = 5 * time.Minute
	}
	if o.ImageWorkers <= 0 {
		o.ImageWorkers = 2
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	return &Service{o: o, images: make(chan struct{}, o.ImageWorkers)}
}

// processImage decodes and re-encodes an image in one of the ImageWorkers slots.
func (s *Service) processImage(ctx context.Context, path, contentType, purpose string, vs *variantSet) (ProcessedImage, string, error) {
	select {
	case s.images <- struct{}{}:
	case <-ctx.Done():
		return ProcessedImage{}, "", ErrBusy
	}
	defer func() { <-s.images }()
	data, err := os.ReadFile(path) //nolint:gosec // our temporary file
	if err != nil {
		return ProcessedImage{}, "", err
	}
	img, err := ProcessImage(data, contentType)
	if err != nil {
		return ProcessedImage{}, "", err
	}
	if img.Animated && purpose != PurposeMessage {
		return ProcessedImage{}, "", ErrUnsupported
	}
	thumbType := "image/jpeg"
	if img.Source == nil {
		return img, thumbType, nil
	}
	if contentType == "image/webp" && !img.Animated {
		if webp, err := s.o.Prober.EncodeWebP(ctx, img.Source, WebPOptions{Quality: 90, Lossless: img.Lossless}); err == nil {
			img.Data, img.ContentType = webp, "image/webp"
		}
	}
	if t, err := s.o.Prober.Resized(ctx, img.Source, ThumbnailWebPSide, 75); err == nil {
		img.Thumbnail, thumbType = t.Data, t.ContentType
	}
	if !img.Animated && NeedsDisplay(img.ContentType, img.Width, img.Height) {
		s.addResized(ctx, vs, VariantNameDisplay, img.Source, DisplaySide, 80)
	}
	if purpose == PurposeAvatar {
		for _, side := range avatarSides {
			s.addResized(ctx, vs, AvatarVariant(side), img.Source, side, 82)
		}
	}
	img.Placeholder = Placeholder(img.Source)
	img.Source = nil
	return img, thumbType, nil
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
	StartMS    *int
	EndMS      *int
	Crop       *StickerCrop
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
	a, _, err := s.UploadWithTags(ctx, u)
	return a, err
}

func (s *Service) UploadWithTags(ctx context.Context, u Upload) (postgres.Attachment, Tags, error) {
	var tags Tags
	a, err := s.upload(ctx, u, &tags)
	return a, tags, err
}

func (s *Service) upload(ctx context.Context, u Upload, tags *Tags) (postgres.Attachment, error) {
	switch u.Purpose {
	case PurposeMessage, PurposeAvatar:
	case PurposeSticker:
		if u.Kind != "" && u.Kind != KindImage && u.Kind != KindVideo {
			return postgres.Attachment{}, fmt.Errorf("%w: a sticker is made from an image or a video", ErrInvalid)
		}
	case PurposeProfileMusic:
		if u.Kind != "" && u.Kind != KindAudio {
			return postgres.Attachment{}, fmt.Errorf("%w: profile music is audio", ErrInvalid)
		}
	case PurposeProfileBackground:
		if u.Kind != "" && u.Kind != KindImage && u.Kind != KindVideo {
			return postgres.Attachment{}, fmt.Errorf("%w: a background is an image or a video", ErrInvalid)
		}
	case PurposeEmoji:
		if u.Kind != "" && u.Kind != KindImage {
			return postgres.Attachment{}, fmt.Errorf("%w: an emoji is an image", ErrInvalid)
		}
	case PurposeChatWallpaper:
		if u.Kind != "" && u.Kind != KindImage {
			return postgres.Attachment{}, fmt.Errorf("%w: a chat background is an image", ErrInvalid)
		}
	default:
		return postgres.Attachment{}, fmt.Errorf("%w: unknown purpose", ErrInvalid)
	}
	if (u.StartMS != nil || u.EndMS != nil) && u.Purpose != PurposeProfileMusic && u.Purpose != PurposeSticker {
		return postgres.Attachment{}, fmt.Errorf("%w: start_ms and end_ms are for profile music and stickers", ErrInvalid)
	}
	if u.Crop != nil && u.Purpose != PurposeSticker {
		return postgres.Attachment{}, fmt.Errorf("%w: crop is for stickers", ErrInvalid)
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
	var det Detected
	switch {
	case u.Purpose == PurposeEmoji:
		det, err = detectEmoji(head)
	case isProfilePurpose(u.Purpose):
		det, err = detectProfile(head, u.Purpose)
	default:
		det, err = Detect(head, u.Purpose, u.Kind)
	}
	if err != nil {
		return postgres.Attachment{}, err
	}
	origSize, origType := size, det.ContentType
	if IsHEIF(det.ContentType) {
		var swap func()
		tmp, size, det, swap = s.convertHEIF(ctx, tmp, size, det)
		defer swap()
	}
	if det.ContentType == "video/webm" {
		det = s.webmKind(ctx, tmp.Name(), det)
		if (u.Purpose == PurposeProfileBackground || u.Purpose == PurposeSticker) && det.Kind != KindVideo {
			return postgres.Attachment{}, ErrUnsupported
		}
	}
	animatedSticker := u.Purpose == PurposeSticker && AnimatedStickerSource(det)
	if u.Purpose == PurposeSticker && !animatedSticker && (u.StartMS != nil || u.EndMS != nil || u.Crop != nil) {
		return postgres.Attachment{}, fmt.Errorf("%w: start_ms, end_ms and crop are for stickers from a video or GIF", ErrInvalid)
	}
	limitOf := s.o.Limits.of(u.Purpose, det.Kind)
	if animatedSticker {
		limitOf = MaxAnimatedStickerSource
	}
	if isProfilePurpose(u.Purpose) {
		limitOf = profileLimit(u.Purpose, det)
	}
	if origSize > limitOf {
		return postgres.Attachment{}, ErrTooLarge
	}

	id := uuid.New()
	prefix := "attachments/"
	switch u.Purpose {
	case PurposeAvatar:
		prefix = "avatars/"
	case PurposeSticker:
		prefix = "stickers/"
	case PurposeProfileMusic, PurposeProfileBackground:
		prefix = "profiles/"
	case PurposeEmoji:
		prefix = "emoji/"
	case PurposeChatWallpaper:
		prefix = "wallpapers/"
	}
	rec := postgres.NewAttachment{
		UploaderID: &u.UploaderID, Purpose: u.Purpose, Kind: det.Kind, Filename: cleanName(u.Filename, det.ContentType),
		StorageKey: prefix + id.String(), MimeType: det.ContentType, Size: size,
	}
	vs := newVariantSet(rec.StorageKey)
	var jobs []string
	var thumb []byte
	thumbType := "image/jpeg"
	var body io.Reader
	switch {
	case u.Purpose == PurposeEmoji:
		img, err := s.processEmoji(ctx, tmp, size, det.ContentType, vs)
		if err != nil {
			return postgres.Attachment{}, err
		}
		w, h := int32(img.Width), int32(img.Height) //nolint:gosec // at most EmojiSide
		rec.Width, rec.Height, rec.MimeType, rec.Size = &w, &h, img.ContentType, int64(len(img.Data))
		rec.Filename = "emoji" + extensionOf(img.ContentType)
		body = bytes.NewReader(img.Data)
	case u.Purpose == PurposeProfileMusic:
		clip, err := s.processMusic(ctx, tmp.Name(), u.StartMS, u.EndMS)
		if err != nil {
			if errors.Is(err, ErrNotAudio) {
				return postgres.Attachment{}, ErrUnsupported
			}
			if errors.Is(err, ErrMeasureTimeout) {
				return postgres.Attachment{}, ErrBusy
			}
			return postgres.Attachment{}, err
		}
		secs := clip.duration.Seconds()
		rec.Kind, rec.MimeType, rec.Size, rec.Duration, rec.Filename = KindAudio, "audio/mp4", int64(len(clip.data)), &secs, "music.m4a"
		body, thumb, *tags = bytes.NewReader(clip.data), clip.cover, clip.tags
	case u.Purpose == PurposeProfileBackground && det.Kind == KindVideo:
		if d, ok := s.o.Prober.duration(ctx, tmp.Name()); ok && d > MaxBackgroundVideo+musicClipTolerance {
			return postgres.Attachment{}, fmt.Errorf("%w: a background video is at most 15 s", ErrInvalid)
		}
		thumb = s.processVideoThumbnail(ctx, tmp.Name(), det.ContentType)
		if frame, err := jpeg.DecodeConfig(bytes.NewReader(thumb)); err == nil {
			w, h := int32(frame.Width), int32(frame.Height) //nolint:gosec // bounded by the thumbnail filter
			rec.Width, rec.Height = &w, &h
		}
		if _, err := tmp.Seek(0, io.SeekStart); err != nil {
			return postgres.Attachment{}, err
		}
		body = tmp
	case animatedSticker:
		clip, err := s.processAnimatedSticker(ctx, tmp.Name(), u, vs)
		if err != nil {
			return postgres.Attachment{}, err
		}
		w, h, secs := int32(StickerSide), int32(StickerSide), clip.Duration.Seconds()
		rec.Kind, rec.Width, rec.Height, rec.MimeType, rec.Size = KindImage, &w, &h, "image/webp", int64(len(clip.Data))
		rec.Filename, rec.Duration = "sticker.webp", &secs
		body = bytes.NewReader(clip.Data)
	case u.Purpose == PurposeSticker:
		img, err := s.processSticker(ctx, tmp, size, det.ContentType, vs)
		if err != nil {
			return postgres.Attachment{}, err
		}
		w, h := int32(StickerSide), int32(StickerSide)
		rec.Width, rec.Height, rec.MimeType, rec.Size = &w, &h, img.ContentType, int64(len(img.Data))
		rec.Filename = "sticker" + map[string]string{"image/webp": ".webp", "image/png": ".png"}[img.ContentType]
		body = bytes.NewReader(img.Data)
	case det.Kind == KindImage:
		img, tt, err := s.processImage(ctx, tmp.Name(), det.ContentType, u.Purpose, vs)
		if err != nil {
			return postgres.Attachment{}, err
		}
		w, h := int32(img.Width), int32(img.Height) //nolint:gosec // bounded by MaxImageSide
		rec.Width, rec.Height, rec.MimeType, rec.Size = &w, &h, img.ContentType, int64(len(img.Data))
		if rec.MimeType != origType {
			rec.Filename = swapExtension(rec.Filename, rec.MimeType)
		}
		body, thumb, thumbType = bytes.NewReader(img.Data), img.Thumbnail, tt
		if img.Placeholder != "" {
			rec.Placeholder = &img.Placeholder
		}
		if img.ContentType == "image/gif" && u.Purpose == PurposeMessage {
			jobs = append(jobs, JobGIF)
		}
	case det.Kind == KindVideo:
		thumb = s.processVideoThumbnail(ctx, tmp.Name(), det.ContentType)
		if frame, err := jpeg.Decode(bytes.NewReader(thumb)); err == nil {
			w, h := int32(frame.Bounds().Dx()), int32(frame.Bounds().Dy()) //nolint:gosec
			rec.Width, rec.Height = &w, &h
			if ph := Placeholder(frame); ph != "" {
				rec.Placeholder = &ph
			}
		}
		if _, err := tmp.Seek(0, io.SeekStart); err != nil {
			return postgres.Attachment{}, err
		}
		body = tmp
		if u.Purpose == PurposeMessage {
			jobs = append(jobs, JobVideo)
		}
	case det.Kind == KindAudio || det.Kind == KindVoice:
		info := ClientAudio(u.DurationMS, u.Waveform)
		if s.o.Prober.Available() {
			if info, err = s.o.Prober.Probe(ctx, tmp.Name(), det.Kind == KindVoice); err != nil {
				if errors.Is(err, ErrNotAudio) {
					return postgres.Attachment{}, ErrUnsupported
				}
				if errors.Is(err, ErrMeasureTimeout) {
					return postgres.Attachment{}, ErrBusy
				}
				return postgres.Attachment{}, err
			}
		}
		secs := info.Duration.Seconds()
		rec.Duration, rec.Waveform = &secs, info.Waveform
		if det.Kind == KindAudio && u.Purpose == PurposeMessage && s.o.Prober.Available() {
			t := s.o.Prober.tags(ctx, tmp.Name())
			rec.AudioTitle, rec.AudioArtist, rec.AudioAlbum = t.Title, t.Artist, t.Album
			*tags = t
			thumb, thumbType = s.audioCover(ctx, tmp.Name())
			jobs = append(jobs, JobAudio)
		}
		fallthrough
	default:
		if _, err := tmp.Seek(0, io.SeekStart); err != nil {
			return postgres.Attachment{}, err
		}
		body = tmp
	}
	puts := []put{{rec.StorageKey, body, rec.Size, rec.MimeType}}
	if thumb != nil {
		key := rec.StorageKey + ".thumb" + extensionOf(thumbType)
		puts = append(puts, put{key, bytes.NewReader(thumb), int64(len(thumb)), thumbType})
		rec.ThumbnailKey = &key
	}
	puts = append(puts, vs.puts...)
	rec.Variants = vs.variants
	if len(jobs) > 0 && s.o.Jobs != nil {
		queued := ProcessingQueued
		rec.ProcessingState = &queued
	}
	if err := s.putAll(ctx, puts); err != nil {
		return postgres.Attachment{}, err
	}
	a, err := s.o.Attachments.Create(ctx, rec)
	if err != nil {
		// The objects are unreferenced; remove them now rather than leaving them to nobody.
		s.remove(ctx, puts)
		return postgres.Attachment{}, err
	}
	if rec.ProcessingState != nil {
		s.enqueue(ctx, &a, jobs)
	}
	return a, nil
}

func (s *Service) enqueue(ctx context.Context, a *postgres.Attachment, jobs []string) {
	for _, kind := range jobs {
		if _, err := s.o.Jobs.Enqueue(ctx, a.ID, kind); err != nil {
			s.o.Log.WarnContext(ctx, "enqueue media job", "attachment_id", a.ID.String(), "kind", kind, "error", err)
			if err := s.o.Attachments.SetProcessing(context.WithoutCancel(ctx), a.ID, nil); err == nil {
				a.ProcessingState = nil
			}
			return
		}
	}
}

func swapExtension(name, contentType string) string {
	ext := extensionOf(contentType)
	if ext == ".bin" {
		return name
	}
	if i := strings.LastIndexByte(name, '.'); i > 0 {
		name = name[:i]
	}
	return name + ext
}

func (s *Service) audioCover(ctx context.Context, path string) ([]byte, string) {
	cover := s.o.Prober.cover(ctx, path)
	if cover == nil {
		return nil, ""
	}
	if img, err := jpeg.Decode(bytes.NewReader(cover)); err == nil {
		if t, err := s.o.Prober.Resized(ctx, img, ThumbnailWebPSide, 75); err == nil {
			return t.Data, t.ContentType
		}
	}
	return cover, "image/jpeg"
}

type put struct {
	key         string
	body        io.Reader
	size        int64
	contentType string
}

func (s *Service) putAll(ctx context.Context, puts []put) error {
	g, gctx := errgroup.WithContext(ctx)
	for _, p := range puts {
		g.Go(func() error { return s.o.Storage.Put(gctx, p.key, p.body, p.size, p.contentType) })
	}
	if err := g.Wait(); err != nil {
		s.remove(ctx, puts)
		return err
	}
	return nil
}

func (s *Service) remove(ctx context.Context, puts []put) {
	for _, p := range puts {
		if err := s.o.Storage.Delete(context.WithoutCancel(ctx), p.key); err != nil {
			s.o.Log.WarnContext(ctx, "delete object of a failed upload", "key", p.key, "error", err)
		}
	}
}

func (s *Service) webmKind(ctx context.Context, path string, det Detected) Detected {
	video, audio, ok := s.o.Prober.Streams(ctx, path)
	if !ok {
		return det
	}
	return refineWebM(det, video, audio)
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

// Download is how a client gets the bytes: a short-lived redirect to object storage, or the object to stream.
type Download struct {
	RedirectURL string
	Object      Object
	ContentType string
	// Disposition is the Content-Disposition header: inline only for images, audio and video.
	Disposition string
	ETagSuffix  string
	Legacy      bool
}

// CanSee reports whether the viewer may download the attachment: the uploader always; otherwise a member of a
// chat in which a message using it is visible to them (or, for a v1 attachment, a member of its chat).
func (s *Service) CanSee(ctx context.Context, viewerID int64, a postgres.Attachment) (bool, error) {
	if a.UploaderID != nil && *a.UploaderID == viewerID {
		return true, nil
	}
	if a.ChatID != nil {
		// A v1 attachment belongs to its chat; forwarded copies link it into others, checked below.
		if ok, err := s.o.Membership.IsMember(ctx, *a.ChatID, viewerID); err != nil || ok {
			return ok, err
		}
	}
	links, err := s.o.Attachments.LinksForViewer(ctx, a.ID, viewerID)
	if err != nil {
		return false, err
	}
	for _, l := range links {
		ok, err := s.o.Visibility.VisibleAttachment(ctx, viewerID, l.ChatID, l.MessageID, a.ID)
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
func (s *Service) Open(ctx context.Context, viewerID int64, id uuid.UUID, v Variant) (Download, error) {
	a, err := s.Authorize(ctx, viewerID, id)
	if err != nil {
		return Download{}, err
	}
	return s.content(ctx, a, v)
}

func (s *Service) Authorize(ctx context.Context, viewerID int64, id uuid.UUID) (postgres.Attachment, error) {
	a, err := s.o.Attachments.Get(ctx, id)
	if errors.Is(err, postgres.ErrNotFound) {
		return postgres.Attachment{}, ErrNoAccess
	}
	if err != nil {
		return postgres.Attachment{}, err
	}
	var ok bool
	switch a.Purpose {
	case PurposeSticker:
		ok, err = s.canSeeSticker(ctx, viewerID, a)
	case PurposeAvatar:
		ok, err = s.canSeeAvatar(ctx, viewerID, a)
	case PurposeProfileMusic, PurposeProfileBackground:
		ok, err = s.canSeeProfileMedia(ctx, viewerID, a)
	case PurposeEmoji:
		ok, err = s.canSeeEmoji(ctx, viewerID, a)
	case PurposeChatWallpaper:
		ok, err = s.canSeeWallpaper(ctx, viewerID, a)
	default:
		ok, err = s.CanSee(ctx, viewerID, a)
	}
	if err != nil {
		return postgres.Attachment{}, err
	}
	if !ok {
		return postgres.Attachment{}, ErrNoAccess
	}
	return a, nil
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

func (s *Service) canSeeWallpaper(ctx context.Context, viewerID int64, a postgres.Attachment) (bool, error) {
	if a.UploaderID != nil && *a.UploaderID == viewerID {
		return true, nil
	}
	return s.o.Attachments.UsedAsWallpaperBy(ctx, a.ID, viewerID)
}

func (s *Service) canSeeSticker(ctx context.Context, viewerID int64, a postgres.Attachment) (bool, error) {
	if a.UploaderID != nil && *a.UploaderID == viewerID {
		return true, nil
	}
	return s.o.Attachments.IsStickerImage(ctx, a.ID)
}

// OpenUnchecked serves an attachment whose access was decided by the caller (avatars).
func (s *Service) OpenUnchecked(ctx context.Context, id uuid.UUID, v Variant) (Download, error) {
	a, err := s.o.Attachments.Get(ctx, id)
	if errors.Is(err, postgres.ErrNotFound) {
		return Download{}, ErrNoAccess
	}
	if err != nil {
		return Download{}, err
	}
	return s.content(ctx, a, v)
}

type Variant int

const (
	VariantOriginal Variant = iota
	VariantThumbnail
	VariantDisplay
	VariantSource
	VariantPanel
	VariantMessage
	VariantInline
	VariantAvatar64
	VariantAvatar128
	VariantAvatar256
	VariantAnimation
)

var variantNames = map[Variant]string{
	VariantDisplay: VariantNameDisplay, VariantPanel: VariantNamePanel, VariantMessage: VariantNameMessage,
	VariantInline: VariantNameInline, VariantAvatar64: AvatarVariant(64), VariantAvatar128: AvatarVariant(128),
	VariantAvatar256: AvatarVariant(256),
}

func AvatarSizeVariant(size int) Variant {
	switch AvatarVariantFor(size) {
	case AvatarVariant(64):
		return VariantAvatar64
	case AvatarVariant(128):
		return VariantAvatar128
	case AvatarVariant(256):
		return VariantAvatar256
	}
	return VariantOriginal
}

func HasOptimized(a postgres.Attachment) bool {
	_, ok := a.Variants.Get(VariantNameOptimized)
	return ok
}

func HasDisplay(a postgres.Attachment) bool {
	return a.Kind != KindVideo && a.Width != nil && a.Height != nil && NeedsDisplay(a.MimeType, int(*a.Width), int(*a.Height))
}

func (s *Service) content(ctx context.Context, a postgres.Attachment, v Variant) (Download, error) {
	key, ctype, name := a.StorageKey, a.MimeType, a.Filename
	if vname, ok := variantNames[v]; ok {
		if sv, ok := a.Variants.Get(vname); ok {
			d, err := s.download(ctx, sv.Key, sv.ContentType, vname+extensionOf(sv.ContentType), nil)
			d.ETagSuffix = "-" + vname
			return d, err
		}
	}
	switch {
	case v == VariantOriginal && HasOptimized(a):
		sv, _ := a.Variants.Get(VariantNameOptimized)
		d, err := s.download(ctx, sv.Key, sv.ContentType, swapExtension(a.Filename, sv.ContentType), nil)
		d.ETagSuffix = "-optimized"
		return d, err
	case v == VariantAnimation:
		if sv, ok := a.Variants.Get(VariantNameAnimation); ok {
			d, err := s.download(ctx, sv.Key, sv.ContentType, "animation.mp4", nil)
			d.ETagSuffix = "-animation"
			return d, err
		}
	case v == VariantThumbnail:
		if a.ThumbnailKey == nil {
			d, err := s.legacyVideoThumbnail(ctx, a)
			d.Legacy = true
			return d, err
		}
		ctype = thumbnailContentType(*a.ThumbnailKey)
		key, name = *a.ThumbnailKey, "thumbnail"+extensionOf(ctype)
	case v == VariantDisplay && HasDisplay(a):
		obj, err := s.displayObject(ctx, a)
		if err == nil {
			return s.download(ctx, postgres.DisplayKey(a.StorageKey), "image/jpeg", "display.jpg", &obj)
		}
		if !errors.Is(err, ErrUnsupported) && !errors.Is(err, ErrImageTooLarge) {
			return Download{}, err
		}
		s.o.Log.WarnContext(ctx, "display copy", "attachment_id", a.ID.String(), "error", err)
	}
	return s.download(ctx, key, ctype, name, nil)
}

func (s *Service) displayObject(ctx context.Context, a postgres.Attachment) (Object, error) {
	key := postgres.DisplayKey(a.StorageKey)
	obj, err := s.o.Storage.Get(ctx, key)
	if !errors.Is(err, ErrNotFound) {
		return obj, err
	}
	select {
	case s.images <- struct{}{}:
	case <-ctx.Done():
		return Object{}, ErrBusy
	}
	defer func() { <-s.images }()
	if obj, err := s.o.Storage.Get(ctx, key); !errors.Is(err, ErrNotFound) {
		return obj, err
	}
	src, err := s.o.Storage.Get(ctx, a.StorageKey)
	if errors.Is(err, ErrNotFound) {
		return Object{}, ErrNoAccess
	}
	if err != nil {
		return Object{}, err
	}
	limit := max(a.Size, s.o.Limits.Image)
	data, err := io.ReadAll(io.LimitReader(src.Body, limit+1))
	_ = src.Body.Close()
	if err != nil {
		return Object{}, err
	}
	if int64(len(data)) > limit {
		return Object{}, ErrImageTooLarge
	}
	out, err := DisplayImage(data)
	if err != nil {
		return Object{}, err
	}
	if err := s.o.Storage.Put(ctx, key, bytes.NewReader(out), int64(len(out)), "image/jpeg"); err != nil {
		return Object{}, err
	}
	if _, err := s.o.Attachments.Get(ctx, a.ID); err != nil {
		_ = s.o.Storage.Delete(context.WithoutCancel(ctx), key)
		if errors.Is(err, postgres.ErrNotFound) {
			return Object{}, ErrNoAccess
		}
		return Object{}, err
	}
	return Object{Body: readSeekNopCloser{bytes.NewReader(out)}, Size: int64(len(out))}, nil
}

type readSeekNopCloser struct{ *bytes.Reader }

func (readSeekNopCloser) Close() error { return nil }

func (s *Service) download(ctx context.Context, key, ctype, name string, obj *Object) (Download, error) {
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
		if err != nil || url != "" {
			if obj != nil {
				_ = obj.Body.Close()
			}
		}
		if err != nil {
			return Download{}, err
		}
		if url != "" {
			return Download{RedirectURL: url, ContentType: ctype, Disposition: disposition}, nil
		}
	}
	if obj != nil {
		return Download{Object: *obj, ContentType: ctype, Disposition: disposition}, nil
	}
	got, err := s.o.Storage.Get(ctx, key)
	if errors.Is(err, ErrNotFound) {
		return Download{}, ErrNoAccess
	}
	if err != nil {
		return Download{}, err
	}
	return Download{Object: got, ContentType: ctype, Disposition: disposition}, nil
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

func (s *Service) convertHEIF(ctx context.Context, tmp *os.File, size int64, det Detected) (*os.File, int64, Detected, func()) {
	asFile := Detected{Kind: KindFile, ContentType: "application/octet-stream"}
	png, err := s.o.Prober.StillToPNG(ctx, tmp.Name())
	if err != nil {
		s.o.Log.InfoContext(ctx, "heif upload kept as file", "error", err)
		return tmp, size, asFile, func() {}
	}
	out, err := os.CreateTemp("", "messenger-heif-*")
	if err != nil {
		return tmp, size, asFile, func() {}
	}
	cleanup := func() {
		_ = out.Close()
		_ = os.Remove(out.Name())
	}
	if _, err := out.Write(png); err != nil {
		cleanup()
		return tmp, size, asFile, func() {}
	}
	return out, int64(len(png)), Detected{Kind: KindImage, ContentType: "image/png"}, cleanup
}
