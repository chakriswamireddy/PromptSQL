// Package crawler orchestrates a single crawl run for one data source.
package crawler

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"github.com/rs/zerolog"

	"github.com/governance-platform/pkg/audit"
	"github.com/governance-platform/schema-crawler/internal/classifier"
	"github.com/governance-platform/schema-crawler/internal/connector"
	"github.com/governance-platform/schema-crawler/internal/differ"
	"github.com/governance-platform/schema-crawler/internal/embedding"
	"github.com/governance-platform/schema-crawler/internal/metrics"
	"github.com/governance-platform/schema-crawler/internal/store"
)

var tracer = otel.Tracer("schema-crawler")

// Crawler executes one crawl run end-to-end.
type Crawler struct {
	db           *store.DB
	suggester    *classifier.Suggester
	embProvider  embedding.Provider
	auditClient  *audit.Client
	log          zerolog.Logger
	sampleMax    int
	embModel     string
	embDims      int
}

// New creates a Crawler.
func New(db *store.DB, emb embedding.Provider, auditClient *audit.Client, log zerolog.Logger, sampleMax int) *Crawler {
	return &Crawler{
		db:          db,
		suggester:   classifier.Default(),
		embProvider: emb,
		auditClient: auditClient,
		log:         log,
		sampleMax:   sampleMax,
		embModel:    emb.Model(),
		embDims:     emb.Dims(),
	}
}

// Run performs a full crawl of the given data source.
// dsn must be a read-only DSN retrieved from Vault for this data source.
func (c *Crawler) Run(ctx context.Context, tenantID, dataSourceID, dsn, triggeredBy string) error {
	ctx, span := tracer.Start(ctx, "crawler.Run",
		trace.WithAttributes(
			attribute.String("tenant_id", tenantID),
			attribute.String("data_source_id", dataSourceID),
		),
	)
	defer span.End()

	start := time.Now()
	runID, err := c.db.InsertCrawlRun(ctx, tenantID, dataSourceID, triggeredBy)
	if err != nil {
		return fmt.Errorf("insert crawl run: %w", err)
	}

	var runErr error
	defer func() {
		dur := time.Since(start).Seconds()
		metrics.CrawlerRunDuration.WithLabelValues(dataSourceID).Observe(dur)
	}()

	nNew, nChanged, nDropped, runErr := c.doCrawl(ctx, tenantID, dataSourceID, dsn)

	status := "success"
	var errMsg *string
	if runErr != nil {
		status = "failed"
		s := runErr.Error()
		errMsg = &s
		metrics.CrawlerRunTotal.WithLabelValues("failed").Inc()
		c.log.Error().Err(runErr).Str("run_id", runID).Msg("crawl failed")
	} else {
		metrics.CrawlerRunTotal.WithLabelValues("success").Inc()
		c.log.Info().
			Str("run_id", runID).
			Str("tenant_id", tenantID).
			Int("new", nNew).Int("changed", nChanged).Int("dropped", nDropped).
			Msg("crawl complete")
	}

	if err := c.db.UpdateCrawlRun(ctx, tenantID, runID, status, nNew, nChanged, nDropped, errMsg); err != nil {
		c.log.Error().Err(err).Str("run_id", runID).Msg("update crawl run failed")
	}

	return runErr
}

func (c *Crawler) doCrawl(ctx context.Context, tenantID, dataSourceID, dsn string) (nNew, nChanged, nDropped int, err error) {
	// 1. Connect read-only to target DB.
	pg, err := connector.NewPostgres(ctx, dsn, c.sampleMax)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("connect target db: %w", err)
	}
	defer pg.Close()

	// 2. Introspect information_schema.
	fresh, err := pg.Introspect(ctx)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("introspect: %w", err)
	}

	// 3. Load existing columns from control plane.
	existing, err := c.db.ListColumns(ctx, tenantID, dataSourceID)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("list columns: %w", err)
	}

	// 4. Diff.
	events, droppedIDs := differ.Diff(existing, fresh)
	for _, e := range events {
		switch e.Kind {
		case differ.DriftNew:
			nNew++
			metrics.CrawlerColumnsDiscovered.WithLabelValues("new").Inc()
		case differ.DriftChanged:
			nChanged++
			metrics.CrawlerColumnsDiscovered.WithLabelValues("changed").Inc()
		case differ.DriftDropped:
			nDropped++
			metrics.CrawlerColumnsDiscovered.WithLabelValues("dropped").Inc()
		}
	}

	// 5. Build store.Column rows from fresh data.
	cols := make([]store.Column, 0, len(fresh))
	var embJobs []store.EmbeddingJob

	for _, f := range fresh {
		fkJSON, _ := connector.FKRefsToJSON(f.FKReferences)

		// Determine quarantine and initial classification.
		quarantine := true
		classifiedBy := "steward"
		if sug := c.suggester.Suggest(f.ColumnName, f.DataType); sug != nil {
			quarantine = false
			classifiedBy = sug.ClassifiedBy
		}

		// Sample values only for non-sensitive columns.
		var samples []string
		if !quarantine && classifier.ShouldSample(resolvedClassification(f.ColumnName, f.DataType, c.suggester)) {
			samples, _ = pg.SampleColumn(ctx, f.SchemaName, f.TableName, f.ColumnName, c.sampleMax)
		}

		col := store.Column{
			TenantID:     tenantID,
			DataSourceID: dataSourceID,
			SchemaName:   f.SchemaName,
			TableName:    f.TableName,
			ColumnName:   f.ColumnName,
			DataType:     f.DataType,
			Nullable:     f.Nullable,
			Quarantine:   quarantine,
			SampleValues: samples,
			ClassifiedBy: classifiedBy,
			FKReferences: fkJSON,
			IndexNames:   f.IndexNames,
		}
		if f.ColumnPosition > 0 {
			n := f.ColumnPosition
			col.ColumnPosition = &n
		}
		col.ColumnDefault = f.ColumnDefault
		col.TableComment = f.TableComment
		col.ColumnComment = f.ColumnComment

		cols = append(cols, col)

		// Queue embeddings for non-quarantined columns.
		if !quarantine {
			tc := ""
			if f.TableComment != nil {
				tc = *f.TableComment
			}
			cc := ""
			if f.ColumnComment != nil {
				cc = *f.ColumnComment
			}
			payload := embedding.PayloadForColumn(f.SchemaName, f.TableName, f.ColumnName, f.DataType, nil, "", tc, cc)
			hash := embedding.PayloadHash(payload, c.embModel, c.embDims)
			embJobs = append(embJobs, store.EmbeddingJob{
				TenantID:    tenantID,
				PayloadHash: hash,
				Model:       c.embModel,
				Dimensions:  c.embDims,
			})
		}
	}

	// 6. Upsert columns.
	if err := c.db.UpsertColumns(ctx, tenantID, cols); err != nil {
		return 0, 0, 0, fmt.Errorf("upsert columns: %w", err)
	}

	// 7. Mark dropped columns.
	if err := c.db.MarkDropped(ctx, tenantID, dataSourceID, droppedIDs); err != nil {
		return 0, 0, 0, fmt.Errorf("mark dropped: %w", err)
	}

	// 8. Upsert FK relationships.
	for _, f := range fresh {
		for _, fk := range f.FKReferences {
			_ = c.db.InsertInferredRelationship(ctx, tenantID, dataSourceID,
				f.SchemaName, f.TableName, f.ColumnName,
				fk.ToSchema, fk.ToTable, fk.ToColumn,
				1.0, "fk",
			)
		}
	}

	// 9. Enqueue embeddings (non-blocking; worker picks up asynchronously).
	if err := c.db.EnqueueEmbeddings(ctx, tenantID, embJobs); err != nil {
		c.log.Warn().Err(err).Msg("enqueue embeddings failed (non-fatal)")
	}

	// 10. Emit drift audit events.
	if c.auditClient != nil {
		for _, e := range events {
			action := "schema.column." + strings.ToLower(string(e.Kind))
			c.auditClient.SystemEvent(ctx, audit.SystemEvent{
				TenantID: tenantID,
				Action:   action,
				Detail: map[string]any{
					"data_source_id": dataSourceID,
					"schema":         e.SchemaName,
					"table":          e.TableName,
					"column":         e.ColumnName,
					"old_type":       e.OldType,
					"new_type":       e.NewType,
				},
			})
		}
	}

	// 11. Update quarantine gauge.
	n, _ := c.db.CountQuarantined(ctx, tenantID)
	metrics.QuarantineGauge.WithLabelValues(tenantID).Set(float64(n))

	return nNew, nChanged, nDropped, nil
}

func resolvedClassification(columnName, dataType string, s *classifier.Suggester) string {
	if sug := s.Suggest(columnName, dataType); sug != nil {
		return sug.Classification
	}
	return "internal"
}
