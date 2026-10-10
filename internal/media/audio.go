package media

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
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

// ErrMeasureTimeout is returned when ffprobe or ffmpeg did not finish in time: a problem of the server (or a
// file that is very slow to decode), not proof that the file is not audio.
var ErrMeasureTimeout = errors.New("media: measuring the audio took too long")

// Probe measures the file at path (a temporary copy of the upload). waveform asks for the bars as well.
// ffprobe and ffmpeg get probeTimeout each, and may read only the file itself (no network, no other files).
func (p *Prober) Probe(ctx context.Context, path string, waveform bool) (AudioInfo, error) {
	pctx, cancel := context.WithTimeout(ctx, probeTimeout)
	out, err := exec.CommandContext(pctx, p.ffprobe, "-v", "error", "-protocol_whitelist", "file", "-select_streams", "a:0", //nolint:gosec // fixed tool and arguments; path is our temporary file
		"-show_entries", "format=duration:stream=codec_type", "-of", "default=noprint_wrappers=1", path).Output()
	timedOut := pctx.Err() != nil
	cancel()
	if err != nil {
		return AudioInfo{}, toolFailed(ctx, timedOut)
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

// toolFailed tells a timeout or a cancelled request apart from a file the tools cannot read.
func toolFailed(ctx context.Context, timedOut bool) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if timedOut {
		return ErrMeasureTimeout
	}
	return ErrNotAudio
}

// pcmWindow is how many 8 kHz samples one stored peak covers while streaming (10 ms).
const pcmWindow = 80

// waveform decodes to 8 kHz mono PCM and takes the peak of WaveformBars equal slices. The PCM is streamed
// through a peak accumulator, never held: the container's duration is the uploader's claim, and the decoded
// audio is cut at MaxAudioDuration whatever it says.
func (p *Prober) waveform(ctx context.Context, path string) ([]int16, error) {
	wctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	limit := strconv.Itoa(int(MaxAudioDuration.Seconds()))
	cmd := exec.CommandContext(wctx, p.ffmpeg, "-v", "error", "-protocol_whitelist", "file,pipe", "-i", path, //nolint:gosec // see Probe
		"-t", limit, "-ac", "1", "-ar", "8000", "-f", "s16le", "pipe:1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	acc := &peaks{window: pcmWindow}
	maxBytes := int64(MaxAudioDuration/time.Second) * 8000 * 2
	_, copyErr := io.Copy(acc, io.LimitReader(stdout, maxBytes))
	_ = cmd.Process.Kill() // done reading: whatever ffmpeg still has is beyond the limit
	waitErr := cmd.Wait()
	timedOut := wctx.Err() == context.DeadlineExceeded
	if copyErr != nil || (waitErr != nil && acc.samples == 0) || timedOut {
		return nil, toolFailed(ctx, timedOut)
	}
	return acc.bars(WaveformBars), nil
}

// peaks accumulates the absolute peak of every `window` samples of signed 16-bit little-endian PCM.
type peaks struct {
	window  int
	cur     float64
	inCur   int
	samples int
	odd     []byte
	list    []float64
}

func (p *peaks) Write(b []byte) (int, error) {
	n := len(b)
	if len(p.odd) > 0 {
		b = append(p.odd, b...)
		p.odd = nil
	}
	for len(b) >= 2 {
		v := math.Abs(float64(int16(binary.LittleEndian.Uint16(b)))) //nolint:gosec // reinterpreting PCM bits as signed is intended
		b = b[2:]
		p.cur = math.Max(p.cur, v)
		p.inCur++
		p.samples++
		if p.inCur == p.window {
			p.list = append(p.list, p.cur)
			p.cur, p.inCur = 0, 0
		}
	}
	if len(b) == 1 {
		p.odd = []byte{b[0]}
	}
	return n, nil
}

// bars spreads the peaks over n equal slices, scaled to 0-255.
func (p *peaks) bars(n int) []int16 {
	list := p.list
	if p.inCur > 0 {
		list = append(list, p.cur)
	}
	bars := make([]int16, n)
	if len(list) == 0 {
		return bars
	}
	slices := make([]float64, n)
	if len(list) >= n {
		for i, v := range list {
			b := i * n / len(list)
			slices[b] = math.Max(slices[b], v)
		}
	} else {
		for b := range slices {
			slices[b] = list[b*len(list)/n]
		}
	}
	top := 0.0
	for _, v := range slices {
		top = math.Max(top, v)
	}
	if top == 0 {
		return bars
	}
	for i, v := range slices {
		bars[i] = int16(math.Round(v / top * 255))
	}
	return bars
}

// Bars turns signed 16-bit little-endian PCM into n peak amplitudes scaled to 0-255.
func Bars(pcm []byte, n int) []int16 {
	acc := &peaks{window: 1}
	_, _ = acc.Write(pcm)
	return acc.bars(n)
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
