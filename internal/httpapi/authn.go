package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"

	"github.com/myronsi/messenger-back/internal/auth"
)

// Authenticator verifies the access token of a request. *auth.Service implements it.
type Authenticator interface {
	Authenticate(ctx context.Context, accessToken string) (auth.Principal, error)
}

type principalKey struct{}

// PrincipalFrom returns the authenticated caller of a request. ok is false for public routes and for
// requests without a valid access token on routes where authentication is optional.
func PrincipalFrom(ctx context.Context) (p auth.Principal, ok bool) {
	p, ok = ctx.Value(principalKey{}).(auth.Principal)
	return p, ok
}

type access int

const (
	// accessRequired is the default: a route that is not listed needs a valid access token, so a new
	// operation of the contract is protected until someone decides otherwise.
	accessRequired access = iota
	accessPublic
	// accessOptional routes work without a bearer token; they use it when it is valid.
	accessOptional
)

// routeAccess lists the operations of the contract that do not require an access token
// (`security: []` or cookie-only in api/openapi.yaml), keyed by "METHOD path" below the base path.
var routeAccess = map[string]access{
	"GET /meta":                 accessPublic,
	"POST /auth/register":       accessPublic,
	"POST /auth/login":          accessPublic,
	"POST /auth/login/2fa":      accessPublic,
	"POST /auth/refresh":        accessPublic,
	"POST /auth/recover":        accessPublic,
	"POST /auth/reset-password": accessPublic,
	"POST /auth/logout":         accessOptional,
}

func accessFor(pattern, basePath string) access {
	method, path, ok := strings.Cut(pattern, " ")
	if !ok {
		return accessRequired
	}
	rest, ok := strings.CutPrefix(path, basePath)
	if !ok {
		return accessRequired
	}
	if a, listed := routeAccess[method+" "+rest]; listed {
		return a
	}
	return accessRequired
}

// authenticate returns the per-operation middleware that checks the bearer access token. It runs after
// the route is matched, so the decision follows the route pattern and never the raw path.
func authenticate(a Authenticator, basePath string, log *slog.Logger) MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mode := accessFor(r.Pattern, basePath)
			if mode == accessPublic {
				next.ServeHTTP(w, r)
				return
			}
			token, ok := bearerToken(r)
			if !ok || a == nil {
				if mode == accessOptional {
					next.ServeHTTP(w, r)
					return
				}
				writeUnauthorized(w, ErrorCodeUnauthenticated)
				return
			}
			p, err := a.Authenticate(r.Context(), token)
			switch {
			case err == nil:
				next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
			case mode == accessOptional && (errors.Is(err, auth.ErrInvalidToken) || errors.Is(err, auth.ErrTokenExpired) || errors.Is(err, auth.ErrUnauthenticated)):
				next.ServeHTTP(w, r)
			case errors.Is(err, auth.ErrTokenExpired):
				writeUnauthorized(w, ErrorCodeTokenExpired)
			case errors.Is(err, auth.ErrInvalidToken), errors.Is(err, auth.ErrUnauthenticated):
				writeUnauthorized(w, ErrorCodeUnauthenticated)
			default:
				log.ErrorContext(r.Context(), "authenticate", "err", err)
				WriteProblem(w, http.StatusInternalServerError, ErrorCodeInternalError)
			}
		})
	}
}

func writeUnauthorized(w http.ResponseWriter, code ErrorCode) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	WriteProblem(w, http.StatusUnauthorized, code)
}

func bearerToken(r *http.Request) (string, bool) {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	token = strings.TrimSpace(token)
	if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" {
		return "", false
	}
	return token, true
}

// clientIP is the address of the caller. Forwarding headers are believed only when the TCP peer is one
// of the configured trusted proxies; the X-Forwarded-For list is then read from the right, skipping
// trusted hops, so entries a client made up on the left are ignored. Callers connecting from anywhere
// else can not choose their address, and with no trusted proxies the peer address is always used.
func clientIP(r *http.Request, trusted []netip.Prefix) netip.Addr {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}
	}
	peer := ap.Addr().Unmap()
	if !isTrusted(peer, trusted) {
		return peer
	}
	hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		ip, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break
		}
		if ip = ip.Unmap(); !isTrusted(ip, trusted) {
			return ip
		}
	}
	if ip, err := netip.ParseAddr(strings.TrimSpace(r.Header.Get("X-Real-IP"))); err == nil && !isTrusted(ip.Unmap(), trusted) {
		return ip.Unmap()
	}
	return peer
}

func isTrusted(ip netip.Addr, trusted []netip.Prefix) bool {
	for _, p := range trusted {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

func (a *AuthServer) clientOf(r *http.Request) auth.Client {
	return auth.Client{IP: clientIP(r, a.trustedProxies), UserAgent: r.UserAgent()}
}
