package media

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"

	"github.com/myronsi/messenger-back/internal/store/postgres"
)

// processVideoThumbnail uses the same bounded processing pool as image uploads.
// Videos that cannot be decoded can still be downloaded.
func (s *Service) processVideoThumbnail(ctx context.Context, path, contentType string) []byte {
	if s.o.Prober == nil || s.o.Prober.ffmpeg == "" {
		return nil
	}
	select {
	case s.images <- struct{}{}:
	case <-ctx.Done():
		return nil
	}
	defer func() { <-s.images }()

	return s.videoFrame(ctx, path, contentType)
}

func (s *Service) videoFrame(ctx context.Context, path, contentType string) []byte {
	format := "mov"
	if contentType == "video/webm" {
		format = "matroska"
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	frame, err := exec.CommandContext(ctx, s.o.Prober.ffmpeg, "-nostdin", "-v", "error", //nolint:gosec // fixed tool, bounded output, and a temporary upload
		"-protocol_whitelist", "file,pipe", "-threads", "1", "-f", format, "-i", path,
		"-map", "0:V:0", "-frames:v", "1", "-vf",
		"scale=w='min(640,iw)':h='min(640,ih)':force_original_aspect_ratio=decrease:force_divisible_by=2,setsar=1",
		"-an", "-c:v", "mjpeg", "-threads", "1", "-f", "image2pipe", "pipe:1").Output()
	if err != nil {
		s.o.Log.DebugContext(ctx, "video thumbnail unavailable", "error", err)
		return nil
	}
	return frame
}

// legacyVideoThumbnail renders a frame for a video uploaded before previews were stored.
// Access is checked by Open before entering this path.
func (s *Service) legacyVideoThumbnail(ctx context.Context, a postgres.Attachment) (Download, error) {
	if !strings.HasPrefix(a.MimeType, "video/") || s.o.Prober == nil || s.o.Prober.ffmpeg == "" || a.Size > s.o.Limits.Video {
		return Download{}, ErrNoAccess
	}
	select {
	case s.images <- struct{}{}:
	case <-ctx.Done():
		return Download{}, ctx.Err()
	}
	defer func() { <-s.images }()

	obj, err := s.o.Storage.Get(ctx, a.StorageKey)
	if errors.Is(err, ErrNotFound) {
		return Download{}, ErrNoAccess
	}
	if err != nil {
		return Download{}, err
	}
	defer func() { _ = obj.Body.Close() }()
	tmp, size, cleanup, err := spool(obj.Body, s.o.Limits.Video)
	if err != nil {
		return Download{}, err
	}
	defer cleanup()
	if size > s.o.Limits.Video {
		return Download{}, ErrNoAccess
	}
	frame := s.videoFrame(ctx, tmp.Name(), a.MimeType)
	if len(frame) == 0 {
		return Download{}, ErrNoAccess
	}
	return Download{
		Object:      Object{Body: readSeekNopCloser{bytes.NewReader(frame)}, Size: int64(len(frame))},
		ContentType: "image/jpeg", Disposition: "inline",
	}, nil
}
