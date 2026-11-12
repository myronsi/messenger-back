package linkpreview

import "strings"

const (
	urlLeading  = "([{<'\""
	urlTrailing = ".,;:!?)]}>'\""
)

func FindURLs(text string, limit int) []string {
	var out []string
	seen := map[string]bool{}
	for _, field := range strings.Fields(text) {
		token := strings.TrimLeft(field, urlLeading)
		lower := strings.ToLower(token)
		if strings.HasPrefix(lower, "www.") {
			token, lower = "https://"+token, "https://"+lower
		}
		if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
			continue
		}
		u, err := Normalize(strings.TrimRight(token, urlTrailing))
		if err != nil {
			continue
		}
		s := u.String()
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}
