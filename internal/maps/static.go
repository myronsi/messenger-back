package maps

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"math"
)

const maxLatitude = 85.05112878

func staticKey(lat, lon float64) string {
	return fmt.Sprintf("maps/static/v1/%d/%s_%s.jpg", StaticZoom, FormatCoordinate(lat), FormatCoordinate(lon))
}

func worldPixel(lat, lon float64, zoom int) (float64, float64) {
	lat = math.Max(-maxLatitude, math.Min(maxLatitude, lat))
	scale := float64(TileSize) * math.Exp2(float64(zoom))
	x := (lon + 180) / 360 * scale
	rad := lat * math.Pi / 180
	y := (1 - math.Log(math.Tan(rad)+1/math.Cos(rad))/math.Pi) / 2 * scale
	return x, y
}

func (s *Service) Static(ctx context.Context, lat, lon float64, sig string) ([]byte, error) {
	if !s.Verify(lat, lon, sig) {
		return nil, ErrBadSignature
	}
	key := staticKey(lat, lon)
	if cached, err := s.read(ctx, key); err == nil && len(cached) > 0 {
		return cached, nil
	}
	v, err, _ := s.group.Do(key, func() (any, error) {
		return s.render(context.WithoutCancel(ctx), lat, lon, key)
	})
	if err != nil {
		return nil, err
	}
	return v.([]byte), nil
}

func (s *Service) render(ctx context.Context, lat, lon float64, key string) ([]byte, error) {
	cx, cy := worldPixel(lat, lon, StaticZoom)
	left, top := int(math.Floor(cx))-StaticWidth/2, int(math.Floor(cy))-StaticHeight/2
	canvas := image.NewRGBA(image.Rect(0, 0, StaticWidth, StaticHeight))
	draw.Draw(canvas, canvas.Bounds(), &image.Uniform{C: color.RGBA{0xe5, 0xe3, 0xdf, 0xff}}, image.Point{}, draw.Src)
	n := 1 << StaticZoom
	for ty := floorDiv(top, TileSize); ty <= floorDiv(top+StaticHeight-1, TileSize); ty++ {
		if ty < 0 || ty >= n {
			continue
		}
		for tx := floorDiv(left, TileSize); tx <= floorDiv(left+StaticWidth-1, TileSize); tx++ {
			data, err := s.Tile(ctx, StaticZoom, ((tx%n)+n)%n, ty)
			if err != nil {
				return nil, err
			}
			tile, err := png.Decode(bytes.NewReader(data))
			if err != nil {
				return nil, fmt.Errorf("%w: decode tile: %w", ErrUnavailable, err)
			}
			at := image.Pt(tx*TileSize-left, ty*TileSize-top)
			draw.Draw(canvas, tile.Bounds().Add(at), tile, tile.Bounds().Min, draw.Over)
		}
	}
	drawPin(canvas, int(cx)-left, int(cy)-top)
	var out bytes.Buffer
	if err := jpeg.Encode(&out, canvas, &jpeg.Options{Quality: 85}); err != nil {
		return nil, err
	}
	data := out.Bytes()
	_ = s.write(ctx, key, "image/jpeg", data)
	return data, nil
}

func floorDiv(a, b int) int {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

func drawPin(img *image.RGBA, x, y int) {
	red := color.RGBA{0xd9, 0x2d, 0x20, 0xff}
	white := color.RGBA{0xff, 0xff, 0xff, 0xff}
	headY := y - 18
	for dy := -12; dy <= 18; dy++ {
		for dx := -12; dx <= 12; dx++ {
			d := math.Hypot(float64(dx), float64(dy))
			inHead := d <= 11
			inTail := dy > 0 && math.Abs(float64(dx)) <= float64(18-dy)*0.5
			switch {
			case inHead && d <= 4:
				img.Set(x+dx, headY+dy, white)
			case inHead || inTail:
				img.Set(x+dx, headY+dy, red)
			case d <= 12.5 && dy <= 4:
				img.Set(x+dx, headY+dy, white)
			}
		}
	}
}
