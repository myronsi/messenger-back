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

func TestDisplayCopyOfLargeImagesOnly(t *testing.T) {
	out, err := ProcessImage(pngBytes(t, 1200, 3200), "image/png")
	if err != nil {
		t.Fatal(err)
	}
	display, err := DisplayImage(out.Data)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(display))
	if err != nil || cfg.Width != 600 || cfg.Height != DisplaySide {
		t.Fatalf("display copy: %+v %v", cfg, err)
	}
	if NeedsDisplay("image/gif", 4000, 4000) || !NeedsDisplay("image/jpeg", 1601, 10) || NeedsDisplay("application/pdf", 4000, 4000) || NeedsDisplay("image/png", DisplaySide, 900) {
		t.Fatal("NeedsDisplay")
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

// craftedGIF is a GIF of n frames that claim w x h pixels each, with empty image data: it is never decoded.
func craftedGIF(n int, w, h uint16) []byte {
	b := []byte("GIF89a\x01\x00\x01\x00\x00\x00\x00")
	for range n {
		b = append(b, 0x21, 0xF9, 4, 0, 5, 0, 0, 0) // graphic control extension
		b = append(b, 0x2C, 0, 0, 0, 0)
		b = binary.LittleEndian.AppendUint16(b, w)
		b = binary.LittleEndian.AppendUint16(b, h)
		b = append(b, 0, 2, 0) // no local palette, LZW code size, no data
	}
	return append(b, 0x3B)
}

func TestGIFFramesAreCountedBeforeDecoding(t *testing.T) {
	if frames, area, err := scanGIF(craftedGIF(3, 4, 5)); err != nil || frames != 3 || area != 60 {
		t.Fatalf("scan: %d frames, %d px, %v", frames, area, err)
	}
	if _, err := ProcessImage(craftedGIF(maxGIFFrames+1, 1, 1), "image/gif"); !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("too many frames: %v", err)
	}
	if _, err := ProcessImage(craftedGIF(5, 16000, 2500), "image/gif"); !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("too many pixels: %v", err)
	}
	if _, _, err := scanGIF([]byte("GIF89a\x01\x00\x01\x00\x00\x00\x00\x2C\x00")); err == nil {
		t.Fatal("truncated descriptor accepted")
	}
}

func segment(marker byte, payload []byte) []byte {
	s := binary.BigEndian.AppendUint16([]byte{0xFF, marker}, uint16(len(payload)+2))
	return append(s, payload...)
}

func TestProcessImageStripsExifWithoutReencoding(t *testing.T) {
	src := jpegWithOrientation(t, 40, 20, 1)
	jfif := append([]byte("JFIF\x00\x01\x02\x01\x00\x48\x00\x48\x01\x01"), []byte("THUMBPIXELS")...)
	extra := append(segment(0xE0, jfif), segment(0xE1, []byte("http://ns.adobe.com/xap/1.0/\x00<x:xmpmeta GPSAltitude/>"))...)
	extra = append(extra, segment(0xED, []byte("Photoshop 3.0\x00IPTC caption"))...)
	extra = append(extra, segment(0xE2, []byte("ICC_PROFILE\x00\x01\x01profile"))...)
	extra = append(extra, segment(0xFE, []byte("shot by someone"))...)
	src = append(append(append([]byte{}, src[:2]...), extra...), src[2:]...)
	src = append(src, []byte("\xFF\xD8appended picture Exif GPSLatitude\xFF\xD9")...)
	if jpegOrientation(src) != 1 {
		t.Fatal("orientation")
	}
	out, err := ProcessImage(src, "image/jpeg")
	if err != nil {
		t.Fatal(err)
	}
	if out.ContentType != "image/jpeg" || out.Width != 40 || out.Height != 20 || len(out.Thumbnail) == 0 {
		t.Fatalf("result %s %dx%d thumb %d", out.ContentType, out.Width, out.Height, len(out.Thumbnail))
	}
	for _, leak := range []string{"Exif", "GPS", "xmpmeta", "IPTC", "ICC_PROFILE", "shot by", "THUMBPIXELS", "appended"} {
		if bytes.Contains(out.Data, []byte(leak)) {
			t.Errorf("%q survived", leak)
		}
	}
	if !bytes.HasPrefix(out.Data, []byte("\xFF\xD8\xFF\xE0\x00\x10JFIF\x00\x01\x02\x01\x00\x48\x00\x48\x00\x00")) {
		t.Errorf("JFIF header not kept without its thumbnail: % x", out.Data[:24])
	}
	sos := bytes.Index(src, []byte{0xFF, 0xDA})
	eoi := bytes.Index(src, []byte("\xFF\xD9\xFF\xD8appended"))
	if sos < 0 || eoi < 0 || !bytes.HasSuffix(out.Data, src[sos:eoi+2]) {
		t.Fatal("the compressed pixels were not kept as they are")
	}
	want, err := jpeg.Decode(bytes.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	got, err := jpeg.Decode(bytes.NewReader(out.Data))
	if err != nil {
		t.Fatal(err)
	}
	for y := range 20 {
		for x := range 40 {
			if want.At(x, y) != got.At(x, y) {
				t.Fatalf("pixel %d,%d changed: %v -> %v", x, y, want.At(x, y), got.At(x, y))
			}
		}
	}
}

func TestStripJPEGSegments(t *testing.T) {
	scan := []byte{0x12, 0xFF, 0x00, 0x34, 0xFF, 0xD3, 0x56}
	src := []byte{0xFF, 0xD8}
	src = append(src, segment(0xE1, []byte("Exif\x00\x00GPS"))...)
	src = append(src, segment(0xEE, []byte("Adobe\x00\x64\x00\x00\x00\x00\x01"))...)
	src = append(src, segment(0xDB, []byte{0x00, 0x01})...)
	src = append(src, segment(0xC2, []byte{0x08, 0x00, 0x01})...)
	src = append(src, segment(0xDA, []byte{0x01, 0x01, 0x00})...)
	src = append(src, scan...)
	src = append(src, segment(0xFE, []byte("between scans"))...)
	src = append(src, segment(0xC4, []byte{0x00})...)
	src = append(src, segment(0xDA, []byte{0x01, 0x01, 0x00})...)
	src = append(src, scan...)
	src = append(src, 0xFF, 0xFF, 0xD9, 'x')
	want := []byte{0xFF, 0xD8}
	want = append(want, segment(0xEE, []byte("Adobe\x00\x64\x00\x00\x00\x00\x01"))...)
	want = append(want, segment(0xDB, []byte{0x00, 0x01})...)
	want = append(want, segment(0xC2, []byte{0x08, 0x00, 0x01})...)
	want = append(want, segment(0xDA, []byte{0x01, 0x01, 0x00})...)
	want = append(want, scan...)
	want = append(want, segment(0xC4, []byte{0x00})...)
	want = append(want, segment(0xDA, []byte{0x01, 0x01, 0x00})...)
	want = append(want, scan...)
	want = append(want, 0xFF, 0xD9)
	got, ok := stripJPEG(src)
	if !ok || !bytes.Equal(got, want) {
		t.Fatalf("stripJPEG = %v\n% x\nwant\n% x", ok, got, want)
	}
	for name, bad := range map[string][]byte{
		"no SOI":         src[2:],
		"truncated":      src[:len(src)-6],
		"short segment":  {0xFF, 0xD8, 0xFF, 0xDB, 0x00},
		"unknown marker": append([]byte{0xFF, 0xD8}, segment(0xF7, []byte{0x00})...),
		"garbage":        {0xFF, 0xD8, 0x00, 0x00},
	} {
		if _, ok := stripJPEG(bad); ok {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestProcessImageRotatedJPEGIsReencodedWithoutMetadata(t *testing.T) {
	src := jpegWithOrientation(t, 400, 200, 8)
	out, err := ProcessImage(src, "image/jpeg")
	if err != nil {
		t.Fatal(err)
	}
	if out.Width != 200 || out.Height != 400 || bytes.Contains(out.Data, []byte("GPSLatitude")) || bytes.Contains(out.Data, []byte("Exif")) {
		t.Fatalf("rotate path: %dx%d", out.Width, out.Height)
	}
	if bytes.HasSuffix(out.Data, src[bytes.Index(src, []byte{0xFF, 0xDA}):]) {
		t.Fatal("a rotated JPEG kept its original pixels")
	}
	th, err := jpeg.DecodeConfig(bytes.NewReader(out.Thumbnail))
	if err != nil || th.Width != 160 || th.Height != ThumbnailSide {
		t.Fatalf("thumbnail %+v %v", th, err)
	}
}

func TestScaledThumbnails(t *testing.T) {
	black := image.NewYCbCr(image.Rect(0, 0, 4000, 1000), image.YCbCrSubsampleRatio420)
	for i := range black.Cb {
		black.Cb[i], black.Cr[i] = 128, 128
	}
	for _, c := range []struct {
		img      image.Image
		w, h     int
		wantGray color.Gray
	}{
		{black, 320, 80, color.Gray{Y: 0}},
		{image.NewNRGBA(image.Rect(0, 0, 1000, 3000)), 106, 320, color.Gray{Y: 255}},
		{image.NewRGBA(image.Rect(0, 0, 100, 50)), 100, 50, color.Gray{Y: 255}},
	} {
		data, err := thumbnail(c.img)
		if err != nil {
			t.Fatal(err)
		}
		got, err := jpeg.Decode(bytes.NewReader(data))
		if err != nil || got.Bounds().Dx() != c.w || got.Bounds().Dy() != c.h {
			t.Fatalf("%T: %v %v", c.img, got.Bounds(), err)
		}
		if g := color.GrayModel.Convert(got.At(c.w/2, c.h/2)).(color.Gray); int(g.Y) > int(c.wantGray.Y)+8 || int(g.Y)+8 < int(c.wantGray.Y) {
			t.Errorf("%T: center %v, want about %v", c.img, g, c.wantGray)
		}
	}
}

func TestRefineWebM(t *testing.T) {
	video := Detected{Kind: KindVideo, ContentType: "video/webm"}
	if d := refineWebM(video, false, true); d.Kind != KindAudio || d.ContentType != "audio/webm" {
		t.Errorf("audio only: %+v", d)
	}
	if d := refineWebM(video, true, true); d != video {
		t.Errorf("video with sound: %+v", d)
	}
	if d := refineWebM(video, false, false); d != video {
		t.Errorf("no streams: %+v", d)
	}
	mp4 := Detected{Kind: KindVideo, ContentType: "video/mp4"}
	if d := refineWebM(mp4, false, true); d != mp4 {
		t.Errorf("mp4: %+v", d)
	}
}
