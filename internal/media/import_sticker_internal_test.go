package media

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
	"time"
)

func squareSticker(t *testing.T, frameMS int, extra ...riffChunk) []byte {
	t.Helper()
	src := fixture(t, "animated.webp")
	chunks, err := riffChunks(src[12:])
	if err != nil {
		t.Fatal(err)
	}
	var body []byte
	for _, c := range chunks {
		payload := append([]byte{}, c.payload...)
		switch c.fourCC {
		case "VP8X":
			payload[0] |= 0x08
			putUint24(payload[4:7], StickerSide-1)
			putUint24(payload[7:10], StickerSide-1)
		case "ANMF":
			if frameMS > 0 {
				putUint24(payload[12:15], frameMS)
			}
		}
		body = appendChunk(body, c.fourCC, payload)
	}
	for _, c := range extra {
		body = appendChunk(body, c.fourCC, c.payload)
	}
	return riffWebP(body)
}

func TestImportStickersKeepSquareAnimatedWebPWithoutMetadata(t *testing.T) {
	src := squareSticker(t, 0, riffChunk{fourCC: "EXIF", payload: []byte(`{"sticker-pack-id":"secret"}`)}, riffChunk{fourCC: "XMP ", payload: []byte("xmp")})
	got, err := prepareImportSticker(src, "image/webp")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"EXIF", "secret", "XMP "} {
		if bytes.Contains(got.data, []byte(s)) {
			t.Fatalf("metadata %q kept", s)
		}
	}
	if !animatedWebP(got.data) || got.data[20]&0x08 != 0 {
		t.Fatalf("flags %08b", got.data[20])
	}
	if got.duration == nil || *got.duration <= 0 || *got.duration > MaxStickerClip || !got.ready {
		t.Fatalf("duration %v ready %v", got.duration, got.ready)
	}
	if binary.LittleEndian.Uint32(got.data[4:8]) != uint32(len(got.data)-8) {
		t.Fatal("riff size")
	}
}

func TestImportStickersThatNeedWorkGoToTheWorkerToo(t *testing.T) {
	cases := map[string][]byte{
		"wrong canvas":     fixture(t, "animated.webp"),
		"longer than clip": squareSticker(t, int((MaxStickerClip + time.Second).Milliseconds())),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := prepareImportSticker(data, "image/webp")
			if err != nil || got.ready || got.duration == nil || *got.duration > MaxStickerClip {
				t.Fatalf("got %+v %v", got, err)
			}
		})
	}
	if _, err := prepareImportSticker([]byte("GIF89a not a webp file at all"), "image/gif"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("gif: %v", err)
	}
}
