package media

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"

	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // registers the WebP decoder
)

// Image limits: decompression bombs and absurd sizes are refused before decoding.
const (
	MaxImagePixels = 40_000_000
	MaxImageSide   = 16_384
	ThumbnailSide  = 320
	maxGIFFrames   = 300
	// maxGIFArea bounds the pixels of all frames together (one byte each once decoded).
	maxGIFArea = 4 * MaxImagePixels
)

// ErrImageTooLarge is returned for images over the limits.
var ErrImageTooLarge = errors.New("media: image too large")

// ProcessedImage is a re-encoded image and its thumbnail.
type ProcessedImage struct {
	Data        []byte
	ContentType string
	Width       int
	Height      int
	Thumbnail   []byte // JPEG
}

// ProcessImage decodes the image, applies the EXIF orientation of a JPEG, and encodes it again: metadata
// (location, camera, comments) is gone and a malformed file cannot reach a client. GIFs keep their frames;
// WebP becomes PNG (with transparency) or JPEG.
func ProcessImage(data []byte, contentType string) (ProcessedImage, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return ProcessedImage{}, ErrUnsupported
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > MaxImageSide || cfg.Height > MaxImageSide || cfg.Width*cfg.Height > MaxImagePixels {
		return ProcessedImage{}, ErrImageTooLarge
	}
	if contentType == "image/gif" {
		return processGIF(data, cfg)
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return ProcessedImage{}, ErrUnsupported
	}
	if contentType == "image/jpeg" {
		img = orient(img, jpegOrientation(data))
	}
	var buf bytes.Buffer
	out := ProcessedImage{Width: img.Bounds().Dx(), Height: img.Bounds().Dy()}
	if contentType == "image/jpeg" || (contentType == "image/webp" && opaque(img)) {
		out.ContentType = "image/jpeg"
		err = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 88})
	} else {
		out.ContentType = "image/png"
		err = (&png.Encoder{CompressionLevel: png.DefaultCompression}).Encode(&buf, img)
	}
	if err != nil {
		return ProcessedImage{}, err
	}
	out.Data = buf.Bytes()
	if out.Thumbnail, err = thumbnail(img); err != nil {
		return ProcessedImage{}, err
	}
	return out, nil
}

func processGIF(data []byte, cfg image.Config) (ProcessedImage, error) {
	// DecodeAll holds every frame in memory, so the frames are counted and measured before decoding.
	frames, area, err := scanGIF(data)
	if err != nil {
		return ProcessedImage{}, ErrUnsupported
	}
	if frames > maxGIFFrames || area > maxGIFArea {
		return ProcessedImage{}, ErrImageTooLarge
	}
	g, err := gif.DecodeAll(bytes.NewReader(data))
	if err != nil || len(g.Image) == 0 {
		return ProcessedImage{}, ErrUnsupported
	}
	// EncodeAll writes frames, timing and the palette only: comments and application extensions are gone.
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, g); err != nil {
		return ProcessedImage{}, err
	}
	thumb, err := thumbnail(g.Image[0])
	if err != nil {
		return ProcessedImage{}, err
	}
	return ProcessedImage{Data: buf.Bytes(), ContentType: "image/gif", Width: cfg.Width, Height: cfg.Height, Thumbnail: thumb}, nil
}

var errBadGIF = errors.New("media: malformed GIF")

// scanGIF walks the blocks of a GIF without decoding it: the number of frames and their pixels together.
// It stops early once either is over the limits.
func scanGIF(data []byte) (frames int, area int64, err error) {
	if len(data) < 13 || (string(data[:6]) != "GIF87a" && string(data[:6]) != "GIF89a") {
		return 0, 0, errBadGIF
	}
	pos := 13
	if data[10]&0x80 != 0 { // global color table
		pos += 3 << (int(data[10]&0x07) + 1)
	}
	// subBlocks skips data sub-blocks up to and including the terminator.
	subBlocks := func() bool {
		for pos < len(data) {
			n := int(data[pos])
			pos++
			if n == 0 {
				return true
			}
			pos += n
		}
		return false
	}
	for pos < len(data) {
		switch data[pos] {
		case 0x21: // extension: label, then sub-blocks
			pos += 2
			if !subBlocks() {
				return 0, 0, errBadGIF
			}
		case 0x2C: // image descriptor
			if pos+10 > len(data) {
				return 0, 0, errBadGIF
			}
			w, h := int64(binary.LittleEndian.Uint16(data[pos+5:])), int64(binary.LittleEndian.Uint16(data[pos+7:]))
			flags := data[pos+9]
			pos += 10
			if flags&0x80 != 0 { // local color table
				pos += 3 << (int(flags&0x07) + 1)
			}
			pos++ // LZW minimum code size
			if !subBlocks() {
				return 0, 0, errBadGIF
			}
			frames++
			area += w * h
			if frames > maxGIFFrames || area > maxGIFArea {
				return frames, area, nil
			}
		case 0x3B: // trailer
			return frames, area, nil
		default:
			return 0, 0, errBadGIF
		}
	}
	return frames, area, nil // no trailer: the decoder accepts that too
}

func opaque(img image.Image) bool {
	if o, ok := img.(interface{ Opaque() bool }); ok {
		return o.Opaque()
	}
	return false
}

// thumbnail scales the image so its longer side is at most ThumbnailSide, on white (JPEG has no alpha).
func thumbnail(img image.Image) ([]byte, error) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w > ThumbnailSide || h > ThumbnailSide {
		if w >= h {
			h, w = max(1, h*ThumbnailSide/w), ThumbnailSide
		} else {
			w, h = max(1, w*ThumbnailSide/h), ThumbnailSide
		}
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(dst, dst.Bounds(), &image.Uniform{C: color.White}, image.Point{}, draw.Src)
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Over, nil)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 80}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// jpegOrientation reads the EXIF orientation (1-8) of a JPEG; 1 when there is none.
func jpegOrientation(data []byte) int {
	r := bytes.NewReader(data)
	var marker [2]byte
	if _, err := io.ReadFull(r, marker[:]); err != nil || marker != [2]byte{0xFF, 0xD8} {
		return 1
	}
	for {
		if _, err := io.ReadFull(r, marker[:]); err != nil || marker[0] != 0xFF {
			return 1
		}
		if marker[1] == 0xDA || marker[1] == 0xD9 { // start of scan, end of image
			return 1
		}
		var size uint16
		if err := binary.Read(r, binary.BigEndian, &size); err != nil || size < 2 {
			return 1
		}
		seg := make([]byte, int(size)-2)
		if _, err := io.ReadFull(r, seg); err != nil {
			return 1
		}
		if marker[1] == 0xE1 && len(seg) > 6 && string(seg[:6]) == "Exif\x00\x00" {
			return exifOrientation(seg[6:])
		}
	}
}

func exifOrientation(tiff []byte) int {
	if len(tiff) < 8 {
		return 1
	}
	var bo binary.ByteOrder
	switch string(tiff[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 1
	}
	off := int(bo.Uint32(tiff[4:8]))
	if off+2 > len(tiff) {
		return 1
	}
	n := int(bo.Uint16(tiff[off : off+2]))
	for i := range n {
		e := off + 2 + i*12
		if e+12 > len(tiff) {
			return 1
		}
		if bo.Uint16(tiff[e:e+2]) == 0x0112 { // Orientation
			v := int(bo.Uint16(tiff[e+8 : e+10]))
			if v >= 1 && v <= 8 {
				return v
			}
			return 1
		}
	}
	return 1
}

// orient turns the image the way its EXIF orientation says it is meant to be seen. It works on RGBA pixels
// directly: one copy of four bytes per pixel.
func orient(img image.Image, o int) image.Image {
	if o <= 1 || o > 8 {
		return img
	}
	b := img.Bounds()
	src, ok := img.(*image.RGBA)
	if !ok || b.Min != (image.Point{}) {
		src = image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
		draw.Draw(src, src.Bounds(), img, b.Min, draw.Src)
	}
	w, h := b.Dx(), b.Dy()
	dw, dh := w, h
	if o >= 5 {
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := range h {
		row := src.Pix[y*src.Stride : y*src.Stride+4*w]
		for x := range w {
			var dx, dy int
			switch o {
			case 2:
				dx, dy = w-1-x, y
			case 3:
				dx, dy = w-1-x, h-1-y
			case 4:
				dx, dy = x, h-1-y
			case 5:
				dx, dy = y, x
			case 6:
				dx, dy = h-1-y, x
			case 7:
				dx, dy = h-1-y, w-1-x
			case 8:
				dx, dy = y, w-1-x
			}
			i := dy*dst.Stride + 4*dx
			copy(dst.Pix[i:i+4], row[4*x:4*x+4])
		}
	}
	return dst
}
