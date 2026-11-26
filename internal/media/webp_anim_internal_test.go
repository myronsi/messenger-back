package media

import (
	"bytes"
	"errors"
	"os"
	"testing"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func rebuildWebP(t *testing.T, data []byte, edit func([]riffChunk) []riffChunk) []byte {
	t.Helper()
	chunks, err := riffChunks(data[12:])
	if err != nil {
		t.Fatal(err)
	}
	var body []byte
	for _, c := range edit(chunks) {
		body = appendChunk(body, c.fourCC, c.payload)
	}
	return riffWebP(body)
}

func TestAnimatedWebPKeepsItsFrames(t *testing.T) {
	for _, tc := range []struct {
		file          string
		width, height int
		alpha         bool
	}{
		{file: "animated.webp", width: 48, height: 32},
		{file: "animated-alpha.webp", width: 40, height: 40, alpha: true},
	} {
		t.Run(tc.file, func(t *testing.T) {
			src := fixture(t, tc.file)
			out, err := ProcessImage(src, "image/webp")
			if err != nil {
				t.Fatal(err)
			}
			if !out.Animated || out.ContentType != "image/webp" || out.Width != tc.width || out.Height != tc.height ||
				len(out.Thumbnail) == 0 || out.Source == nil || !animatedWebP(out.Data) {
				t.Fatalf("processed %+v", out)
			}
			srcChunks, _ := riffChunks(src[12:])
			outChunks, _ := riffChunks(out.Data[12:])
			if count(srcChunks, "ANMF") != count(outChunks, "ANMF") || count(outChunks, "ANMF") < 2 {
				t.Fatalf("frames %d -> %d", count(srcChunks, "ANMF"), count(outChunks, "ANMF"))
			}
			_, _, _, a := out.Source.At(30, 20).RGBA()
			if tc.alpha != (a < 0xffff) {
				t.Fatalf("first frame alpha %d", a)
			}
			again, err := ProcessImage(out.Data, "image/webp")
			if err != nil || !bytes.Equal(again.Data, out.Data) {
				t.Fatalf("processing the result again changed it: %v", err)
			}
		})
	}
}

func count(chunks []riffChunk, fourCC string) int {
	n := 0
	for _, c := range chunks {
		if c.fourCC == fourCC {
			n++
		}
	}
	return n
}

func TestAnimatedWebPDropsMetadata(t *testing.T) {
	src := rebuildWebP(t, fixture(t, "animated.webp"), func(cs []riffChunk) []riffChunk {
		vp8x := append([]byte{}, cs[0].payload...)
		vp8x[0] |= 0x20 | 0x08 | 0x04
		frame := append([]byte{}, cs[2].payload[:anmfHeaderSize]...)
		frame = appendChunk(frame, "XYZW", []byte("hidden"))
		frame = append(frame, cs[2].payload[anmfHeaderSize:]...)
		out := []riffChunk{{"VP8X", vp8x}, {"ICCP", []byte("profile")}, cs[1], {"ANMF", frame}}
		out = append(out, cs[3:]...)
		return append(out, riffChunk{"EXIF", []byte("GPS 52.5 13.4")}, riffChunk{"XMP ", []byte("<x:xmpmeta/>")})
	})
	out, err := ProcessImage(src, "image/webp")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"ICCP", "EXIF", "XMP ", "XYZW", "GPS", "hidden", "profile"} {
		if bytes.Contains(out.Data, []byte(s)) {
			t.Errorf("%q kept", s)
		}
	}
	chunks, _ := riffChunks(out.Data[12:])
	if chunks[0].payload[0] != webpFlagAnimation {
		t.Fatalf("VP8X flags %#x", chunks[0].payload[0])
	}
}

func TestAnimatedWebPRejectsBrokenFiles(t *testing.T) {
	src := fixture(t, "animated.webp")
	cases := map[string]struct {
		data []byte
		want error
	}{
		"truncated": {data: src[:len(src)-40], want: ErrUnsupported},
		"frame outside the canvas": {data: rebuildWebP(t, src, func(cs []riffChunk) []riffChunk {
			frame := append([]byte{}, cs[2].payload...)
			putUint24(frame[0:3], 20)
			cs[2].payload = frame
			return cs
		}), want: ErrUnsupported},
		"corrupt frame": {data: rebuildWebP(t, src, func(cs []riffChunk) []riffChunk {
			frame := append([]byte{}, cs[2].payload...)
			for i := anmfHeaderSize + 8; i < len(frame); i++ {
				frame[i] = 0xA5
			}
			cs[2].payload = frame
			return cs
		}), want: ErrUnsupported},
		"no frames": {data: rebuildWebP(t, src, func(cs []riffChunk) []riffChunk {
			return append(cs[:2], riffChunk{"EXIF", []byte("x")})
		}), want: ErrUnsupported},
		"frame before ANIM": {data: rebuildWebP(t, src, func(cs []riffChunk) []riffChunk {
			return append([]riffChunk{cs[0], cs[2], cs[1]}, cs[3:]...)
		}), want: ErrUnsupported},
		"huge canvas": {data: rebuildWebP(t, src, func(cs []riffChunk) []riffChunk {
			vp8x := append([]byte{}, cs[0].payload...)
			putUint24(vp8x[4:7], 20000)
			cs[0].payload = vp8x
			return cs
		}), want: ErrImageTooLarge},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ProcessImage(tc.data, "image/webp"); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}
