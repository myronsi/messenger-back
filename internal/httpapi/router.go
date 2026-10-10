package httpapi

import (
	"log/slog"
	"net/http"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/myronsi/messenger-back/internal/config"
	"github.com/myronsi/messenger-back/internal/observability"
)

// Options configures the router.
type Options struct {
	HTTP       config.HTTP
	Production bool
	Tracing    bool
	Log        *slog.Logger
	Metrics    *observability.Metrics
	// Checks are the dependencies probed by /readyz.
	Checks []Check
	// API implements the contract. Operations it does not implement answer 501 when it embeds
	// Unimplemented.
	API ServerInterface
	// Authenticator verifies access tokens. Every operation that the contract does not list as public is
	// answered 401 without one.
	Authenticator Authenticator
	// WebSocket serves <base path>/ws (the realtime gateway). It authenticates with a ticket, not a bearer
	// token, so it is mounted outside the contract's routes. Optional.
	WebSocket http.Handler
	// UploadMaxBytes is the body limit of POST /attachments (0: HTTP_MAX_BODY_BYTES like everything else).
	UploadMaxBytes int64
}

// Router is the HTTP handler of the API server.
type Router struct {
	handler http.Handler
	health  *health
}

// NewRouter wires the operational endpoints (/healthz, /readyz, /metrics) and the REST contract
// under the configured base path, wrapped in the security, logging and metrics middleware.
func NewRouter(o Options) *Router {
	h := &health{log: o.Log, metrics: o.Metrics, checks: o.Checks, timeout: o.HTTP.ReadinessTimeout}
	api := o.API
	if api == nil {
		api = Unimplemented{}
	}

	mux := routeMux{http.NewServeMux()}
	mux.HandleFunc("GET /healthz", h.healthz)
	mux.HandleFunc("GET /readyz", h.readyz)
	mux.Handle("GET /metrics", o.Metrics.Handler())
	if o.WebSocket != nil {
		mux.Handle("GET "+o.HTTP.BasePath+"/ws", o.WebSocket)
	}
	HandlerWithOptions(api, StdHTTPServerOptions{
		BaseURL:          o.HTTP.BasePath,
		BaseRouter:       mux,
		Middlewares:      []MiddlewareFunc{authenticate(o.Authenticator, o.HTTP.BasePath, o.Log)},
		ErrorHandlerFunc: requestErrorHandler,
	})

	var handler http.Handler = problemFallback{mux}
	handler = chain(handler,
		requestID(),
		observe(o.Log, o.Metrics),
		recoverPanic(o.Log),
		securityHeaders(o.Production),
		cors(o.HTTP.CORSOrigins),
		bodyLimit(o.HTTP.MaxBodyBytes, UploadPath(o.HTTP.BasePath), o.UploadMaxBytes),
		timeout(o.HTTP.RequestTimeout),
	)
	if o.Tracing {
		handler = otelhttp.NewHandler(handler, "http.server")
	}
	return &Router{handler: handler, health: h}
}

// ServeHTTP implements http.Handler.
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) { r.handler.ServeHTTP(w, req) }

// Drain makes /readyz answer 503 so load balancers stop sending new traffic before the server
// stops listening.
func (r *Router) Drain() { r.health.draining.Store(true) }
