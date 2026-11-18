package media

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"image"
	"image/color"
	"testing"
	"time"

	"github.com/google/uuid"
)

func gradient(w, h int, alpha uint8) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetNRGBA(x, y, color.NRGBA{R: uint8(x * 255 / w), G: uint8(y * 255 / h), B: 128, A: alpha})
		}
	}
	return img
}

func TestPlaceholderIsSmallStableAndMarksAlpha(t *testing.T) {
	opaqueHash := Placeholder(gradient(400, 300, 255))
	if opaqueHash == "" || len(opaqueHash) > 64 {
		t.Fatalf("placeholder %q", opaqueHash)
	}
	if again := Placeholder(gradient(400, 300, 255)); again != opaqueHash {
		t.Fatalf("not stable: %q vs %q", again, opaqueHash)
	}
	raw, err := base64.StdEncoding.DecodeString(opaqueHash)
	if err != nil || len(raw) < 5 {
		t.Fatalf("decode %v len %d", err, len(raw))
	}
	if raw[2]&0x80 != 0 {
		t.Fatal("opaque image marked as having alpha")
	}
	if raw[4]&0x80 == 0 {
		t.Fatal("landscape image not marked as landscape")
	}
	clear, _ := base64.StdEncoding.DecodeString(Placeholder(gradient(300, 400, 100)))
	if clear[2]&0x80 == 0 {
		t.Fatal("transparent image not marked as having alpha")
	}
	if Placeholder(nil) != "" {
		t.Fatal("nil image has a placeholder")
	}
}

func box(kind string, size int) []byte {
	b := make([]byte, size)
	binary.BigEndian.PutUint32(b, uint32(size))
	copy(b[4:8], kind)
	return b
}

func TestMoovAfterMdat(t *testing.T) {
	late := bytes.Join([][]byte{box("ftyp", 24), box("mdat", 1000), box("moov", 200)}, nil)
	if !MoovAfterMdat(bytes.NewReader(late), int64(len(late))) {
		t.Fatal("moov after mdat not found")
	}
	early := bytes.Join([][]byte{box("ftyp", 24), box("moov", 200), box("mdat", 1000)}, nil)
	if MoovAfterMdat(bytes.NewReader(early), int64(len(early))) {
		t.Fatal("faststart file reported as late")
	}
	broken := append(box("ftyp", 24), 0, 0, 0, 3, 'm', 'd', 'a', 't')
	if MoovAfterMdat(bytes.NewReader(broken), int64(len(broken))) {
		t.Fatal("broken box reported as late")
	}
}

func TestParseStreamInfoSkipsCoverArt(t *testing.T) {
	out := []byte(`{"streams":[
		{"codec_type":"video","codec_name":"mjpeg","width":600,"height":600,"disposition":{"attached_pic":1}},
		{"codec_type":"audio","codec_name":"flac","bit_rate":"900000"},
		{"codec_type":"video","codec_name":"hevc","pix_fmt":"yuv420p10le","width":3840,"height":2160}
	],"format":{"format_name":"mov,mp4,m4a,3gp,3g2,mj2","duration":"12.5","bit_rate":"40000000"}}`)
	info, err := parseStreamInfo(out)
	if err != nil {
		t.Fatal(err)
	}
	if info.VideoCodec != "hevc" || info.Width != 3840 || info.AudioCodec != "flac" || info.Duration != 12500*time.Millisecond || info.Bitrate != 40_000_000 {
		t.Fatalf("info %+v", info)
	}
	if _, err := parseStreamInfo([]byte("nope")); err == nil {
		t.Fatal("junk parsed")
	}
}

func TestTranscodeDecisions(t *testing.T) {
	videos := []struct {
		info StreamInfo
		want bool
	}{
		{StreamInfo{VideoCodec: "h264", PixFmt: "yuv420p", AudioCodec: "aac", Width: 1280, Height: 720, Bitrate: 2_000_000}, false},
		{StreamInfo{VideoCodec: "h264", PixFmt: "yuv420p", Width: 720, Height: 1280, Bitrate: 2_900_000}, false},
		{StreamInfo{VideoCodec: "hevc", PixFmt: "yuv420p", AudioCodec: "aac", Width: 1280, Height: 720}, true},
		{StreamInfo{VideoCodec: "h264", PixFmt: "yuv420p", AudioCodec: "aac", Width: 1920, Height: 1080, Bitrate: 2_000_000}, true},
		{StreamInfo{VideoCodec: "h264", PixFmt: "yuv420p", AudioCodec: "opus", Width: 640, Height: 360}, true},
		{StreamInfo{VideoCodec: "h264", PixFmt: "yuv420p10le", Width: 640, Height: 360}, true},
		{StreamInfo{VideoCodec: "h264", PixFmt: "yuv420p", Width: 640, Height: 360, Bitrate: 8_000_000}, true},
	}
	for i, c := range videos {
		if got := NeedsVideoTranscode(c.info); got != c.want {
			t.Errorf("video %d: got %v", i, got)
		}
	}
	audio := []struct {
		info StreamInfo
		want bool
	}{
		{StreamInfo{AudioCodec: "mp3", Bitrate: 128_000}, false},
		{StreamInfo{AudioCodec: "aac", Bitrate: 192_000}, false},
		{StreamInfo{AudioCodec: "mp3", Bitrate: 320_000}, true},
		{StreamInfo{AudioCodec: "flac", Bitrate: 150_000}, true},
		{StreamInfo{AudioCodec: "pcm_s16le"}, true},
		{StreamInfo{}, false},
	}
	for i, c := range audio {
		if got := NeedsAudioTranscode(c.info); got != c.want {
			t.Errorf("audio %d: got %v", i, got)
		}
	}
}

func ftyp(brand string, compatible ...string) []byte {
	b := append([]byte{0, 0, 0, 0}, []byte("ftyp"+brand+"\x00\x00\x00\x00")...)
	for _, c := range compatible {
		b = append(b, c...)
	}
	return append(b, make([]byte, 16)...)
}

func TestHEIFBrands(t *testing.T) {
	cases := map[string]string{
		string(ftyp("heic")):         "image/heic",
		string(ftyp("avif")):         "image/avif",
		string(ftyp("mif1", "avif")): "image/avif",
		string(ftyp("mif1", "heic")): "image/heic",
		string(ftyp("isom")):         "",
		"GIF89a":                     "",
	}
	for head, want := range cases {
		if got := heifType([]byte(head)); got != want {
			t.Errorf("%q: got %q want %q", head[:min(12, len(head))], got, want)
		}
	}
	det, err := Detect(ftyp("heic"), PurposeMessage, "")
	if err != nil || det.Kind != KindImage || det.ContentType != "image/heic" {
		t.Fatalf("detect %+v %v", det, err)
	}
}

func TestPlaybackToken(t *testing.T) {
	signer := NewPlaybackSigner("secret")
	now := time.Unix(1_800_000_000, 0)
	signer.now = func() time.Time { return now }
	id := uuid.New()
	token, expires := signer.Sign(id, 42)
	if !expires.Equal(now.Add(PlaybackTTL)) {
		t.Fatalf("expires %v", expires)
	}
	if viewer, ok := signer.Verify(token, id); !ok || viewer != 42 {
		t.Fatalf("verify %d %v", viewer, ok)
	}
	if _, ok := signer.Verify(token, uuid.New()); ok {
		t.Fatal("token works for another attachment")
	}
	if _, ok := NewPlaybackSigner("other").Verify(token, id); ok {
		t.Fatal("token works with another secret")
	}
	for _, bad := range []string{"", "1.2", "x.42.abc", token + "x"} {
		if _, ok := signer.Verify(bad, id); ok {
			t.Fatalf("accepted %q", bad)
		}
	}
	now = now.Add(PlaybackTTL + time.Second)
	if _, ok := signer.Verify(token, id); ok {
		t.Fatal("expired token accepted")
	}
}

func TestVariantKeysAndNames(t *testing.T) {
	vs := newVariantSet("attachments/x")
	vs.add(AvatarVariant(128), EncodedImage{Data: []byte("a"), ContentType: "image/webp", Width: 128, Height: 128})
	vs.add(VariantNameDisplay, EncodedImage{Data: []byte("b"), ContentType: "image/webp"})
	if got := vs.variants[AvatarVariant(128)].Key; got != "attachments/x.avatar-128.webp" {
		t.Fatalf("key %q", got)
	}
	if len(vs.puts) != 2 || len(vs.variants.Keys()) != 2 {
		t.Fatalf("puts %d keys %v", len(vs.puts), vs.variants.Keys())
	}
	for size, want := range map[int]Variant{1: VariantAvatar64, 64: VariantAvatar64, 65: VariantAvatar128, 256: VariantAvatar256, 257: VariantOriginal} {
		if got := AvatarSizeVariant(size); got != want {
			t.Errorf("size %d: got %v want %v", size, got, want)
		}
	}
	if swapExtension("IMG_0001.HEIC", "image/jpeg") != "IMG_0001.jpg" || swapExtension("clip", "video/mp4") != "clip.mp4" {
		t.Fatal("swapExtension")
	}
}
