// Package metrics registers all Prometheus metrics for schema-crawler.
package metrics

import "github.com/prometheus/client_golang/prometheus"

var (
	CrawlerRunTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "crawler_run_total",
		Help: "Total crawl runs by result.",
	}, []string{"result"})

	CrawlerColumnsDiscovered = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "crawler_columns_discovered_total",
		Help: "Columns discovered by kind (new, changed, dropped).",
	}, []string{"kind"})

	CrawlerRunDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "crawler_run_duration_seconds",
		Help:    "Duration of a crawl run in seconds.",
		Buckets: prometheus.DefBuckets,
	}, []string{"data_source_id"})

	EmbeddingCostUSD = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "embedding_cost_usd_total",
		Help: "Estimated embedding cost in USD per tenant.",
	}, []string{"tenant_id"})

	EmbeddingQueueDepth = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "embedding_queue_depth",
		Help: "Number of columns pending embedding.",
	}, []string{"tenant_id"})

	EmbeddingErrors = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "embedding_errors_total",
		Help: "Total embedding errors by error kind.",
	}, []string{"kind"})

	QuarantineGauge = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "catalog_quarantined_columns",
		Help: "Number of columns in quarantine per tenant.",
	}, []string{"tenant_id"})

	ClassificationCoverage = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "catalog_classified_columns_ratio",
		Help: "Ratio of classified to total columns per tenant.",
	}, []string{"tenant_id"})
)

func Register() {
	prometheus.MustRegister(
		CrawlerRunTotal,
		CrawlerColumnsDiscovered,
		CrawlerRunDuration,
		EmbeddingCostUSD,
		EmbeddingQueueDepth,
		EmbeddingErrors,
		QuarantineGauge,
		ClassificationCoverage,
	)
}
