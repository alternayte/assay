package app

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
	"go.opentelemetry.io/otel/trace"
)

// metrics holds the process metrics. Each server has its own registry, so
// several servers can run in one test process.
type metrics struct {
	registry *prometheus.Registry
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
}

func newMetrics(pool *pgxpool.Pool, extra ...prometheus.Collector) *metrics {
	m := &metrics{
		registry: prometheus.NewRegistry(),
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "assay_http_requests_total", Help: "HTTP requests by method, route and status.",
		}, []string{"method", "route", "status"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "assay_http_request_duration_seconds", Help: "HTTP request duration by method and route.",
			Buckets: prometheus.DefBuckets,
		}, []string{"method", "route"}),
	}
	m.registry.MustRegister(m.requests, m.duration,
		collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		&riverCollector{pool: pool}, &usageCollector{pool: pool})
	m.registry.MustRegister(extra...)
	return m
}

func (m *metrics) handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// middleware counts requests by route pattern, so that IDs in paths do not
// create series.
func (m *metrics) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		route := "unmatched"
		if rc := chi.RouteContext(r.Context()); rc != nil && rc.RoutePattern() != "" {
			route = rc.RoutePattern()
		}
		m.requests.WithLabelValues(r.Method, route, strconv.Itoa(sw.status)).Inc()
		m.duration.WithLabelValues(r.Method, route).Observe(time.Since(start).Seconds())
	})
}

// riverCollector reports the River jobs by kind and state at each scrape.
type riverCollector struct {
	pool *pgxpool.Pool
}

var riverJobs = prometheus.NewDesc("assay_river_jobs", "River jobs by kind and state.", []string{"kind", "state"}, nil)

func (c *riverCollector) Describe(ch chan<- *prometheus.Desc) { ch <- riverJobs }

func (c *riverCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	rows, err := c.pool.Query(ctx, "SELECT kind, state::text, count(*) FROM river_job GROUP BY kind, state")
	if err != nil {
		ch <- prometheus.NewInvalidMetric(riverJobs, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var kind, state string
		var n int64
		if err := rows.Scan(&kind, &state, &n); err != nil {
			ch <- prometheus.NewInvalidMetric(riverJobs, err)
			return
		}
		ch <- prometheus.MustNewConstMetric(riverJobs, prometheus.GaugeValue, float64(n), kind, state)
	}
}

// usageCollector reports the decisions by kind, band and outcome, and the
// model usage of the month by function, at each scrape (REQ-OPS-001).
type usageCollector struct {
	pool *pgxpool.Pool
}

var (
	decisionCount = prometheus.NewDesc("assay_decisions", "Stored decisions by kind, band and outcome. The band is none when decide was off.",
		[]string{"kind", "band", "outcome"}, nil)
	usageTokens = prometheus.NewDesc("assay_model_usage_tokens", "Model tokens of the current month by function and direction.",
		[]string{"function", "direction"}, nil)
	usageCost = prometheus.NewDesc("assay_model_usage_cost_usd", "Model cost of the current month in USD by function.", []string{"function"}, nil)
)

func (c *usageCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- decisionCount
	ch <- usageTokens
	ch <- usageCost
}

func (c *usageCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	rows, err := c.pool.Query(ctx, "SELECT kind, coalesce(band, 'none'), outcome, count(*) FROM decisions GROUP BY 1, 2, 3")
	if err != nil {
		ch <- prometheus.NewInvalidMetric(decisionCount, err)
		return
	}
	for rows.Next() {
		var kind, band, outcome string
		var n int64
		if err := rows.Scan(&kind, &band, &outcome, &n); err != nil {
			rows.Close()
			ch <- prometheus.NewInvalidMetric(decisionCount, err)
			return
		}
		ch <- prometheus.MustNewConstMetric(decisionCount, prometheus.GaugeValue, float64(n), kind, band, outcome)
	}
	rows.Close()
	rows, err = c.pool.Query(ctx, `SELECT function, sum(tokens_in)::float8, sum(tokens_out)::float8, sum(cost_usd)::float8
FROM model_usage WHERE at >= date_trunc('month', now()) GROUP BY function`)
	if err != nil {
		ch <- prometheus.NewInvalidMetric(usageCost, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var fn string
		var in, out, cost float64
		if err := rows.Scan(&fn, &in, &out, &cost); err != nil {
			ch <- prometheus.NewInvalidMetric(usageCost, err)
			return
		}
		ch <- prometheus.MustNewConstMetric(usageTokens, prometheus.GaugeValue, in, fn, "in")
		ch <- prometheus.MustNewConstMetric(usageTokens, prometheus.GaugeValue, out, fn, "out")
		ch <- prometheus.MustNewConstMetric(usageCost, prometheus.GaugeValue, cost, fn)
	}
}

// tracing returns a tracer provider that exports over OTLP HTTP when
// OTEL_EXPORTER_OTLP_ENDPOINT or OTEL_EXPORTER_OTLP_TRACES_ENDPOINT is set.
// It returns nil when tracing is off.
func tracing(ctx context.Context) (*sdktrace.TracerProvider, error) {
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" && os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") == "" {
		return nil, nil
	}
	exp, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, err
	}
	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(semconv.ServiceName("assay"), semconv.ServiceVersion(Version)))
	if err != nil {
		return nil, err
	}
	return sdktrace.NewTracerProvider(sdktrace.WithBatcher(exp), sdktrace.WithResource(res)), nil
}

// traced wraps h with request spans when tp is not nil.
func traced(h http.Handler, tp trace.TracerProvider) http.Handler {
	if tp == nil {
		return h
	}
	return otelhttp.NewHandler(h, "http",
		otelhttp.WithTracerProvider(tp),
		otelhttp.WithPropagators(propagation.TraceContext{}),
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string { return "HTTP " + r.Method }))
}
