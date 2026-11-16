package emoji

import (
	"regexp"
	"strconv"
	"strings"
)

const (
	ReactionPrefix = "c:"
	maxTokens      = 200
)

var (
	tokenPattern     = regexp.MustCompile(`<(a?):([a-z0-9_]{2,32}):([1-9][0-9]{0,18})>`)
	ShortcodePattern = regexp.MustCompile(`^[a-z0-9_]{2,32}$`)
)

type Token struct {
	Start, End int
	Animated   bool
	Shortcode  string
	ID         int64
}

func Parse(text string) []Token {
	matches := tokenPattern.FindAllStringSubmatchIndex(text, maxTokens)
	out := make([]Token, 0, len(matches))
	for _, m := range matches {
		id, err := strconv.ParseInt(text[m[6]:m[7]], 10, 64)
		if err != nil {
			continue
		}
		out = append(out, Token{Start: m[0], End: m[1], Animated: m[3] > m[2], Shortcode: text[m[4]:m[5]], ID: id})
	}
	return out
}

func IDs(text string) []int64 {
	seen := map[int64]bool{}
	var out []int64
	for _, t := range Parse(text) {
		if !seen[t.ID] {
			seen[t.ID] = true
			out = append(out, t.ID)
		}
	}
	return out
}

func Format(shortcode string, id int64, animated bool) string {
	prefix := "<:"
	if animated {
		prefix = "<a:"
	}
	return prefix + shortcode + ":" + strconv.FormatInt(id, 10) + ">"
}

func Fallback(shortcode string) string { return ":" + shortcode + ":" }

func Rewrite(text string, replace func(Token) string) string {
	tokens := Parse(text)
	if len(tokens) == 0 {
		return text
	}
	var b strings.Builder
	last := 0
	for _, t := range tokens {
		b.WriteString(text[last:t.Start])
		b.WriteString(replace(t))
		last = t.End
	}
	b.WriteString(text[last:])
	return b.String()
}

func Plain(text string) string {
	return Rewrite(text, func(t Token) string { return Fallback(t.Shortcode) })
}

func PlainPtr(text *string) *string {
	if text == nil {
		return nil
	}
	p := Plain(*text)
	return &p
}

func ReactionID(reaction string) (int64, bool) {
	rest, ok := strings.CutPrefix(reaction, ReactionPrefix)
	if !ok || rest == "" || rest[0] == '0' || len(rest) > 19 {
		return 0, false
	}
	id, err := strconv.ParseInt(rest, 10, 64)
	return id, err == nil && id > 0
}

func Reaction(id int64) string { return ReactionPrefix + strconv.FormatInt(id, 10) }
