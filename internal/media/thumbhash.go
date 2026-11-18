package media

import (
	"encoding/base64"
	"image"
	"image/draw"
	"math"

	xdraw "golang.org/x/image/draw"
)

const placeholderSide = 100

func Placeholder(img image.Image) string {
	if img == nil {
		return ""
	}
	b := img.Bounds()
	if b.Dx() <= 0 || b.Dy() <= 0 {
		return ""
	}
	w, h := b.Dx(), b.Dy()
	if w > placeholderSide || h > placeholderSide {
		if w >= h {
			h, w = max(1, h*placeholderSide/w), placeholderSide
		} else {
			w, h = max(1, w*placeholderSide/h), placeholderSide
		}
	}
	small := image.NewNRGBA(image.Rect(0, 0, w, h))
	xdraw.ApproxBiLinear.Scale(small, small.Bounds(), img, b, draw.Src, nil)
	return base64.StdEncoding.EncodeToString(thumbHash(w, h, small.Pix))
}

func thumbHash(w, h int, rgba []byte) []byte {
	n := w * h
	var avgR, avgG, avgB, avgA float64
	for i := range n {
		alpha := float64(rgba[i*4+3]) / 255
		avgR += alpha / 255 * float64(rgba[i*4])
		avgG += alpha / 255 * float64(rgba[i*4+1])
		avgB += alpha / 255 * float64(rgba[i*4+2])
		avgA += alpha
	}
	if avgA > 0 {
		avgR /= avgA
		avgG /= avgA
		avgB /= avgA
	}
	hasAlpha := avgA < float64(n)
	lLimit := 7.0
	if hasAlpha {
		lLimit = 5
	}
	longest := float64(max(w, h))
	lx := max(1, int(math.Round(lLimit*float64(w)/longest)))
	ly := max(1, int(math.Round(lLimit*float64(h)/longest)))
	l, p, q, a := make([]float64, n), make([]float64, n), make([]float64, n), make([]float64, n)
	for i := range n {
		alpha := float64(rgba[i*4+3]) / 255
		r := avgR*(1-alpha) + alpha/255*float64(rgba[i*4])
		g := avgG*(1-alpha) + alpha/255*float64(rgba[i*4+1])
		bl := avgB*(1-alpha) + alpha/255*float64(rgba[i*4+2])
		l[i] = (r + g + bl) / 3
		p[i] = (r+g)/2 - bl
		q[i] = r - g
		a[i] = alpha
	}
	encode := func(channel []float64, nx, ny int) (float64, []float64, float64) {
		var dc, scale float64
		var ac []float64
		fx := make([]float64, w)
		for cy := range ny {
			for cx := 0; cx*ny < nx*(ny-cy); cx++ {
				for x := range w {
					fx[x] = math.Cos(math.Pi / float64(w) * float64(cx) * (float64(x) + 0.5))
				}
				f := 0.0
				for y := range h {
					fy := math.Cos(math.Pi / float64(h) * float64(cy) * (float64(y) + 0.5))
					for x := range w {
						f += channel[x+y*w] * fx[x] * fy
					}
				}
				f /= float64(n)
				if cx > 0 || cy > 0 {
					ac = append(ac, f)
					scale = math.Max(scale, math.Abs(f))
				} else {
					dc = f
				}
			}
		}
		if scale > 0 {
			for i := range ac {
				ac[i] = 0.5 + 0.5/scale*ac[i]
			}
		}
		return dc, ac, scale
	}
	lDC, lAC, lScale := encode(l, max(3, lx), max(3, ly))
	pDC, pAC, pScale := encode(p, 3, 3)
	qDC, qAC, qScale := encode(q, 3, 3)
	var aDC, aScale float64
	var aAC []float64
	if hasAlpha {
		aDC, aAC, aScale = encode(a, 5, 5)
	}
	landscape := w > h
	round := func(v float64) uint32 { return uint32(math.Max(0, math.Round(v))) }
	header24 := round(63*lDC) | round(31.5+31.5*pDC)<<6 | round(31.5+31.5*qDC)<<12 | round(31*lScale)<<18
	if hasAlpha {
		header24 |= 1 << 23
	}
	side := ly
	if !landscape {
		side = lx
	}
	header16 := uint32(side) | round(63*pScale)<<3 | round(63*qScale)<<9 //nolint:gosec
	if landscape {
		header16 |= 1 << 15
	}
	hash := []byte{byte(header24), byte(header24 >> 8), byte(header24 >> 16), byte(header16), byte(header16 >> 8)}
	acStart := 5
	if hasAlpha {
		acStart = 6
		hash = append(hash, byte(round(15*aDC)|round(15*aScale)<<4))
	}
	channels := [][]float64{lAC, pAC, qAC}
	if hasAlpha {
		channels = append(channels, aAC)
	}
	index := 0
	for _, ac := range channels {
		for _, f := range ac {
			pos := acStart + index>>1
			for len(hash) <= pos {
				hash = append(hash, 0)
			}
			hash[pos] |= byte(round(15*f) << ((index & 1) * 4))
			index++
		}
	}
	return hash
}
