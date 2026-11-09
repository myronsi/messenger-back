package drawing

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	Version        = 1
	MaxItems       = 200
	MaxCoordinates = 10000
	MaxTextRunes   = 200
	MaxBytes       = 96 << 10

	TypeStroke = "stroke"
	TypeText   = "text"

	minWidth, maxWidth = 0.001, 0.2
	minSize, maxSize   = 0.01, 0.5
	precision          = 1e4
)

var ErrInvalid = errors.New("invalid drawing")

var colorPattern = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

type Item struct {
	Type   string    `json:"type"`
	Color  string    `json:"color"`
	Width  *float64  `json:"width,omitempty"`
	Points []float64 `json:"points,omitempty"`
	X      *float64  `json:"x,omitempty"`
	Y      *float64  `json:"y,omitempty"`
	Size   *float64  `json:"size,omitempty"`
	Text   *string   `json:"text,omitempty"`
}

type Drawing struct {
	Version int    `json:"version"`
	Items   []Item `json:"items"`
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

func Normalize(raw []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var d Drawing
	if err := dec.Decode(&d); err != nil {
		return nil, invalid("not a drawing")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, invalid("not a drawing")
	}
	if d.Version != Version {
		return nil, invalid("version must be %d", Version)
	}
	if len(d.Items) == 0 || len(d.Items) > MaxItems {
		return nil, invalid("a drawing has 1 to %d items", MaxItems)
	}
	coordinates := 0
	for i := range d.Items {
		it := &d.Items[i]
		if !colorPattern.MatchString(it.Color) {
			return nil, invalid("item %d: color must be #rrggbb", i)
		}
		it.Color = strings.ToLower(it.Color)
		var err error
		switch it.Type {
		case TypeStroke:
			coordinates += len(it.Points)
			err = normalizeStroke(it)
		case TypeText:
			err = normalizeText(it)
		default:
			err = invalid("type must be stroke or text")
		}
		if err != nil {
			return nil, fmt.Errorf("item %d: %w", i, err)
		}
	}
	if coordinates > MaxCoordinates {
		return nil, invalid("a drawing has at most %d coordinates", MaxCoordinates)
	}
	out, err := json.Marshal(d)
	if err != nil {
		return nil, invalid("not a drawing")
	}
	if len(out) > MaxBytes {
		return nil, invalid("a drawing is at most %d bytes", MaxBytes)
	}
	return out, nil
}

func normalizeStroke(it *Item) error {
	if it.X != nil || it.Y != nil || it.Size != nil || it.Text != nil {
		return invalid("a stroke has only color, width and points")
	}
	if it.Width == nil || !within(*it.Width, minWidth, maxWidth) {
		return invalid("width must be between %g and %g", minWidth, maxWidth)
	}
	*it.Width = round(*it.Width)
	if len(it.Points) < 2 || len(it.Points)%2 != 0 {
		return invalid("points must be x, y pairs")
	}
	for j, v := range it.Points {
		if !within(v, 0, 1) {
			return invalid("points must be between 0 and 1")
		}
		it.Points[j] = round(v)
	}
	return nil
}

func normalizeText(it *Item) error {
	if it.Width != nil || it.Points != nil {
		return invalid("a text has only color, x, y, size and text")
	}
	if it.X == nil || it.Y == nil || !within(*it.X, 0, 1) || !within(*it.Y, 0, 1) {
		return invalid("x and y must be between 0 and 1")
	}
	if it.Size == nil || !within(*it.Size, minSize, maxSize) {
		return invalid("size must be between %g and %g", minSize, maxSize)
	}
	if it.Text == nil || !validText(*it.Text) {
		return invalid("text must be 1 to %d characters", MaxTextRunes)
	}
	*it.X, *it.Y, *it.Size = round(*it.X), round(*it.Y), round(*it.Size)
	return nil
}

func validText(s string) bool {
	if !utf8.ValidString(s) || strings.TrimSpace(s) == "" || utf8.RuneCountInString(s) > MaxTextRunes {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) && r != '\n' {
			return false
		}
	}
	return true
}

func within(v, lo, hi float64) bool {
	return !math.IsNaN(v) && v >= lo && v <= hi
}

func round(v float64) float64 {
	return math.Round(v*precision) / precision
}
