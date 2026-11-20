package mentions

import "strings"

const MaxPerMessage = 20

const All = "all"

func nameByte(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func Parse(text string) (names []string, all bool) {
	seen := map[string]bool{}
	for i := 0; i < len(text); i++ {
		if text[i] != '@' {
			continue
		}
		if i > 0 && (nameByte(text[i-1]) || text[i-1] == '@') {
			continue
		}
		j := i + 1
		for j < len(text) && nameByte(text[j]) {
			j++
		}
		n := j - i - 1
		if n < 3 || n > 32 {
			i = j - 1
			continue
		}
		name := strings.ToLower(text[i+1 : j])
		i = j - 1
		if name == All {
			all = true
			continue
		}
		if seen[name] || len(names) >= MaxPerMessage {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	return names, all
}
