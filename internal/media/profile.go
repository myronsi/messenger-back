package media

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/myronsi/messenger-back/internal/store/postgres"
)

const (
	PurposeProfileMusic      = postgres.PurposeProfileMusic
	PurposeProfileBackground = postgres.PurposeProfileBackground

	MaxMusicClip          = 30 * time.Second
	MaxBackgroundVideo    = 15 * time.Second
	musicClipTolerance    = 250 * time.Millisecond
	maxTagLength          = 100
	profileMusicLimit     = 20 << 20
	backgroundImageLimit  = 10 << 20
	backgroundMotionLimit = 15 << 20
	musicTimeout          = 60 * time.Second
)

type Tags struct {
	Title  *string
	Artist *string
	Album  *string
}

type musicClip struct {
	data     []byte
	duration time.Duration
	tags     Tags
	cover    []byte
}

func sniff(head []byte) string {
	s := http.DetectContentType(head)
	if i := strings.IndexByte(s, ';'); i >= 0 {
		s = s[:i]
	}
	return s
}

func isProfilePurpose(purpose string) bool {
	return purpose == PurposeProfileMusic || purpose == PurposeProfileBackground
}

func profileLimit(purpose string, det Detected) int64 {
	if purpose == PurposeProfileMusic {
		return profileMusicLimit
	}
	if det.Kind == KindVideo || det.ContentType == "image/gif" {
		return backgroundMotionLimit
	}
	return backgroundImageLimit
}

func detectProfile(head []byte, purpose string) (Detected, error) {
	if purpose == PurposeProfileMusic {
		at := audioType(head, sniff(head))
		if at == "" {
			return Detected{}, ErrUnsupported
		}
		return Detected{Kind: KindAudio, ContentType: at}, nil
	}
	det, err := Detect(head, PurposeMessage, "")
	if err != nil {
		return Detected{}, err
	}
	if det.Kind != KindImage && det.Kind != KindVideo {
		return Detected{}, ErrUnsupported
	}
	return det, nil
}

func clipRange(duration time.Duration, startMS, endMS *int) (time.Duration, time.Duration, error) {
	start := time.Duration(0)
	if startMS != nil {
		start = time.Duration(*startMS) * time.Millisecond
	}
	if start < 0 || (duration > 0 && start >= duration) {
		return 0, 0, fmt.Errorf("%w: start_ms is outside the file", ErrInvalid)
	}
	end := start + MaxMusicClip
	if endMS != nil {
		end = time.Duration(*endMS) * time.Millisecond
		if end <= start || end-start > MaxMusicClip+musicClipTolerance {
			return 0, 0, fmt.Errorf("%w: end_ms must be after start_ms and at most 30 s later", ErrInvalid)
		}
	}
	if duration > 0 && end > duration {
		end = duration
	}
	return start, min(end-start, MaxMusicClip), nil
}

func seconds(d time.Duration) string { return strconv.FormatFloat(d.Seconds(), 'f', 3, 64) }

func (p *Prober) cutMusic(ctx context.Context, path string, start, length time.Duration) ([]byte, error) {
	out, err := os.CreateTemp("", "messenger-music-*.m4a")
	if err != nil {
		return nil, err
	}
	name := out.Name()
	_ = out.Close()
	defer func() { _ = os.Remove(name) }()
	cctx, cancel := context.WithTimeout(ctx, musicTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, p.ffmpeg, "-nostdin", "-v", "error", "-y", //nolint:gosec // fixed tool and arguments; path is our temporary file
		"-protocol_whitelist", "file", "-threads", "1", "-ss", seconds(start), "-t", seconds(length), "-i", path,
		"-map", "0:a:0", "-vn", "-map_metadata", "-1", "-ac", "2", "-ar", "44100", "-c:a", "aac", "-b:a", "128k",
		"-movflags", "+faststart", "-f", "mp4", name)
	if err := cmd.Run(); err != nil {
		return nil, toolFailed(ctx, cctx.Err() != nil)
	}
	return os.ReadFile(name) //nolint:gosec // our temporary file
}

func cleanTag(v string) *string {
	v = strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, v))
	for utf8.RuneCountInString(v) > maxTagLength {
		_, size := utf8.DecodeLastRuneInString(v)
		v = v[:len(v)-size]
	}
	if v == "" {
		return nil
	}
	return &v
}

func parseTags(out string) Tags {
	var t Tags
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimPrefix(k, "TAG:")) {
		case "title":
			if t.Title == nil {
				t.Title = cleanTag(v)
			}
		case "artist":
			if t.Artist == nil {
				t.Artist = cleanTag(v)
			}
		case "album":
			if t.Album == nil {
				t.Album = cleanTag(v)
			}
		}
	}
	return t
}

func (p *Prober) tags(ctx context.Context, path string) Tags {
	tctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	out, err := exec.CommandContext(tctx, p.ffprobe, "-v", "error", "-protocol_whitelist", "file", //nolint:gosec // see Probe
		"-show_entries", "format_tags=title,artist,album:stream_tags=title,artist,album", "-of", "default=noprint_wrappers=1", path).Output()
	if err != nil {
		return Tags{}
	}
	return parseTags(string(out))
}

func (p *Prober) cover(ctx context.Context, path string) []byte {
	cctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	frame, err := exec.CommandContext(cctx, p.ffmpeg, "-nostdin", "-v", "error", //nolint:gosec // see cutMusic
		"-protocol_whitelist", "file,pipe", "-threads", "1", "-i", path, "-map", "0:v:0", "-frames:v", "1", "-vf",
		"scale=w='min(640,iw)':h='min(640,ih)':force_original_aspect_ratio=decrease:force_divisible_by=2,setsar=1",
		"-an", "-c:v", "mjpeg", "-f", "image2pipe", "pipe:1").Output()
	if err != nil || len(frame) == 0 {
		return nil
	}
	return frame
}

func (s *Service) processMusic(ctx context.Context, path string, startMS, endMS *int) (musicClip, error) {
	if !s.o.Prober.Available() {
		return musicClip{}, fmt.Errorf("%w: profile music needs ffmpeg on the server", ErrUnsupported)
	}
	info, err := s.o.Prober.Probe(ctx, path, false)
	if err != nil {
		return musicClip{}, err
	}
	start, length, err := clipRange(info.Duration, startMS, endMS)
	if err != nil {
		return musicClip{}, err
	}
	data, err := s.o.Prober.cutMusic(ctx, path, start, length)
	if err != nil {
		return musicClip{}, err
	}
	return musicClip{data: data, duration: length, tags: s.o.Prober.tags(ctx, path), cover: s.o.Prober.cover(ctx, path)}, nil
}

func (s *Service) canSeeProfileMedia(ctx context.Context, viewerID int64, a postgres.Attachment) (bool, error) {
	if a.UploaderID != nil && *a.UploaderID == viewerID {
		return true, nil
	}
	if s.o.Profiles != nil {
		owners, err := s.o.Attachments.UsersWithProfileMedia(ctx, a.ID)
		if err != nil {
			return false, err
		}
		for _, owner := range owners {
			ok, err := s.o.Profiles.ProfileVisible(ctx, viewerID, owner)
			if err != nil || ok {
				return ok, err
			}
		}
	}
	chats, err := s.o.Attachments.ChatsWithBackground(ctx, a.ID)
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

func (p *Prober) duration(ctx context.Context, path string) (time.Duration, bool) {
	if p == nil || p.ffprobe == "" {
		return 0, false
	}
	dctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	out, err := exec.CommandContext(dctx, p.ffprobe, "-v", "error", "-protocol_whitelist", "file", //nolint:gosec // see Probe
		"-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", path).Output()
	if err != nil {
		return 0, false
	}
	secs, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil || secs < 0 {
		return 0, false
	}
	return time.Duration(secs * float64(time.Second)), true
}
