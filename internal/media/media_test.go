package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"strings"
	"testing"
)

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := range w {
		img.Set(x, 0, color.RGBA{R: 255, A: 255})
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// jpegWithOrientation builds a JPEG with an EXIF APP1 segment that carries the orientation and a fake GPS
// tag text, so the test can check that metadata is dropped.
func jpegWithOrientation(t *testing.T, w, h, orientation int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := range w {
		for y := range h {
			c := color.RGBA{B: 255, A: 255}
			if x < 10 && y < 5 {
				c = color.RGBA{R: 255, A: 255} // a red top-left block to follow the rotation
			}
			img.Set(x, y, c)
		}
	}
	var plain bytes.Buffer
	if err := jpeg.Encode(&plain, img, &jpeg.Options{Quality: 100}); err != nil {
		t.Fatal(err)
	}
	tiff := []byte("II*\x00\x08\x00\x00\x00\x01\x00\x12\x01\x03\x00\x01\x00\x00\x00")
	tiff = binary.LittleEndian.AppendUint16(tiff, uint16(orientation))
	tiff = append(tiff, 0, 0, 0, 0, 0, 0)
	payload := append([]byte("Exif\x00\x00"), tiff...)
	payload = append(payload, []byte("GPSLatitude=48.137")...)
	app1 := []byte{0xFF, 0xE1}
	app1 = binary.BigEndian.AppendUint16(app1, uint16(len(payload)+2))
	app1 = append(app1, payload...)
	raw := plain.Bytes()
	return append(append(append([]byte{}, raw[:2]...), app1...), raw[2:]...)
}

func TestDetect(t *testing.T) {
	png := pngBytes(t, 4, 4)
	cases := []struct {
		name          string
		head          []byte
		purpose, kind string
		wantKind      string
		wantType      string
		wantErr       bool
	}{
		{"png", png, PurposeMessage, "", KindImage, "image/png", false},
		{"html is a plain file", []byte("<!DOCTYPE html><script>alert(1)</script>"), PurposeMessage, "", KindFile, "application/octet-stream", false},
		{"svg is a plain file", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script/></svg>`), PurposeMessage, "", KindFile, "application/octet-stream", false},
		{"pdf", []byte("%PDF-1.7\n"), PurposeMessage, "", KindFile, "application/pdf", false},
		{"ogg voice", []byte("OggS\x00\x02\x00\x00"), PurposeMessage, KindVoice, KindVoice, "audio/ogg", false},
		{"webm voice", []byte{0x1A, 0x45, 0xDF, 0xA3, 0x9F}, PurposeMessage, KindVoice, KindVoice, "audio/webm", false},
		{"text as voice", []byte("hello"), PurposeMessage, KindVoice, "", "", true},
		{"html as avatar", []byte("<html>"), PurposeAvatar, "", "", "", true},
		{"png avatar", png, PurposeAvatar, "", KindImage, "image/png", false},
		{"text claiming to be an image", []byte("GIF8 not really"), PurposeMessage, KindImage, "", "", true},
	}
	for _, c := range cases {
		d, err := Detect(c.head, c.purpose, c.kind)
		if (err != nil) != c.wantErr || d.Kind != c.wantKind || d.ContentType != c.wantType {
			t.Errorf("%s: %+v %v", c.name, d, err)
		}
	}
	if Inline("application/octet-stream") || Inline("text/html") || !Inline("image/png") || !Inline("audio/ogg") {
		t.Error("Inline")
	}
}

func TestProcessImageStripsMetadataAndRotates(t *testing.T) {
	src := jpegWithOrientation(t, 40, 20, 6) // rotate 90° clockwise to display
	if jpegOrientation(src) != 6 {
		t.Fatal("orientation not read")
	}
	out, err := ProcessImage(src, "image/jpeg")
	if err != nil {
		t.Fatal(err)
	}
	if out.ContentType != "image/jpeg" || out.Width != 20 || out.Height != 40 {
		t.Fatalf("result %s %dx%d", out.ContentType, out.Width, out.Height)
	}
	if bytes.Contains(out.Data, []byte("Exif")) || bytes.Contains(out.Data, []byte("GPSLatitude")) {
		t.Fatal("metadata survived")
	}
	img, err := jpeg.Decode(bytes.NewReader(out.Data))
	if err != nil {
		t.Fatal(err)
	}
	// The red block moved from top-left to top-right: (x, y) -> (height-1-y, x).
	if r, _, b, _ := img.At(17, 4).RGBA(); r < 0xC000 || b > 0x4000 {
		t.Fatalf("not rotated: %v", img.At(17, 4))
	}
	if len(out.Thumbnail) == 0 {
		t.Fatal("no thumbnail")
	}
	th, err := jpeg.Decode(bytes.NewReader(out.Thumbnail))
	if err != nil || th.Bounds().Dx() > ThumbnailSide || th.Bounds().Dy() > ThumbnailSide {
		t.Fatalf("thumbnail: %v %v", th.Bounds(), err)
	}
}

func TestProcessImageRefusesBombsAndJunk(t *testing.T) {
	// A PNG header that claims 100000 x 100000 pixels.
	ihdr := []byte("IHDR")
	ihdr = binary.BigEndian.AppendUint32(ihdr, 100000)
	ihdr = binary.BigEndian.AppendUint32(ihdr, 100000)
	ihdr = append(ihdr, 8, 6, 0, 0, 0)
	var b bytes.Buffer
	b.Write([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\x0d"))
	b.Write(ihdr)
	_ = binary.Write(&b, binary.BigEndian, crc32.ChecksumIEEE(ihdr))
	if _, err := ProcessImage(b.Bytes(), "image/png"); !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("bomb: %v", err)
	}
	if _, err := ProcessImage([]byte("\x89PNG\r\n\x1a\nnope"), "image/png"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("junk: %v", err)
	}
	// GIF frames survive.
	pal := []color.Color{color.Black, color.White}
	g := &gif.GIF{Image: []*image.Paletted{image.NewPaletted(image.Rect(0, 0, 4, 4), pal), image.NewPaletted(image.Rect(0, 0, 4, 4), pal)}, Delay: []int{5, 5}}
	var gb bytes.Buffer
	if err := gif.EncodeAll(&gb, g); err != nil {
		t.Fatal(err)
	}
	out, err := ProcessImage(gb.Bytes(), "image/gif")
	if err != nil {
		t.Fatal(err)
	}
	back, err := gif.DecodeAll(bytes.NewReader(out.Data))
	if err != nil || len(back.Image) != 2 {
		t.Fatalf("gif frames: %v", err)
	}
}

func TestBarsAndClientAudio(t *testing.T) {
	pcm := make([]byte, 0, 800)
	for i := range 400 {
		v := int16(0)
		if i >= 200 {
			v = 16000
		}
		pcm = binary.LittleEndian.AppendUint16(pcm, uint16(v))
	}
	bars := Bars(pcm, 4)
	if bars[0] != 0 || bars[1] != 0 || bars[2] != 255 || bars[3] != 255 {
		t.Fatalf("bars %v", bars)
	}
	ms := -5
	if info := ClientAudio(&ms, []int{-3, 300, 7}); info.Duration != 0 || info.Waveform[0] != 0 || info.Waveform[1] != 255 || info.Waveform[2] != 7 {
		t.Fatalf("client audio %+v", info)
	}
}

func TestDiskStorage(t *testing.T) {
	d, err := NewDisk(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := d.Put(ctx, "attachments/abc", strings.NewReader("hello"), 5, "text/plain"); err != nil {
		t.Fatal(err)
	}
	obj, err := d.Get(ctx, "attachments/abc")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(obj.Body)
	_ = obj.Body.Close()
	if string(b) != "hello" || obj.Size != 5 {
		t.Fatalf("read %q (%d)", b, obj.Size)
	}
	for _, bad := range []string{"../escape", "/abs", "UPPER", "a b", ""} {
		if err := d.Put(ctx, bad, strings.NewReader("x"), 1, ""); err == nil {
			t.Errorf("accepted key %q", bad)
		}
	}
	if err := d.Delete(ctx, "attachments/abc"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Get(ctx, "attachments/abc"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
	if err := d.Delete(ctx, "attachments/abc"); err != nil {
		t.Fatal("deleting twice failed")
	}
	if u, err := d.SignedURL(ctx, "attachments/abc", 0, "", ""); u != "" || err != nil {
		t.Fatal("disk signed a URL")
	}
}

func TestCleanName(t *testing.T) {
	cases := map[string]string{
		"../../etc/passwd":           "passwd",
		`C:\Users\me\report "q".pdf`: "report q.pdf",
		"":                           "file.png",
		"bell\x07.txt":               "bell.txt",
	}
	for in, want := range cases {
		if got := cleanName(in, "image/png"); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
	if long := cleanName(strings.Repeat("ä", 200), ""); len(long) > 255 {
		t.Errorf("name of %d bytes", len(long))
	}
}
