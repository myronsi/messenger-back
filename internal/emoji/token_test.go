package emoji

import (
	"testing"
)

func TestParseAndFormat(t *testing.T) {
	text := "hi <:party_cat:42> and <a:wave:7>, not <:Bad:1> or <:x:2> or <:ok:0>"
	tokens := Parse(text)
	if len(tokens) != 2 {
		t.Fatalf("tokens %+v", tokens)
	}
	if tokens[0].Shortcode != "party_cat" || tokens[0].ID != 42 || tokens[0].Animated || text[tokens[0].Start:tokens[0].End] != "<:party_cat:42>" {
		t.Fatalf("first %+v", tokens[0])
	}
	if !tokens[1].Animated || tokens[1].ID != 7 || tokens[1].Shortcode != "wave" {
		t.Fatalf("second %+v", tokens[1])
	}
	if Format("party_cat", 42, false) != "<:party_cat:42>" || Format("wave", 7, true) != "<a:wave:7>" {
		t.Fatal("format")
	}
	if got := IDs("<:a1:5><:b2:5><:c3:6>"); len(got) != 2 || got[0] != 5 || got[1] != 6 {
		t.Fatalf("ids %v", got)
	}
}

func TestPlainAndRewrite(t *testing.T) {
	if got := Plain("yay <:party:9>!<a:wave:3>"); got != "yay :party:!:wave:" {
		t.Fatalf("plain %q", got)
	}
	if got := Plain("no tokens here"); got != "no tokens here" {
		t.Fatalf("untouched %q", got)
	}
	got := Rewrite("<:old:1> <:gone:2>", func(tok Token) string {
		if tok.ID == 1 {
			return Format("new", 1, true)
		}
		return Fallback(tok.Shortcode)
	})
	if got != "<a:new:1> :gone:" {
		t.Fatalf("rewrite %q", got)
	}
	if PlainPtr(nil) != nil {
		t.Fatal("nil stays nil")
	}
}

func TestReactionID(t *testing.T) {
	for in, want := range map[string]int64{"c:12": 12, "c:9223372036854775807": 9223372036854775807} {
		if id, ok := ReactionID(in); !ok || id != want {
			t.Errorf("%s: %d %v", in, id, ok)
		}
	}
	for _, bad := range []string{"c:", "c:0", "c:01", "c:-1", "c:abc", "👍", "c:99999999999999999999"} {
		if _, ok := ReactionID(bad); ok {
			t.Errorf("%s accepted", bad)
		}
	}
	if Reaction(5) != "c:5" || len(Reaction(9223372036854775807)) > 32 {
		t.Fatal("reaction format")
	}
}
