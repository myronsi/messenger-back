package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/store/postgres"
)

const (
	JobVideo = "video"
	JobGIF   = "gif"
	JobAudio = "audio"

	ProcessingQueued  = "queued"
	ProcessingRunning = "running"
	ProcessingDone    = "done"
	ProcessingFailed  = "failed"

	VariantNameAnimation = "animation"

	MaxJobAttempts     = 3
	maxTranscodeLength = 30 * time.Minute
	videoTargetSide    = 720
	videoTargetBitrate = 2_500_000
	audioMaxBitrate    = 192_000
)

var ErrJobSkipped = errors.New("media: nothing to optimize")

type StreamInfo struct {
	VideoCodec string
	AudioCodec string
	PixFmt     string
	Width      int
	Height     int
	Bitrate    int64
	Duration   time.Duration
	Format     string
}

type ffprobeOutput struct {
	Streams []struct {
		CodecType string `json:"codec_type"`
		CodecName string `json:"codec_name"`
		PixFmt    string `json:"pix_fmt"`
		Width     int    `json:"width"`
		Height    int    `json:"height"`
		BitRate   string `json:"bit_rate"`
		Attached  struct {
			AttachedPic int `json:"attached_pic"`
		} `json:"disposition"`
	} `json:"streams"`
	Format struct {
		FormatName string `json:"format_name"`
		Duration   string `json:"duration"`
		BitRate    string `json:"bit_rate"`
	} `json:"format"`
}

func parseStreamInfo(out []byte) (StreamInfo, error) {
	var p ffprobeOutput
	if err := json.Unmarshal(out, &p); err != nil {
		return StreamInfo{}, ErrUnsupported
	}
	var info StreamInfo
	for _, st := range p.Streams {
		switch st.CodecType {
		case "video":
			if st.Attached.AttachedPic == 1 || info.VideoCodec != "" {
				continue
			}
			info.VideoCodec, info.PixFmt, info.Width, info.Height = st.CodecName, st.PixFmt, st.Width, st.Height
		case "audio":
			if info.AudioCodec == "" {
				info.AudioCodec = st.CodecName
				if info.Bitrate == 0 {
					info.Bitrate, _ = strconv.ParseInt(st.BitRate, 10, 64)
				}
			}
		}
	}
	info.Format = p.Format.FormatName
	if br, err := strconv.ParseInt(p.Format.BitRate, 10, 64); err == nil && br > 0 {
		info.Bitrate = br
	}
	if d, err := strconv.ParseFloat(p.Format.Duration, 64); err == nil && d > 0 {
		info.Duration = time.Duration(d * float64(time.Second))
	}
	return info, nil
}

func (p *Prober) StreamInfo(ctx context.Context, path string) (StreamInfo, error) {
	if !p.Available() {
		return StreamInfo{}, ErrNoEncoder
	}
	pctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	out, err := exec.CommandContext(pctx, p.ffprobe, "-v", "error", "-protocol_whitelist", "file", //nolint:gosec
		"-show_entries", "stream=codec_type,codec_name,pix_fmt,width,height,bit_rate:stream_disposition=attached_pic:format=format_name,duration,bit_rate",
		"-of", "json", path).Output()
	if err != nil {
		return StreamInfo{}, toolFailed(ctx, pctx.Err() != nil)
	}
	return parseStreamInfo(out)
}

func MoovAfterMdat(r io.ReaderAt, size int64) bool {
	var pos int64
	seenMdat := false
	header := make([]byte, 16)
	for pos+8 <= size {
		if _, err := r.ReadAt(header[:8], pos); err != nil {
			return false
		}
		boxSize := int64(binary.BigEndian.Uint32(header[:4]))
		kind := string(header[4:8])
		switch boxSize {
		case 0:
			boxSize = size - pos
		case 1:
			if _, err := r.ReadAt(header[8:16], pos+8); err != nil {
				return false
			}
			boxSize = int64(binary.BigEndian.Uint64(header[8:16])) //nolint:gosec
		}
		if boxSize < 8 {
			return false
		}
		switch kind {
		case "mdat":
			seenMdat = true
		case "moov":
			return seenMdat
		}
		pos += boxSize
	}
	return false
}

func isMP4Family(format string) bool {
	return strings.Contains(format, "mp4") || strings.Contains(format, "mov")
}

func NeedsVideoTranscode(info StreamInfo) bool {
	if info.VideoCodec != "h264" || (info.PixFmt != "" && info.PixFmt != "yuv420p" && info.PixFmt != "yuvj420p") {
		return true
	}
	if info.AudioCodec != "" && info.AudioCodec != "aac" && info.AudioCodec != "mp3" {
		return true
	}
	if min(info.Width, info.Height) > videoTargetSide {
		return true
	}
	return info.Bitrate > videoTargetBitrate*6/5
}

var losslessAudio = map[string]bool{"flac": true, "alac": true, "wavpack": true, "ape": true, "tta": true, "truehd": true}

func NeedsAudioTranscode(info StreamInfo) bool {
	if info.AudioCodec == "" {
		return false
	}
	return losslessAudio[info.AudioCodec] || strings.HasPrefix(info.AudioCodec, "pcm_") || info.Bitrate > audioMaxBitrate
}

func (p *Prober) run(ctx context.Context, timeout time.Duration, args ...string) error {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var stderr bytes.Buffer
	cmd := exec.CommandContext(cctx, p.ffmpeg, args...) //nolint:gosec
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if cctx.Err() != nil {
			return ErrMeasureTimeout
		}
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 300 {
			msg = msg[len(msg)-300:]
		}
		return fmt.Errorf("ffmpeg: %w: %s", err, msg)
	}
	return nil
}

func transcodeTimeout(d time.Duration) time.Duration {
	return min(10*time.Minute, max(time.Minute, 3*d+time.Minute))
}

const evenScale = "scale=trunc(iw/2)*2:trunc(ih/2)*2"

func videoScale(side int) string {
	s := strconv.Itoa(side)
	return "scale=w='if(gte(iw,ih),-2,min(" + s + ",iw))':h='if(gte(iw,ih),min(" + s + ",ih),-2)'," + evenScale
}

func (p *Prober) TranscodeVideo(ctx context.Context, in, out string, info StreamInfo) error {
	args := []string{"-nostdin", "-v", "error", "-y", "-protocol_whitelist", "file", "-i", in,
		"-map", "0:v:0", "-map", "0:a:0?", "-map_metadata", "-1", "-map_chapters", "-1",
		"-vf", videoScale(videoTargetSide), "-fpsmax", "30",
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "26", "-profile:v", "high", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "96k", "-ac", "2", "-threads", "2", "-movflags", "+faststart", "-f", "mp4", out}
	return p.run(ctx, transcodeTimeout(info.Duration), args...)
}

func (p *Prober) Faststart(ctx context.Context, in, out string, audioOnly bool) error {
	args := []string{"-nostdin", "-v", "error", "-y", "-protocol_whitelist", "file", "-i", in, "-map", "0", "-c", "copy"}
	if audioOnly {
		args = []string{"-nostdin", "-v", "error", "-y", "-protocol_whitelist", "file", "-i", in, "-map", "0:a", "-c", "copy"}
	}
	args = append(args, "-movflags", "+faststart", "-f", "mp4", out)
	return p.run(ctx, 2*time.Minute, args...)
}

func (p *Prober) TranscodeGIF(ctx context.Context, in, out string) error {
	return p.run(ctx, 2*time.Minute, "-nostdin", "-v", "error", "-y", "-protocol_whitelist", "file", "-f", "gif", "-i", in,
		"-vf", evenScale, "-c:v", "libx264", "-preset", "veryfast", "-crf", "28", "-pix_fmt", "yuv420p", "-an",
		"-threads", "2", "-movflags", "+faststart", "-f", "mp4", out)
}

func (p *Prober) TranscodeAudio(ctx context.Context, in, out string, info StreamInfo) error {
	return p.run(ctx, transcodeTimeout(info.Duration), "-nostdin", "-v", "error", "-y", "-protocol_whitelist", "file", "-i", in,
		"-map", "0:a:0", "-vn", "-map_metadata", "0", "-c:a", "aac", "-b:a", "160k", "-threads", "2",
		"-movflags", "+faststart", "-f", "mp4", out)
}

type jobOutput struct {
	variant     string
	contentType string
	path        string
}

func (s *Service) RunJob(ctx context.Context, job postgres.MediaJob) (postgres.Attachment, error) {
	a, err := s.o.Attachments.Get(ctx, job.AttachmentID)
	if err != nil {
		return postgres.Attachment{}, err
	}
	running := ProcessingRunning
	if err := s.o.Attachments.SetProcessing(ctx, a.ID, &running); err != nil {
		return a, err
	}
	if job.Kind == JobImportImage {
		return s.runImportImage(ctx, a)
	}
	if job.Kind == JobImportSticker {
		return s.runImportSticker(ctx, a)
	}
	src, cleanup, err := s.fetch(ctx, a)
	if err != nil {
		return a, err
	}
	defer cleanup()
	outPath := src + ".out"
	defer func() { _ = os.Remove(outPath) }()
	result, err := s.optimize(ctx, job.Kind, src, outPath)
	done := ProcessingDone
	if errors.Is(err, ErrJobSkipped) {
		updated, uerr := s.o.Attachments.MergeVariants(ctx, a.ID, postgres.AttachmentUpdate{ProcessingState: &done})
		if uerr != nil {
			return a, uerr
		}
		return updated, nil
	}
	if err != nil {
		return a, err
	}
	st, err := os.Stat(result.path)
	if err != nil {
		return a, err
	}
	if job.Kind != JobVideo && st.Size() >= a.Size {
		updated, uerr := s.o.Attachments.MergeVariants(ctx, a.ID, postgres.AttachmentUpdate{ProcessingState: &done})
		if uerr != nil {
			return a, uerr
		}
		return updated, nil
	}
	f, err := os.Open(result.path)
	if err != nil {
		return a, err
	}
	defer func() { _ = f.Close() }()
	key := a.StorageKey + "." + result.variant + extensionOf(result.contentType)
	if err := s.o.Storage.Put(ctx, key, f, st.Size(), result.contentType); err != nil {
		return a, err
	}
	sv := postgres.StoredVariant{Key: key, ContentType: result.contentType, Size: st.Size()}
	updated, err := s.o.Attachments.MergeVariants(ctx, a.ID, postgres.AttachmentUpdate{
		Variants: postgres.Variants{result.variant: sv}, ProcessingState: &done,
	})
	if err != nil {
		_ = s.o.Storage.Delete(context.WithoutCancel(ctx), key)
		return a, err
	}
	return updated, nil
}

func (s *Service) MarkJobFailed(ctx context.Context, job postgres.MediaJob) {
	failed := ProcessingFailed
	if err := s.o.Attachments.SetProcessing(ctx, job.AttachmentID, &failed); err != nil && !errors.Is(err, postgres.ErrNotFound) {
		s.o.Log.WarnContext(ctx, "mark media job failed", "attachment_id", job.AttachmentID.String(), "error", err)
	}
}

func (s *Service) fetch(ctx context.Context, a postgres.Attachment) (string, func(), error) {
	obj, err := s.o.Storage.Get(ctx, a.StorageKey)
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = obj.Body.Close() }()
	tmp, err := os.CreateTemp("", "messenger-job-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.Remove(tmp.Name()) }
	if _, err := io.Copy(tmp, io.LimitReader(obj.Body, s.o.Limits.Max()+1)); err != nil {
		_ = tmp.Close()
		cleanup()
		return "", nil, err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return "", nil, err
	}
	return tmp.Name(), cleanup, nil
}

func (s *Service) optimize(ctx context.Context, kind, src, out string) (jobOutput, error) {
	if !s.o.Prober.Available() {
		return jobOutput{}, ErrJobSkipped
	}
	if kind == JobGIF {
		if err := s.o.Prober.TranscodeGIF(ctx, src, out); err != nil {
			return jobOutput{}, err
		}
		return jobOutput{variant: VariantNameAnimation, contentType: "video/mp4", path: out}, nil
	}
	info, err := s.o.Prober.StreamInfo(ctx, src)
	if err != nil {
		return jobOutput{}, err
	}
	if info.Duration > maxTranscodeLength {
		return jobOutput{}, ErrJobSkipped
	}
	moovLate := false
	if isMP4Family(info.Format) {
		if f, err := os.Open(src); err == nil { //nolint:gosec
			if st, err := f.Stat(); err == nil {
				moovLate = MoovAfterMdat(f, st.Size())
			}
			_ = f.Close()
		}
	}
	switch kind {
	case JobVideo:
		if info.VideoCodec == "" {
			return jobOutput{}, ErrJobSkipped
		}
		if NeedsVideoTranscode(info) {
			err = s.o.Prober.TranscodeVideo(ctx, src, out, info)
		} else if moovLate {
			err = s.o.Prober.Faststart(ctx, src, out, false)
		} else {
			return jobOutput{}, ErrJobSkipped
		}
		if err != nil {
			return jobOutput{}, err
		}
		return jobOutput{variant: VariantNameOptimized, contentType: "video/mp4", path: out}, nil
	case JobAudio:
		if NeedsAudioTranscode(info) {
			err = s.o.Prober.TranscodeAudio(ctx, src, out, info)
		} else if moovLate {
			err = s.o.Prober.Faststart(ctx, src, out, true)
		} else {
			return jobOutput{}, ErrJobSkipped
		}
		if err != nil {
			return jobOutput{}, err
		}
		return jobOutput{variant: VariantNameOptimized, contentType: "audio/mp4", path: out}, nil
	}
	return jobOutput{}, ErrJobSkipped
}

func (s *Service) Attachment(ctx context.Context, id uuid.UUID) (postgres.Attachment, error) {
	return s.o.Attachments.Get(ctx, id)
}
