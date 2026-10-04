package observability

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics holds the Prometheus collectors of a process. It uses its own registry so tests and
// several binaries never collide on the global one.
type Metrics struct {
	Registry *prometheus.Registry

	requests       *prometheus.CounterVec
	duration       *prometheus.HistogramVec
	inFlight       prometheus.Gauge
	webSockets     prometheus.Gauge
	messages       prometheus.Counter
	storeErrors    *prometheus.CounterVec
	storeCheckTime *prometheus.HistogramVec
}

// NewMetrics registers the collectors together with the Go runtime and process collectors.
func NewMetrics() *Metrics {
	m := &Metrics{
		Registry: prometheus.NewRegistry(),
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "messenger_http_requests_total",
			Help: "HTTP requests by method, route pattern and status code.",
		}, []string{"method", "route", "status"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "messenger_http_request_duration_seconds",
			Help:    "HTTP request latency by method and route pattern.",
			Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30},
		}, []string{"method", "route"}),
		inFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "messenger_http_requests_in_flight",
			Help: "HTTP requests currently being served.",
		}),
		webSockets: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "messenger_websocket_connections_open",
			Help: "Open WebSocket connections.",
		}),
		messages: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "messenger_messages_total",
			Help: "Messages accepted for delivery; use rate() for messages per second.",
		}),
		storeErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "messenger_store_errors_total",
			Help: "Failed operations per data store.",
		}, []string{"store"}),
		storeCheckTime: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "messenger_store_check_duration_seconds",
			Help:    "Duration of the readiness check per data store.",
			Buckets: []float64{.001, .005, .01, .05, .1, .25, .5, 1, 2},
		}, []string{"store"}),
	}
	m.Registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		m.requests, m.duration, m.inFlight, m.webSockets, m.messages, m.storeErrors, m.storeCheckTime,
	)
	return m
}

// Handler serves the metrics in the Prometheus text format.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{Registry: m.Registry})
}

// ObserveRequest records one finished HTTP request.
func (m *Metrics) ObserveRequest(method, route string, status int, d time.Duration) {
	m.requests.WithLabelValues(method, route, statusLabel(status)).Inc()
	m.duration.WithLabelValues(method, route).Observe(d.Seconds())
}

// RequestStarted and RequestFinished maintain the in-flight gauge.
func (m *Metrics) RequestStarted() { m.inFlight.Inc() }

// RequestFinished decrements the in-flight gauge.
func (m *Metrics) RequestFinished() { m.inFlight.Dec() }

// WebSocketOpened and WebSocketClosed maintain the open connections gauge.
func (m *Metrics) WebSocketOpened() { m.webSockets.Inc() }

// WebSocketClosed decrements the open connections gauge.
func (m *Metrics) WebSocketClosed() { m.webSockets.Dec() }

// MessageAccepted counts a message accepted for delivery.
func (m *Metrics) MessageAccepted() { m.messages.Inc() }

// StoreError counts a failed operation of the named store ("postgres", "redis", "scylla",
// "elasticsearch").
func (m *Metrics) StoreError(store string) { m.storeErrors.WithLabelValues(store).Inc() }

// ObserveStoreCheck records the duration of a readiness check.
func (m *Metrics) ObserveStoreCheck(store string, d time.Duration) {
	m.storeCheckTime.WithLabelValues(store).Observe(d.Seconds())
}

// statusLabel keeps the label values to the three-digit codes: 200, 404, ...
func statusLabel(status int) string {
	if status < 100 || status > 599 {
		return "unknown"
	}
	return strconv.Itoa(status)
}
