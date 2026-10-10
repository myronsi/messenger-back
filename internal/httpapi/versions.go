package httpapi

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/myronsi/messenger-back/internal/version"
)

// Version headers sent by clients (docs/versioning.md).
const (
	ClientVersionHeader    = "X-Client-Version"
	ClientAPIVersionHeader = "X-Client-Api-Version"
)

// maxTrackedVersions bounds the label values of the per-version request metric.
const maxTrackedVersions = 50

// VersionCounter counts requests per client contract version (the metric).
type VersionCounter interface {
	ClientAPIRequest(apiVersion string)
}

// clientVersions answers 426 client_outdated to clients whose X-Client-Api-Version has another major version
// or is below the minimum, and 400 invalid_client_version to a malformed one. Requests without the header are
// served. The operational endpoints, /meta (how clients learn the versions) and the WebSocket (which reports
// versions in hello) are never blocked.
func clientVersions(minimum version.SemVer, basePath string, counter VersionCounter) middleware {
	exempt := map[string]bool{"/healthz": true, "/readyz": true, "/metrics": true, basePath + "/meta": true, basePath + "/ws": true}
	var mu sync.Mutex
	seen := map[string]bool{}
	label := func(v string) string {
		mu.Lock()
		defer mu.Unlock()
		if seen[v] || len(seen) < maxTrackedVersions {
			seen[v] = true
			return v
		}
		return "other"
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw := strings.TrimSpace(r.Header.Get(ClientAPIVersionHeader))
			if raw == "" || exempt[r.URL.Path] {
				if counter != nil && !exempt[r.URL.Path] {
					counter.ClientAPIRequest("none")
				}
				next.ServeHTTP(w, r)
				return
			}
			v, err := version.Parse(raw)
			if err != nil {
				WriteProblem(w, http.StatusBadRequest, ErrorCodeInvalidClientVersion)
				return
			}
			if counter != nil {
				counter.ClientAPIRequest(label(versionLabel(v)))
			}
			if !version.Supported(v, minimum) {
				WriteProblem(w, http.StatusUpgradeRequired, ErrorCodeClientOutdated)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// releasePre matches the pre-release tags the project publishes (alpha.N, beta.N, rc.N).
var releasePre = regexp.MustCompile(`^(alpha|beta|rc)\.[0-9]{1,3}$`)

// versionLabel is the metric label of a client version: build metadata dropped, and pre-releases that no
// release uses folded into "other", so made-up values cannot use up the label slots.
func versionLabel(v version.SemVer) string {
	s := strconv.Itoa(v.Major) + "." + strconv.Itoa(v.Minor) + "." + strconv.Itoa(v.Patch)
	if len(v.Pre) == 0 {
		return s
	}
	pre := strings.Join(v.Pre, ".")
	if !releasePre.MatchString(pre) {
		return "other"
	}
	return s + "-" + pre
}

// MetaInfo is what GET /meta reports.
type MetaInfo struct {
	BackendVersion      string
	Commit              string
	MinClientAPIVersion string
}

func (m MetaInfo) serve(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "public, max-age=60")
	writeJSONStatus(w, http.StatusOK, Meta{
		BackendVersion: m.BackendVersion, Commit: m.Commit, ApiVersion: version.API, MinClientApiVersion: m.MinClientAPIVersion,
	})
}
