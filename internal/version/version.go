// Package version holds the API contract version the server implements and compares client versions
// with it (Semantic Versioning precedence).
package version

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Backend is the version of this build, set at build time
// (-ldflags "-X github.com/myronsi/messenger-back/internal/version.Backend=1.2.3").
var Backend = "dev"

// API is the version of the contract in api/openapi.yaml (info.version). A test keeps the two equal.
const API = "2.0.0-alpha.6"

// SemVer is a parsed version; build metadata is dropped.
type SemVer struct {
	Major, Minor, Patch int
	Pre                 []string
}

var semverRE = regexp.MustCompile(`^(\d{1,6})\.(\d{1,6})\.(\d{1,6})(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)

// Parse reads a version such as 2.0.0 or 2.0.0-alpha.2.
func Parse(s string) (SemVer, error) {
	s = strings.TrimSpace(s)
	m := semverRE.FindStringSubmatch(s)
	if len(s) > 64 || m == nil {
		return SemVer{}, fmt.Errorf("version %q is not a semantic version", s)
	}
	v := SemVer{}
	v.Major, _ = strconv.Atoi(m[1])
	v.Minor, _ = strconv.Atoi(m[2])
	v.Patch, _ = strconv.Atoi(m[3])
	if m[4] != "" {
		v.Pre = strings.Split(m[4], ".")
	}
	return v, nil
}

// MustParse is Parse for constants.
func MustParse(s string) SemVer {
	v, err := Parse(s)
	if err != nil {
		panic(err)
	}
	return v
}

// Compare orders by SemVer precedence: -1, 0 or 1. A pre-release sorts below its release.
func (v SemVer) Compare(o SemVer) int {
	for _, d := range [][2]int{{v.Major, o.Major}, {v.Minor, o.Minor}, {v.Patch, o.Patch}} {
		if d[0] != d[1] {
			return sign(d[0] - d[1])
		}
	}
	switch {
	case len(v.Pre) == 0 && len(o.Pre) == 0:
		return 0
	case len(v.Pre) == 0:
		return 1
	case len(o.Pre) == 0:
		return -1
	}
	for i := 0; i < len(v.Pre) && i < len(o.Pre); i++ {
		a, b := v.Pre[i], o.Pre[i]
		an, aerr := strconv.Atoi(a)
		bn, berr := strconv.Atoi(b)
		switch {
		case aerr == nil && berr == nil:
			if an != bn {
				return sign(an - bn)
			}
		case aerr == nil:
			return -1 // numeric identifiers sort below alphanumeric ones
		case berr == nil:
			return 1
		default:
			if c := strings.Compare(a, b); c != 0 {
				return c
			}
		}
	}
	return sign(len(v.Pre) - len(o.Pre))
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}

// Supported reports whether a client built against contract version client may talk to a server that
// implements API and serves clients down to minimum: same major version, and not below minimum.
func Supported(client, minimum SemVer) bool {
	return client.Major == MustParse(API).Major && client.Compare(minimum) >= 0
}
