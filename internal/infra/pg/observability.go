package pg

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/metrics"
	"github.com/vedantadhobley/found-footy/internal/observability/vocabulary"
)

// Observability bundles the pg adapter's metric handles + logger.
// One instance per binary, constructed at startup by the bootstrap
// layer and passed into pg.New(). The same instance can back multiple
// pools in the (rare) case a binary opens more than one — the
// queryTracer references the same counters and histograms.
type Observability struct {
	log logging.Emitter
	reg *metrics.Registry

	queries       *prometheus.CounterVec
	queryDuration *prometheus.HistogramVec
}

// RegisterMetrics builds the pg adapter's counters + histograms,
// registers them into reg, and returns the Observability instance to
// pass to pg.New. Call once per binary before opening any pool.
//
// Metrics:
//   - found_footy_pg_queries_total{op,outcome} — every query counted
//     by op (select/insert/update/delete/begin/commit/rollback/other)
//     and outcome (success/failure). Rolled into the four-golden-signals
//     dashboard via rate(...)[5m].
//   - found_footy_pg_query_duration_seconds{op} — histogram; buckets
//     span 1 ms → 16 s (14 exponential buckets, factor 2).
//   - found_footy_pg_pool_connections_{acquired,idle,total,max} —
//     gauges reported at scrape time via a Collector attached in
//     pg.New (one per pool).
func RegisterMetrics(reg *metrics.Registry, log logging.Emitter) *Observability {
	queries := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "found_footy",
		Subsystem: "pg",
		Name:      "queries_total",
		Help:      "Cumulative count of pg queries, by op + outcome.",
	}, []string{"op", "outcome"})

	queryDuration := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "found_footy",
		Subsystem: "pg",
		Name:      "query_duration_seconds",
		Help:      "Duration of pg queries in seconds, by op.",
		Buckets:   prometheus.ExponentialBuckets(0.001, 2, 14), // 1ms → ~16s
	}, []string{"op"})

	reg.PrometheusRegistry().MustRegister(queries, queryDuration)

	return &Observability{
		log:           log,
		reg:           reg,
		queries:       queries,
		queryDuration: queryDuration,
	}
}

// queryTracer implements pgx.QueryTracer. Attached to pgxpool.Config so
// every query fires start/end callbacks, letting us record metrics +
// structured logs without wrapping every pool method.
type queryTracer struct {
	obs *Observability
}

// queryTraceCtxKey is the private context key carrying per-query state
// from TraceQueryStart to TraceQueryEnd. Using a struct type prevents
// collisions with keys defined elsewhere.
type queryTraceCtxKey struct{}

type queryTraceData struct {
	start time.Time
	op    string
	sql   string
}

// TraceQueryStart is called by pgx before every query. Stashes the
// start time + classified op into the context; TraceQueryEnd reads them
// back on completion.
func (t *queryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, queryTraceCtxKey{}, &queryTraceData{
		start: time.Now(),
		op:    classifyOp(data.SQL),
		sql:   data.SQL,
	})
}

// TraceQueryEnd is called by pgx after every query — success or failure.
// Increments the counter, observes the histogram, and emits a DEBUG log
// on success or a WARN log on failure (with a bounded SQL preview).
func (t *queryTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	d, ok := ctx.Value(queryTraceCtxKey{}).(*queryTraceData)
	if !ok {
		return
	}
	elapsed := time.Since(d.start)
	outcome := "success"
	level := logging.LevelDebug
	action := vocabulary.ActionQuery
	msg := "pg query ok"

	if data.Err != nil {
		outcome = "failure"
		level = logging.LevelWarn
		action = vocabulary.ActionQueryFailed
		msg = "pg query failed"
	}

	t.obs.queries.WithLabelValues(d.op, outcome).Inc()
	t.obs.queryDuration.WithLabelValues(d.op).Observe(elapsed.Seconds())

	fields := []slog.Attr{
		logging.String("op", d.op),
		logging.Int64("elapsed_ms", elapsed.Milliseconds()),
	}
	if data.Err != nil {
		fields = append(fields,
			logging.String("sql_preview", previewSQL(d.sql, 200)),
			logging.Err(data.Err),
		)
	}
	t.obs.log.Emit(ctx, level, vocabulary.ModuleInfraPG, action, msg, fields...)
}

// classifyOp derives a bounded op label from the SQL string. Keeps the
// prometheus label cardinality tiny — one series per verb, not one per
// distinct query.
func classifyOp(sql string) string {
	trimmed := strings.TrimSpace(sql)
	if len(trimmed) == 0 {
		return "empty"
	}
	firstWord := trimmed
	for i, c := range trimmed {
		if c == ' ' || c == '\t' || c == '\n' {
			firstWord = trimmed[:i]
			break
		}
	}
	switch strings.ToLower(firstWord) {
	case "select":
		return "select"
	case "insert":
		return "insert"
	case "update":
		return "update"
	case "delete":
		return "delete"
	case "begin":
		return "begin"
	case "commit":
		return "commit"
	case "rollback":
		return "rollback"
	case "with":
		return "with"
	default:
		return "other"
	}
}

// previewSQL collapses whitespace and truncates to n runes so failing
// query logs stay readable in Grafana without dumping multi-KB SQL.
func previewSQL(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// poolStatsCollector reports pgxpool.Stat as gauges at prometheus
// scrape time. Registered with the metrics registry inside pg.New;
// unregistered by pool.Close so tests that open + close multiple pools
// don't collide.
type poolStatsCollector struct {
	pool *pgxpool.Pool

	acquiredDesc *prometheus.Desc
	idleDesc     *prometheus.Desc
	totalDesc    *prometheus.Desc
	maxDesc      *prometheus.Desc
}

func newPoolStatsCollector(pool *pgxpool.Pool) *poolStatsCollector {
	return &poolStatsCollector{
		pool: pool,
		acquiredDesc: prometheus.NewDesc(
			"found_footy_pg_pool_connections_acquired",
			"Currently acquired (in-use) connections in the pgxpool.",
			nil, nil,
		),
		idleDesc: prometheus.NewDesc(
			"found_footy_pg_pool_connections_idle",
			"Currently idle (open but unused) connections in the pgxpool.",
			nil, nil,
		),
		totalDesc: prometheus.NewDesc(
			"found_footy_pg_pool_connections_total",
			"Total open connections in the pgxpool (acquired + idle).",
			nil, nil,
		),
		maxDesc: prometheus.NewDesc(
			"found_footy_pg_pool_connections_max",
			"Configured MaxConns for the pgxpool.",
			nil, nil,
		),
	}
}

// Describe implements prometheus.Collector.
func (c *poolStatsCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.acquiredDesc
	ch <- c.idleDesc
	ch <- c.totalDesc
	ch <- c.maxDesc
}

// Collect implements prometheus.Collector. Reads pgxpool.Stat at scrape
// time so gauges are always fresh — no polling goroutine.
func (c *poolStatsCollector) Collect(ch chan<- prometheus.Metric) {
	stat := c.pool.Stat()
	ch <- prometheus.MustNewConstMetric(c.acquiredDesc, prometheus.GaugeValue, float64(stat.AcquiredConns()))
	ch <- prometheus.MustNewConstMetric(c.idleDesc, prometheus.GaugeValue, float64(stat.IdleConns()))
	ch <- prometheus.MustNewConstMetric(c.totalDesc, prometheus.GaugeValue, float64(stat.TotalConns()))
	ch <- prometheus.MustNewConstMetric(c.maxDesc, prometheus.GaugeValue, float64(stat.MaxConns()))
}
