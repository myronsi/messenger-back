package drawing_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/myronsi/messenger-back/internal/drawing"
)

func TestNormalizeRoundsAndLowercases(t *testing.T) {
	in := `{"version":1,"items":[
		{"type":"stroke","color":"#FF00aa","width":0.0123456,"points":[0.123456,0.5,1,0]},
		{"type":"text","color":"#000000","x":0.25,"y":0.333333,"size":0.05,"text":"Look here"}
	]}`
	out, err := drawing.Normalize([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	var d drawing.Drawing
	if err := json.Unmarshal(out, &d); err != nil {
		t.Fatal(err)
	}
	stroke, text := d.Items[0], d.Items[1]
	if stroke.Color != "#ff00aa" || *stroke.Width != 0.0123 || stroke.Points[0] != 0.1235 {
		t.Errorf("stroke not normalized: %+v", stroke)
	}
	if *text.Y != 0.3333 || *text.Text != "Look here" {
		t.Errorf("text not normalized: %+v", text)
	}
}

func TestNormalizeRejectsInvalidDrawings(t *testing.T) {
	long := `"` + strings.Repeat("a", drawing.MaxTextRunes+1) + `"`
	many := make([]string, drawing.MaxItems+1)
	for i := range many {
		many[i] = `{"type":"stroke","color":"#000000","width":0.01,"points":[0,0]}`
	}
	points := make([]string, drawing.MaxCoordinates+2)
	for i := range points {
		points[i] = "0.5"
	}
	cases := map[string]string{
		"not json":         `{`,
		"wrong version":    `{"version":2,"items":[{"type":"stroke","color":"#000000","width":0.01,"points":[0,0]}]}`,
		"no items":         `{"version":1,"items":[]}`,
		"too many items":   `{"version":1,"items":[` + strings.Join(many, ",") + `]}`,
		"unknown field":    `{"version":1,"items":[{"type":"stroke","color":"#000000","width":0.01,"points":[0,0],"blur":1}]}`,
		"unknown type":     `{"version":1,"items":[{"type":"circle","color":"#000000"}]}`,
		"bad color":        `{"version":1,"items":[{"type":"stroke","color":"red","width":0.01,"points":[0,0]}]}`,
		"odd points":       `{"version":1,"items":[{"type":"stroke","color":"#000000","width":0.01,"points":[0,0,1]}]}`,
		"point outside":    `{"version":1,"items":[{"type":"stroke","color":"#000000","width":0.01,"points":[0,1.5]}]}`,
		"no width":         `{"version":1,"items":[{"type":"stroke","color":"#000000","points":[0,0]}]}`,
		"too wide":         `{"version":1,"items":[{"type":"stroke","color":"#000000","width":0.5,"points":[0,0]}]}`,
		"stroke with text": `{"version":1,"items":[{"type":"stroke","color":"#000000","width":0.01,"points":[0,0],"text":"x"}]}`,
		"text with points": `{"version":1,"items":[{"type":"text","color":"#000000","x":0,"y":0,"size":0.05,"text":"x","points":[0,0]}]}`,
		"blank text":       `{"version":1,"items":[{"type":"text","color":"#000000","x":0,"y":0,"size":0.05,"text":"  "}]}`,
		"long text":        `{"version":1,"items":[{"type":"text","color":"#000000","x":0,"y":0,"size":0.05,"text":` + long + `}]}`,
		"control in text":  `{"version":1,"items":[{"type":"text","color":"#000000","x":0,"y":0,"size":0.05,"text":"a\u0007"}]}`,
		"tiny text":        `{"version":1,"items":[{"type":"text","color":"#000000","x":0,"y":0,"size":0.001,"text":"x"}]}`,
		"too many points":  `{"version":1,"items":[{"type":"stroke","color":"#000000","width":0.01,"points":[` + strings.Join(points, ",") + `]}]}`,
		"trailing data":    `{"version":1,"items":[{"type":"stroke","color":"#000000","width":0.01,"points":[0,0]}]} {}`,
		"trailing brace":   `{"version":1,"items":[{"type":"stroke","color":"#000000","width":0.01,"points":[0,0]}]}}`,
	}
	for name, in := range cases {
		if _, err := drawing.Normalize([]byte(in)); !errors.Is(err, drawing.ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
