package media

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestClipRange(t *testing.T) {
	ms := func(v int) *int { return &v }
	cases := []struct {
		name       string
		duration   time.Duration
		start, end *int
		wantStart  time.Duration
		wantLength time.Duration
		wantErr    bool
	}{
		{"first 30 s of a long song", 3 * time.Minute, nil, nil, 0, 30 * time.Second, false},
		{"short file stays whole", 12 * time.Second, nil, nil, 0, 12 * time.Second, false},
		{"chosen part", 3 * time.Minute, ms(60000), ms(80000), time.Minute, 20 * time.Second, false},
		{"end past the file is cut", 70 * time.Second, ms(60000), ms(90000), time.Minute, 10 * time.Second, false},
		{"start only", 3 * time.Minute, ms(10000), nil, 10 * time.Second, 30 * time.Second, false},
		{"too long", 3 * time.Minute, ms(0), ms(45000), 0, 0, true},
		{"end before start", 3 * time.Minute, ms(5000), ms(4000), 0, 0, true},
		{"start after the end of the file", 20 * time.Second, ms(25000), nil, 0, 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			start, length, err := clipRange(c.duration, c.start, c.end)
			if (err != nil) != c.wantErr {
				t.Fatalf("error %v", err)
			}
			if err != nil {
				if !errors.Is(err, ErrInvalid) {
					t.Fatalf("not ErrInvalid: %v", err)
				}
				return
			}
			if start != c.wantStart || length != c.wantLength {
				t.Fatalf("got %s + %s", start, length)
			}
		})
	}
}

func TestParseTags(t *testing.T) {
	long := strings.Repeat("a", 150)
	tags := parseTags("TAG:title=  Song\x07 Name \nTAG:ARTIST=" + long + "\nTAG:title=Second\nduration=1\n")
	if tags.Title == nil || *tags.Title != "Song Name" {
		t.Fatalf("title %v", tags.Title)
	}
	if tags.Artist == nil || len(*tags.Artist) != maxTagLength {
		t.Fatalf("artist %v", tags.Artist)
	}
	if empty := parseTags("TAG:title=   \n"); empty.Title != nil || empty.Artist != nil {
		t.Fatalf("blank tags %+v", empty)
	}
}

func TestDetectProfile(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	if d, err := detectProfile(png, PurposeProfileBackground); err != nil || d.Kind != KindImage {
		t.Fatalf("png background: %+v %v", d, err)
	}
	if _, err := detectProfile(png, PurposeProfileMusic); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("an image is no music: %v", err)
	}
	if _, err := detectProfile([]byte("%PDF-1.7 hello"), PurposeProfileBackground); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("a pdf is no background: %v", err)
	}
	if d, err := detectProfile([]byte("OggS\x00\x02"), PurposeProfileMusic); err != nil || d.ContentType != "audio/ogg" {
		t.Fatalf("ogg music: %+v %v", d, err)
	}
}
