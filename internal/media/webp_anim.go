package media

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/draw"

	"golang.org/x/image/webp"
)

var errBadWebP = errors.New("media: malformed WebP")

const (
	webpFlagAlpha     = 0x10
	webpFlagAnimation = 0x02
	anmfHeaderSize    = 16
)

type riffChunk struct {
	fourCC  string
	payload []byte
}

func riffChunks(data []byte) ([]riffChunk, error) {
	var out []riffChunk
	for pos := 0; pos < len(data); {
		if len(data)-pos < 8 {
			return nil, errBadWebP
		}
		size := int(binary.LittleEndian.Uint32(data[pos+4 : pos+8]))
		start := pos + 8
		if size < 0 || size > len(data)-start {
			return nil, errBadWebP
		}
		out = append(out, riffChunk{fourCC: string(data[pos : pos+4]), payload: data[start : start+size]})
		pos = start + size + size&1
	}
	return out, nil
}

func uint24(b []byte) int {
	return int(b[0]) | int(b[1])<<8 | int(b[2])<<16
}

func putUint24(b []byte, v int) {
	b[0], b[1], b[2] = byte(v&0xff), byte((v>>8)&0xff), byte((v>>16)&0xff)
}

func appendChunk(dst []byte, fourCC string, payload []byte) []byte {
	dst = append(dst, fourCC...)
	dst = binary.LittleEndian.AppendUint32(dst, uint32(len(payload)&0xffffffff))
	dst = append(dst, payload...)
	if len(payload)&1 == 1 {
		dst = append(dst, 0)
	}
	return dst
}

func riffWebP(body []byte) []byte {
	out := make([]byte, 0, len(body)+12)
	out = append(out, "RIFF"...)
	out = binary.LittleEndian.AppendUint32(out, uint32((len(body)+4)&0xffffffff))
	out = append(out, "WEBP"...)
	return append(out, body...)
}

type webpFrame struct {
	x, y, width, height int
	alpha, bitstream    *riffChunk
}

func (f webpFrame) still() []byte {
	var body []byte
	if f.alpha != nil {
		vp8x := make([]byte, 10)
		vp8x[0] = webpFlagAlpha
		putUint24(vp8x[4:7], f.width-1)
		putUint24(vp8x[7:10], f.height-1)
		body = appendChunk(body, "VP8X", vp8x)
		body = appendChunk(body, "ALPH", f.alpha.payload)
	}
	return riffWebP(appendChunk(body, f.bitstream.fourCC, f.bitstream.payload))
}

func parseANMF(payload []byte, canvasW, canvasH int) (webpFrame, []byte, error) {
	if len(payload) < anmfHeaderSize {
		return webpFrame{}, nil, errBadWebP
	}
	f := webpFrame{
		x: uint24(payload[0:3]) * 2, y: uint24(payload[3:6]) * 2,
		width: uint24(payload[6:9]) + 1, height: uint24(payload[9:12]) + 1,
	}
	if f.x+f.width > canvasW || f.y+f.height > canvasH {
		return webpFrame{}, nil, errBadWebP
	}
	subs, err := riffChunks(payload[anmfHeaderSize:])
	if err != nil {
		return webpFrame{}, nil, err
	}
	var kept []byte
	for i := range subs {
		switch subs[i].fourCC {
		case "ALPH":
			if f.alpha != nil || f.bitstream != nil {
				return webpFrame{}, nil, errBadWebP
			}
			f.alpha = &subs[i]
		case "VP8 ", "VP8L":
			if f.bitstream != nil || (f.alpha != nil && subs[i].fourCC == "VP8L") {
				return webpFrame{}, nil, errBadWebP
			}
			f.bitstream = &subs[i]
		default:
			continue
		}
		kept = appendChunk(kept, subs[i].fourCC, subs[i].payload)
	}
	if f.bitstream == nil {
		return webpFrame{}, nil, errBadWebP
	}
	return f, append(append([]byte{}, payload[:anmfHeaderSize]...), kept...), nil
}

type animatedWebPResult struct {
	data          []byte
	width, height int
	first         *image.NRGBA
}

func sanitizeAnimatedWebP(data []byte) (animatedWebPResult, error) {
	if len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		return animatedWebPResult{}, errBadWebP
	}
	riffSize := int(binary.LittleEndian.Uint32(data[4:8]))
	if riffSize < 4 || riffSize > len(data)-8 {
		return animatedWebPResult{}, errBadWebP
	}
	chunks, err := riffChunks(data[12 : 8+riffSize])
	if err != nil {
		return animatedWebPResult{}, err
	}
	if len(chunks) < 3 || chunks[0].fourCC != "VP8X" || len(chunks[0].payload) < 10 || chunks[0].payload[0]&webpFlagAnimation == 0 {
		return animatedWebPResult{}, errBadWebP
	}
	head := chunks[0].payload
	res := animatedWebPResult{width: uint24(head[4:7]) + 1, height: uint24(head[7:10]) + 1}
	if res.width > MaxImageSide || res.height > MaxImageSide || res.width*res.height > MaxImagePixels {
		return animatedWebPResult{}, ErrImageTooLarge
	}
	vp8x := make([]byte, 10)
	copy(vp8x, head[:10])
	vp8x[0] &= webpFlagAlpha | webpFlagAnimation
	body := appendChunk(nil, "VP8X", vp8x)
	var frames int
	var area int64
	seenAnim := false
	for _, c := range chunks[1:] {
		switch c.fourCC {
		case "ANIM":
			if seenAnim || frames > 0 || len(c.payload) != 6 {
				return animatedWebPResult{}, errBadWebP
			}
			seenAnim = true
			body = appendChunk(body, "ANIM", c.payload)
		case "ANMF":
			if !seenAnim {
				return animatedWebPResult{}, errBadWebP
			}
			f, payload, err := parseANMF(c.payload, res.width, res.height)
			if err != nil {
				return animatedWebPResult{}, err
			}
			frames++
			area += int64(f.width) * int64(f.height)
			if frames > maxGIFFrames || area > maxGIFArea {
				return animatedWebPResult{}, ErrImageTooLarge
			}
			img, err := webp.Decode(bytes.NewReader(f.still()))
			if err != nil || img.Bounds().Dx() != f.width || img.Bounds().Dy() != f.height {
				return animatedWebPResult{}, errBadWebP
			}
			if res.first == nil {
				res.first = image.NewNRGBA(image.Rect(0, 0, res.width, res.height))
				draw.Draw(res.first, image.Rect(f.x, f.y, f.x+f.width, f.y+f.height), img, img.Bounds().Min, draw.Src)
			}
			body = appendChunk(body, "ANMF", payload)
		}
	}
	if frames == 0 {
		return animatedWebPResult{}, errBadWebP
	}
	res.data = riffWebP(body)
	return res, nil
}

func processAnimatedWebP(data []byte) (ProcessedImage, error) {
	res, err := sanitizeAnimatedWebP(data)
	if errors.Is(err, ErrImageTooLarge) {
		return ProcessedImage{}, err
	}
	if err != nil {
		return ProcessedImage{}, ErrUnsupported
	}
	thumb, err := thumbnail(res.first)
	if err != nil {
		return ProcessedImage{}, err
	}
	return ProcessedImage{
		Data: res.data, ContentType: "image/webp", Width: res.width, Height: res.height,
		Thumbnail: thumb, Source: res.first, Animated: true,
	}, nil
}
