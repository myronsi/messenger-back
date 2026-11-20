package mentions

import (
	"slices"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	cases := []struct {
		text  string
		names []string
		all   bool
	}{
		{"hi @Anna and @bob_1", []string{"anna", "bob_1"}, false},
		{"@anna @ANNA @anna.", []string{"anna"}, false},
		{"mail me at me@example.com", nil, false},
		{"@@anna", nil, false},
		{"@ab is too short", nil, false},
		{"@" + strings.Repeat("a", 33), nil, false},
		{"@" + strings.Repeat("a", 32) + "!", []string{strings.Repeat("a", 32)}, false},
		{"@all please read", nil, true},
		{"@All and @anna", []string{"anna"}, true},
		{"(@anna) said", []string{"anna"}, false},
		{"привет @anna", []string{"anna"}, false},
		{"@allison", []string{"allison"}, false},
	}
	for _, c := range cases {
		names, all := Parse(c.text)
		if !slices.Equal(names, c.names) || all != c.all {
			t.Errorf("Parse(%q) = %v, %v; want %v, %v", c.text, names, all, c.names, c.all)
		}
	}
}

func TestParseCapsNames(t *testing.T) {
	var b strings.Builder
	for i := range 30 {
		b.WriteString("@user")
		b.WriteString(strings.Repeat("x", i%5+1))
		b.WriteByte(byte('a' + i%26))
		b.WriteString(" ")
	}
	names, _ := Parse(b.String())
	if len(names) != MaxPerMessage {
		t.Fatalf("got %d names, want %d", len(names), MaxPerMessage)
	}
}
