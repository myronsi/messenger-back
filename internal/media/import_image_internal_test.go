package media

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image/png"
	"testing"
)

func pngChunk(name string, payload []byte) []byte {
	out := binary.BigEndian.AppendUint32(nil, uint32(len(payload))) //nolint:gosec // small test payloads
	body := append([]byte(name), payload...)
	out = append(out, body...)
	return binary.BigEndian.AppendUint32(out, crc32.ChecksumIEEE(body))
}

func withPNGChunk(t *testing.T, data []byte, name string, payload []byte) []byte {
	t.Helper()
	ihdrEnd := len(pngSignature) + 12 + 13
	out := append([]byte{}, data[:ihdrEnd]...)
	out = append(out, pngChunk(name, payload)...)
	return append(out, data[ihdrEnd:]...)
}

func TestPrepareImportImageStripsMetadataWithoutDecoding(t *testing.T) {
	jpg, err := prepareImportImage(jpegWithOrientation(t, 40, 20, 1), "image/jpeg")
	if err != nil {
		t.Fatal(err)
	}
	if jpg.width != 40 || jpg.height != 20 || bytes.Contains(jpg.data, []byte("Exif")) || bytes.Contains(jpg.data, []byte("GPSLatitude")) {
		t.Fatalf("jpeg %dx%d, metadata kept: %v", jpg.width, jpg.height, bytes.Contains(jpg.data, []byte("GPSLatitude")))
	}

	tagged := withPNGChunk(t, pngBytes(t, 30, 10), "tEXt", []byte("Comment\x00GPS 52.5 13.4"))
	pngOut, err := prepareImportImage(tagged, "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(pngOut.data, []byte("GPS")) || pngOut.width != 30 || pngOut.height != 10 {
		t.Fatalf("png %dx%d, text kept: %v", pngOut.width, pngOut.height, bytes.Contains(pngOut.data, []byte("GPS")))
	}
	if _, err := png.Decode(bytes.NewReader(pngOut.data)); err != nil {
		t.Fatalf("stripped png does not decode: %v", err)
	}
}

func TestPrepareImportImageLeavesTheRestToTheNormalPath(t *testing.T) {
	cases := map[string]struct {
		data        []byte
		contentType string
		want        error
	}{
		"rotated jpeg":   {jpegWithOrientation(t, 40, 20, 6), "image/jpeg", ErrUnsupported},
		"gif":            {[]byte("GIF89a"), "image/gif", ErrUnsupported},
		"broken png crc": {append(pngBytes(t, 4, 4)[:20], 0, 0, 0, 0), "image/png", ErrUnsupported},
		"animated webp":  {fixture(t, "animated.webp"), "image/webp", ErrUnsupported},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := prepareImportImage(tc.data, tc.contentType); !errors.Is(err, tc.want) {
				t.Fatalf("got %v", err)
			}
		})
	}
	huge := pngBytes(t, 4, 4)
	ihdr := len(pngSignature) + 8
	binary.BigEndian.PutUint32(huge[ihdr:], 20000)
	binary.BigEndian.PutUint32(huge[ihdr+4:], 20000)
	binary.BigEndian.PutUint32(huge[ihdr+13:], crc32.ChecksumIEEE(huge[ihdr-4:ihdr+13]))
	if _, err := prepareImportImage(huge, "image/png"); !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("huge png: %v", err)
	}
}

func TestStripStillWebPDropsMetadataChunks(t *testing.T) {
	src := fixture(t, "animated.webp")
	chunks, err := riffChunks(src[12:])
	if err != nil {
		t.Fatal(err)
	}
	var frame []byte
	for _, c := range chunks {
		if c.fourCC == "ANMF" {
			frame = c.payload[anmfHeaderSize:]
			break
		}
	}
	inner, err := riffChunks(frame)
	if err != nil {
		t.Fatal(err)
	}
	vp8x := make([]byte, 10)
	vp8x[0] = 0x08 | 0x04
	putUint24(vp8x[4:7], 47)
	putUint24(vp8x[7:10], 31)
	body := appendChunk(nil, "VP8X", vp8x)
	body = appendChunk(body, "ICCP", []byte("profile"))
	for _, c := range inner {
		body = appendChunk(body, c.fourCC, c.payload)
	}
	body = appendChunk(body, "EXIF", []byte("GPS 52.5 13.4"))
	still := riffWebP(body)
	out, err := prepareImportImage(still, "image/webp")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"EXIF", "GPS", "ICCP", "profile"} {
		if bytes.Contains(out.data, []byte(s)) {
			t.Errorf("%q kept", s)
		}
	}
	if out.width != 48 || out.height != 32 {
		t.Fatalf("size %dx%d", out.width, out.height)
	}
}
