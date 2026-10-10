package version

import (
	"os"
	"regexp"
	"testing"
)

func TestAPIMatchesTheContract(t *testing.T) {
	raw, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^  version: (\S+)\r?$`).FindSubmatch(raw)
	if m == nil {
		t.Fatal("no info.version in openapi.yaml")
	}
	if string(m[1]) != API {
		t.Fatalf("version.API is %s, api/openapi.yaml says %s", API, m[1])
	}
}

func TestPrecedence(t *testing.T) {
	ordered := []string{"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.0.1", "1.1.0", "2.0.0"}
	for i := range ordered {
		for j := range ordered {
			got := MustParse(ordered[i]).Compare(MustParse(ordered[j]))
			want := sign(i - j)
			if got != want {
				t.Errorf("%s vs %s: %d, want %d", ordered[i], ordered[j], got, want)
			}
		}
	}
	if MustParse("1.0.0+build.5").Compare(MustParse("1.0.0")) != 0 {
		t.Error("build metadata counts")
	}
	for _, bad := range []string{"", "1", "1.0", "v1.0.0", "1.0.0-", "01.0.0x"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("parsed %q", bad)
		}
	}
}

func TestSupported(t *testing.T) {
	min := MustParse("2.0.0-alpha.1")
	cases := map[string]bool{"2.0.0-alpha.1": true, "2.0.0-alpha.2": true, "2.1.0": true, "2.0.0-alpha.0": false, "1.9.0": false, "3.0.0": false}
	for v, want := range cases {
		if got := Supported(MustParse(v), min); got != want {
			t.Errorf("%s: %v", v, got)
		}
	}
}
