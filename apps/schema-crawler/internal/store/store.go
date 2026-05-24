// Package store handles all control-plane DB operations for the schema crawler.
package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DB wraps pgxpool and enforces SET LOCAL discipline on every connection.
type DB struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *DB { return &DB{pool: pool} }

// DataSource is a minimal view of data_sources.
type DataSource struct {
	ID                  string
	TenantID            string
	Kind                string
	DisplayName         string
	ConnectionSecretRef string
	DefaultDB           string
	Status              string
}

// Column represents a row in schema_metadata.
type Column struct {
	ID               string
	TenantID         string
	DataSourceID     string
	SchemaName       string
	TableName        string
	ColumnName       string
	DataType         string
	Nullable         bool
	Description      string
	ClassificationID *string
	Quarantine       bool
	SampleValues     []string
	EmbeddingModel   *string
	EmbeddingDims    *int
	ClassifiedBy     string
	ColumnPosition   *int
	ColumnDefault    *string
	TableComment     *string
	ColumnComment    *string
	FKReferences     []byte
	IndexNames       []string
	LastCrawledAt    *time.Time
	DroppedAt        *time.Time
	LastSeenAt       time.Time
}

// CrawlRun tracks a single crawl execution.
type CrawlRun struct {
	ID             string
	TenantID       string
	DataSourceID   string
	Status         string
	TriggeredBy    string
	ColumnsNew     int
	ColumnsChanged int
	ColumnsDropped int
	ErrorMessage   *string
	StartedAt      time.Time
	FinishedAt     *time.Time
}

// EmbeddingJob is a row from embedding_queue.
type EmbeddingJob struct {
	ID          string
	TenantID    string
	ColumnID    string
	PayloadHash string
	Model       string
	Dimensions  int
	Attempts    int
}

// setSession applies tenant isolation before any query.
func (d *DB) setSession(ctx context.Context, tx pgx.Tx, tenantID string) error {
	_, err := tx.Exec(ctx,
		`SELECT set_config('app.tenant_id', $1, true), set_config('app.user_id', 'service:schema-crawler', true)`,
		tenantID,
	)
	return err
}

// ListTenantIDs returns all active tenant IDs. The tenants table has no RLS,
// so this is safe to call without a tenant context — it is the entry point
// for any system service that needs to iterate across all tenants.
func (d *DB) ListTenantIDs(ctx context.Context) ([]string, error) {
	rows, err := d.pool.Query(ctx, `
		SELECT id FROM tenants WHERE status = 'active' ORDER BY id
	`)
	if err != nil {
		return nil, fmt.Errorf("list tenant ids: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ListActiveDataSources returns all active data sources for a specific tenant.
// Requires the tenant context to satisfy RLS on data_sources.
func (d *DB) ListActiveDataSources(ctx context.Context, tenantID string) ([]DataSource, error) {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if err := d.setSession(ctx, tx, tenantID); err != nil {
		return nil, err
	}

	rows, err := tx.Query(ctx, `
		SELECT id, tenant_id, kind, display_name, connection_secret_ref,
		       COALESCE(default_db, ''), status
		FROM data_sources
		WHERE status = 'active'
		ORDER BY id
	`)
	if err != nil {
		return nil, fmt.Errorf("list data sources: %w", err)
	}
	defer rows.Close()

	var out []DataSource
	for rows.Next() {
		var ds DataSource
		if err := rows.Scan(&ds.ID, &ds.TenantID, &ds.Kind, &ds.DisplayName,
			&ds.ConnectionSecretRef, &ds.DefaultDB, &ds.Status); err != nil {
			return nil, err
		}
		out = append(out, ds)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, tx.Commit(ctx)
}

// GetLastCrawlRun returns the most recent crawl run for a data source.
func (d *DB) GetLastCrawlRun(ctx context.Context, tenantID, dataSourceID string) (*CrawlRun, error) {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if err := d.setSession(ctx, tx, tenantID); err != nil {
		return nil, err
	}

	var r CrawlRun
	err = tx.QueryRow(ctx, `
		SELECT id, tenant_id, data_source_id, status, triggered_by,
		       columns_new, columns_changed, columns_dropped, error_message,
		       started_at, finished_at
		FROM crawl_runs
		WHERE tenant_id = $1 AND data_source_id = $2
		ORDER BY started_at DESC
		LIMIT 1
	`, tenantID, dataSourceID).Scan(
		&r.ID, &r.TenantID, &r.DataSourceID, &r.Status, &r.TriggeredBy,
		&r.ColumnsNew, &r.ColumnsChanged, &r.ColumnsDropped, &r.ErrorMessage,
		&r.StartedAt, &r.FinishedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, tx.Commit(ctx)
	}
	if err != nil {
		return nil, err
	}
	return &r, tx.Commit(ctx)
}

// InsertCrawlRun creates a new crawl run record.
func (d *DB) InsertCrawlRun(ctx context.Context, tenantID, dataSourceID, triggeredBy string) (string, error) {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if err := d.setSession(ctx, tx, tenantID); err != nil {
		return "", err
	}

	var id string
	err = tx.QueryRow(ctx, `
		INSERT INTO crawl_runs (tenant_id, data_source_id, triggered_by, status)
		VALUES ($1, $2, $3, 'running')
		RETURNING id
	`, tenantID, dataSourceID, triggeredBy).Scan(&id)
	if err != nil {
		return "", err
	}
	return id, tx.Commit(ctx)
}

// UpdateCrawlRun updates the outcome of a crawl run.
func (d *DB) UpdateCrawlRun(ctx context.Context, tenantID, runID, status string, new, changed, dropped int, errMsg *string) error {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if err := d.setSession(ctx, tx, tenantID); err != nil {
		return err
	}

	_, err = tx.Exec(ctx, `
		UPDATE crawl_runs
		SET status = $1, columns_new = $2, columns_changed = $3, columns_dropped = $4,
		    error_message = $5, finished_at = now()
		WHERE id = $6
	`, status, new, changed, dropped, errMsg, runID)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ListColumns returns all schema_metadata rows for a data source.
func (d *DB) ListColumns(ctx context.Context, tenantID, dataSourceID string) ([]Column, error) {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if err := d.setSession(ctx, tx, tenantID); err != nil {
		return nil, err
	}

	rows, err := tx.Query(ctx, `
		SELECT id, tenant_id, data_source_id, schema_name, table_name, column_name,
		       data_type, nullable, COALESCE(description,''), classification_id,
		       quarantine, sample_values, embedding_model, embedding_dimensions,
		       classified_by, column_position, column_default, table_comment,
		       column_comment, fk_references, index_names, last_crawled_at,
		       dropped_at, last_seen_at
		FROM schema_metadata
		WHERE tenant_id = $1 AND data_source_id = $2
	`, tenantID, dataSourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Column
	for rows.Next() {
		var c Column
		if err := rows.Scan(
			&c.ID, &c.TenantID, &c.DataSourceID, &c.SchemaName, &c.TableName, &c.ColumnName,
			&c.DataType, &c.Nullable, &c.Description, &c.ClassificationID,
			&c.Quarantine, &c.SampleValues, &c.EmbeddingModel, &c.EmbeddingDims,
			&c.ClassifiedBy, &c.ColumnPosition, &c.ColumnDefault, &c.TableComment,
			&c.ColumnComment, &c.FKReferences, &c.IndexNames, &c.LastCrawledAt,
			&c.DroppedAt, &c.LastSeenAt,
		); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, tx.Commit(ctx)
}

// UpsertColumns bulk-upserts crawled column metadata.
func (d *DB) UpsertColumns(ctx context.Context, tenantID string, cols []Column) error {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if err := d.setSession(ctx, tx, tenantID); err != nil {
		return err
	}

	for _, c := range cols {
		_, err = tx.Exec(ctx, `
			INSERT INTO schema_metadata (
				tenant_id, data_source_id, schema_name, table_name, column_name,
				data_type, nullable, column_position, column_default, table_comment,
				column_comment, fk_references, index_names, sample_values, quarantine,
				classified_by, last_crawled_at, last_seen_at, dropped_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,now(),now(),$17)
			ON CONFLICT (tenant_id, data_source_id, schema_name, table_name, column_name)
			DO UPDATE SET
				data_type       = EXCLUDED.data_type,
				nullable        = EXCLUDED.nullable,
				column_position = EXCLUDED.column_position,
				column_default  = EXCLUDED.column_default,
				table_comment   = EXCLUDED.table_comment,
				column_comment  = EXCLUDED.column_comment,
				fk_references   = EXCLUDED.fk_references,
				index_names     = EXCLUDED.index_names,
				sample_values   = EXCLUDED.sample_values,
				dropped_at      = EXCLUDED.dropped_at,
				last_crawled_at = now(),
				last_seen_at    = now()
		`,
			c.TenantID, c.DataSourceID, c.SchemaName, c.TableName, c.ColumnName,
			c.DataType, c.Nullable, c.ColumnPosition, c.ColumnDefault, c.TableComment,
			c.ColumnComment, c.FKReferences, c.IndexNames, c.SampleValues, c.Quarantine,
			c.ClassifiedBy, c.DroppedAt,
		)
		if err != nil {
			return fmt.Errorf("upsert column %s.%s.%s: %w", c.SchemaName, c.TableName, c.ColumnName, err)
		}
	}
	return tx.Commit(ctx)
}

// MarkDropped marks columns not seen in the latest crawl.
func (d *DB) MarkDropped(ctx context.Context, tenantID, dataSourceID string, columnIDs []string) error {
	if len(columnIDs) == 0 {
		return nil
	}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if err := d.setSession(ctx, tx, tenantID); err != nil {
		return err
	}

	_, err = tx.Exec(ctx, `
		UPDATE schema_metadata
		SET dropped_at = now()
		WHERE tenant_id = $1 AND data_source_id = $2
		  AND id = ANY($3::uuid[]) AND dropped_at IS NULL
	`, tenantID, dataSourceID, columnIDs)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// EnqueueEmbeddings inserts pending embedding jobs for newly crawled/changed columns.
func (d *DB) EnqueueEmbeddings(ctx context.Context, tenantID string, jobs []EmbeddingJob) error {
	if len(jobs) == 0 {
		return nil
	}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if err := d.setSession(ctx, tx, tenantID); err != nil {
		return err
	}

	for _, j := range jobs {
		_, err = tx.Exec(ctx, `
			INSERT INTO embedding_queue (tenant_id, column_id, payload_hash, model, dimensions, status)
			VALUES ($1, $2, $3, $4, $5, 'pending')
			ON CONFLICT (column_id, model, payload_hash) DO NOTHING
		`, j.TenantID, j.ColumnID, j.PayloadHash, j.Model, j.Dimensions)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ClaimEmbeddingJobs atomically claims up to limit pending embedding jobs.
func (d *DB) ClaimEmbeddingJobs(ctx context.Context, tenantID string, limit int) ([]EmbeddingJob, error) {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if err := d.setSession(ctx, tx, tenantID); err != nil {
		return nil, err
	}

	rows, err := tx.Query(ctx, `
		UPDATE embedding_queue
		SET status = 'processing', attempts = attempts + 1
		WHERE id IN (
			SELECT id FROM embedding_queue
			WHERE tenant_id = $1 AND status = 'pending'
			ORDER BY enqueued_at
			LIMIT $2
			FOR UPDATE SKIP LOCKED
		)
		RETURNING id, tenant_id, column_id, payload_hash, model, dimensions, attempts
	`, tenantID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []EmbeddingJob
	for rows.Next() {
		var j EmbeddingJob
		if err := rows.Scan(&j.ID, &j.TenantID, &j.ColumnID, &j.PayloadHash, &j.Model, &j.Dimensions, &j.Attempts); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, tx.Commit(ctx)
}

// FinishEmbeddingJob marks a job done and stores the embedding vector.
func (d *DB) FinishEmbeddingJob(ctx context.Context, tenantID, jobID, columnID, model string, dims int, vector []float32) error {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if err := d.setSession(ctx, tx, tenantID); err != nil {
		return err
	}

	// Build pgvector literal from float32 slice.
	vectorLiteral := float32SliceToPgVector(vector)

	_, err = tx.Exec(ctx, `
		UPDATE schema_metadata
		SET embedding = $1::vector, embedding_model = $2, embedding_dimensions = $3
		WHERE id = $4
	`, vectorLiteral, model, dims, columnID)
	if err != nil {
		return err
	}

	_, err = tx.Exec(ctx, `
		UPDATE embedding_queue
		SET status = 'done', processed_at = now()
		WHERE id = $1
	`, jobID)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// FailEmbeddingJob marks a job failed with an error message.
func (d *DB) FailEmbeddingJob(ctx context.Context, tenantID, jobID, errMsg string) error {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if err := d.setSession(ctx, tx, tenantID); err != nil {
		return err
	}

	_, err = tx.Exec(ctx, `
		UPDATE embedding_queue
		SET status = CASE WHEN attempts >= 5 THEN 'failed' ELSE 'pending' END,
		    last_error = $1
		WHERE id = $2
	`, errMsg, jobID)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// ClassifyColumn updates the classification of a single column.
func (d *DB) ClassifyColumn(ctx context.Context, tenantID, columnID, classification, classifiedBy string, tags []string) error {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if err := d.setSession(ctx, tx, tenantID); err != nil {
		return err
	}

	// Upsert into data_classifications.
	_, err = tx.Exec(ctx, `
		INSERT INTO data_classifications (tenant_id, data_source_id, schema_name, table_name, column_name, classification, tags, classified_by)
		SELECT tenant_id, data_source_id, schema_name, table_name, column_name, $2, $3, $4
		FROM schema_metadata
		WHERE id = $1 AND tenant_id = $5
		ON CONFLICT (tenant_id, data_source_id, schema_name, table_name, column_name)
		DO UPDATE SET classification = EXCLUDED.classification, tags = EXCLUDED.tags,
		              classified_by = EXCLUDED.classified_by
	`, columnID, classification, tags, classifiedBy, tenantID)
	if err != nil {
		return err
	}

	// Link classification_id back into schema_metadata and release quarantine.
	_, err = tx.Exec(ctx, `
		UPDATE schema_metadata sm
		SET classification_id = dc.id,
		    classified_by = $3,
		    quarantine = false
		FROM data_classifications dc
		WHERE sm.id = $1
		  AND sm.tenant_id = $2
		  AND dc.tenant_id = sm.tenant_id
		  AND dc.data_source_id = sm.data_source_id
		  AND dc.schema_name = sm.schema_name
		  AND dc.table_name = sm.table_name
		  AND dc.column_name = sm.column_name
	`, columnID, tenantID, classifiedBy)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// InsertInferredRelationship upserts an FK-inferred relationship.
func (d *DB) InsertInferredRelationship(ctx context.Context, tenantID, dataSourceID string,
	fromSchema, fromTable, fromCol, toSchema, toTable, toCol string,
	confidence float64, source string) error {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if err := d.setSession(ctx, tx, tenantID); err != nil {
		return err
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO inferred_relationships (
			tenant_id, data_source_id,
			from_schema, from_table, from_column,
			to_schema,   to_table,   to_column,
			confidence, source
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (tenant_id, data_source_id, from_schema, from_table, from_column, to_schema, to_table, to_column)
		DO UPDATE SET confidence = EXCLUDED.confidence
	`, tenantID, dataSourceID, fromSchema, fromTable, fromCol, toSchema, toTable, toCol, confidence, source)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// CountQuarantined returns quarantined column counts per tenant.
func (d *DB) CountQuarantined(ctx context.Context, tenantID string) (int, error) {
	var n int
	err := d.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM schema_metadata
		WHERE tenant_id = $1 AND quarantine = true AND dropped_at IS NULL
	`, tenantID).Scan(&n)
	return n, err
}

// float32SliceToPgVector converts a float32 slice to a pgvector literal string.
func float32SliceToPgVector(v []float32) string {
	if len(v) == 0 {
		return "[]"
	}
	b := make([]byte, 0, len(v)*8+2)
	b = append(b, '[')
	for i, f := range v {
		if i > 0 {
			b = append(b, ',')
		}
		b = fmt.Appendf(b, "%g", f)
	}
	b = append(b, ']')
	return string(b)
}
