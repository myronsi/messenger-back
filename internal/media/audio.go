package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Audio limits.
const (
	MaxAudioDuration = time.Hour
	WaveformBars     = 64
	probeTimeout     = 15 * time.Second
)

// AudioInfo is what the server knows about an audio file.
type AudioInfo struct {
	Duration time.Duration
	// Waveform has WaveformBars amplitudes 0-255 (voice messages only).
	Waveform []int16
}

// Prober measures audio with ffprobe and ffmpeg. Without them (Available false) the client's values are used,
// clamped to the limits.
type Prober struct {
	ffprobe, ffmpeg string
}

// NewProber looks the tools up: the given paths, or ffprobe and ffmpeg on PATH. Missing tools are no error
// (Available is then false).
func NewProber(ffprobePath, ffmpegPath string) *Prober {
	look := func(configured, name string) string {
		if configured != "" {
			name = configured
		}
		p, err := exec.LookPath(name)
		if err != nil {
			return ""
		}
		return p
	}
	return &Prober{ffprobe: look(ffprobePath, "ffprobe"), ffmpeg: look(ffmpegPath, "ffmpeg")}
}

// Available reports whether the tools were found.
func (p *Prober) Available() bool { return p != nil && p.ffprobe != "" && p.ffmpeg != "" }

// ErrNotAudio is returned when the tools cannot read the file as audio.
var ErrNotAudio = errors.New("media: not a readable audio file")

// Probe measures the file at path (a temporary copy of the upload). waveform asks for the bars as well.
func (p *Prober) Probe(ctx context.Context, path string, waveform bool) (AudioInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, p.ffprobe, "-v", "error", "-select_streams", "a:0", //nolint:gosec // fixed tool and arguments; path is our temporary file
		"-show_entries", "format=duration:stream=codec_type", "-of", "default=noprint_wrappers=1", path).Output()
	if err != nil {
		return AudioInfo{}, ErrNotAudio
	}
	var info AudioInfo
	audio := false
	for _, line := range strings.Split(string(out), "\n") {
		k, v, _ := strings.Cut(strings.TrimSpace(line), "=")
		switch k {
		case "codec_type":
			audio = audio || v == "audio"
		case "duration":
			if s, err := strconv.ParseFloat(v, 64); err == nil && s >= 0 {
				info.Duration = time.Duration(s * float64(time.Second))
			}
		}
	}
	if !audio {
		return AudioInfo{}, ErrNotAudio
	}
	if info.Duration > MaxAudioDuration {
		return AudioInfo{}, fmt.Errorf("%w: longer than %s", ErrUnsupported, MaxAudioDuration)
	}
	if waveform {
		if info.Waveform, err = p.waveform(ctx, path); err != nil {
			return AudioInfo{}, err
		}
	}
	return info, nil
}

// waveform decodes to 8 kHz mono PCM and takes the peak of WaveformBars equal slices.
func (p *Prober) waveform(ctx context.Context, path string) ([]int16, error) {
	cmd := exec.CommandContext(ctx, p.ffmpeg, "-v", "error", "-i", path, "-ac", "1", "-ar", "8000", "-f", "s16le", "pipe:1") //nolint:gosec // see Probe
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, ErrNotAudio
	}
	return Bars(out.Bytes(), WaveformBars), nil
}

// Bars turns signed 16-bit little-endian PCM into n peak amplitudes scaled to 0-255.
func Bars(pcm []byte, n int) []int16 {
	samples := len(pcm) / 2
	bars := make([]int16, n)
	if samples == 0 {
		return bars
	}
	peaks := make([]float64, n)
	for i := range samples {
		v := math.Abs(float64(int16(binary.LittleEndian.Uint16(pcm[2*i:])))) //nolint:gosec // reinterpreting PCM bits as signed is intended
		b := i * n / samples
		if v > peaks[b] {
			peaks[b] = v
		}
	}
	top := 0.0
	for _, p := range peaks {
		top = math.Max(top, p)
	}
	if top == 0 {
		return bars
	}
	for i, p := range peaks {
		bars[i] = int16(math.Round(p / top * 255))
	}
	return bars
}

// ClientAudio validates the duration and waveform a client sent, used when the server cannot measure.
func ClientAudio(durationMS *int, waveform []int) AudioInfo {
	var info AudioInfo
	if durationMS != nil && *durationMS >= 0 {
		info.Duration = min(time.Duration(*durationMS)*time.Millisecond, MaxAudioDuration)
	}
	if n := min(len(waveform), 128); n > 0 {
		info.Waveform = make([]int16, n)
		for i := range n {
			info.Waveform[i] = int16(min(max(waveform[i], 0), 255))
		}
	}
	return info
}
