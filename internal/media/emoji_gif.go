package media

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/gif"

	xdraw "golang.org/x/image/draw"
)

func fitInside(w, h, side int) (int, int) {
	if w <= side && h <= side {
		return w, h
	}
	if w >= h {
		return side, max(1, h*side/w)
	}
	return max(1, w*side/h), side
}

func withTransparent(p color.Palette) (color.Palette, uint8) {
	for i, c := range p {
		if _, _, _, a := c.RGBA(); a == 0 {
			return p, uint8(i) //nolint:gosec // a palette has at most 256 entries
		}
	}
	out := append(color.Palette(nil), p...)
	if len(out) >= 256 {
		out = out[:255]
	}
	out = append(out, color.Transparent)
	return out, uint8(len(out) - 1) //nolint:gosec // at most 256 entries
}

func ScaleGIF(data []byte, side int) ([]byte, int, int, error) {
	frames, area, err := scanGIF(data)
	if err != nil {
		return nil, 0, 0, ErrUnsupported
	}
	if frames > maxGIFFrames || area > maxGIFArea {
		return nil, 0, 0, ErrImageTooLarge
	}
	g, err := gif.DecodeAll(bytes.NewReader(data))
	if err != nil || len(g.Image) == 0 {
		return nil, 0, 0, ErrUnsupported
	}
	bounds := image.Rect(0, 0, g.Config.Width, g.Config.Height)
	if bounds.Empty() {
		bounds = g.Image[0].Bounds()
	}
	w, h := fitInside(bounds.Dx(), bounds.Dy(), side)
	canvas := image.NewRGBA(bounds)
	out := &gif.GIF{LoopCount: g.LoopCount, Config: image.Config{Width: w, Height: h}}
	for i, frame := range g.Image {
		disposal := byte(0)
		if i < len(g.Disposal) {
			disposal = g.Disposal[i]
		}
		var saved *image.RGBA
		if disposal == gif.DisposalPrevious {
			saved = image.NewRGBA(bounds)
			draw.Draw(saved, bounds, canvas, bounds.Min, draw.Src)
		}
		draw.Draw(canvas, frame.Bounds(), frame, frame.Bounds().Min, draw.Over)
		scaled := image.NewRGBA(image.Rect(0, 0, w, h))
		xdraw.CatmullRom.Scale(scaled, scaled.Bounds(), canvas, bounds, xdraw.Src, nil)
		palette, transparent := withTransparent(frame.Palette)
		paletted := image.NewPaletted(scaled.Bounds(), palette)
		draw.FloydSteinberg.Draw(paletted, paletted.Bounds(), scaled, image.Point{})
		for p := range paletted.Pix {
			if scaled.Pix[p*4+3] < 128 {
				paletted.Pix[p] = transparent
			}
		}
		out.Image = append(out.Image, paletted)
		out.Delay = append(out.Delay, delayOf(g, i))
		out.Disposal = append(out.Disposal, gif.DisposalBackground)
		switch disposal {
		case gif.DisposalBackground:
			draw.Draw(canvas, frame.Bounds(), image.Transparent, image.Point{}, draw.Src)
		case gif.DisposalPrevious:
			canvas = saved
		}
	}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, out); err != nil {
		return nil, 0, 0, err
	}
	return buf.Bytes(), w, h, nil
}

func delayOf(g *gif.GIF, i int) int {
	if i < len(g.Delay) {
		return g.Delay[i]
	}
	return 10
}
