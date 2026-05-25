package connectors

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	"cloud.google.com/go/bigquery"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

// bigqueryConnector implements Connector for Google BigQuery.
//
// Minimum supported version: BigQuery Standard Edition (any).
//
// Session context propagation: BigQuery is serverless and stateless; there are
// no session variables.  We propagate identity via:
//   - Per-query labels (tenant_id, user_id, session_id) for audit.
//   - The service account / impersonated SA credentials restrict which datasets
//     are accessible — row-level enforcement uses authorized views.
//
// Native enforcement:
//   - Row-level: authorized views in a governance dataset that filter rows.
//   - Column-level: BigQuery column-level Policy Tags (stub — full IAM integration
//     requires the Data Catalog API; this implementation creates the authorized view
//     with column projection as a fallback).
type bigqueryConnector struct {
	client    *bigquery.Client
	projectID string
	log       zerolog.Logger
	tracer    trace.Tracer
	ds        *DataSource
	// sessionLabels carry the current session context for per-query labels.
	sessionLabels map[string]string
}

func newBigQueryConnector(log zerolog.Logger, tracer trace.Tracer) *bigqueryConnector {
	return &bigqueryConnector{log: log, tracer: tracer}
}

func (b *bigqueryConnector) Engine() Engine { return EngineBigQuery }

// Connect initialises a BigQuery client.
// DSN for BigQuery is non-standard; we use a URI-style string:
//
//	bigquery://project-id?credentials_file=/path/to/sa.json
//
// Or with GOOGLE_APPLICATION_CREDENTIALS set, just:
//
//	bigquery://project-id
//
// The project ID is extracted from the DSN.  Credentials come from the
// service-account JSON file path stored in Vault.
func (b *bigqueryConnector) Connect(ctx context.Context, ds *DataSource) error {
	b.ds = ds

	// Parse project ID from DSN: "bigquery://project-id[?credentials_file=...]"
	projectID, credsFile := parseBigQueryDSN(ds.DSN)
	if projectID == "" {
		return fmt.Errorf("bigquery: dsn must start with bigquery://project-id")
	}
	b.projectID = projectID

	var opts []option.ClientOption
	if credsFile != "" {
		opts = append(opts, option.WithCredentialsFile(credsFile))
	}

	client, err := bigquery.NewClient(ctx, projectID, opts...)
	if err != nil {
		return fmt.Errorf("bigquery: new client for project %s: %w", projectID, err)
	}
	b.client = client
	b.log.Info().Str("data_source_id", ds.ID).Str("project", projectID).Msg("bigquery: connected")
	return nil
}

// EnforceContext stores labels that will be attached to every query.
// BigQuery itself has no session concept; we hash the user_id for privacy.
func (b *bigqueryConnector) EnforceContext(_ context.Context, sc *SessionContext) error {
	h := sha256.Sum256([]byte(sc.UserID))
	b.sessionLabels = map[string]string{
		"gov_tenant_id":  sanitizeBQLabel(sc.TenantID),
		"gov_user_hash":  fmt.Sprintf("%x", h[:8]), // 16 hex chars — BigQuery label limit is 63 chars
		"gov_session_id": sanitizeBQLabel(sc.SessionID),
	}
	return nil
}

// PrepareUDFs is a no-op for BigQuery; masking is handled by authorized views
// and Policy Tags.  The function exists to satisfy the Connector interface.
func (b *bigqueryConnector) PrepareUDFs(_ context.Context) error { return nil }

// SyncNativePolicies creates BigQuery authorized views in the governance dataset
// that apply row filtering and column projection (masking by exclusion or constant
// replacement).
//
// Policy Tags are not set here — they require the Data Catalog API and separate
// IAM bindings; that integration is tracked as a follow-up.
func (b *bigqueryConnector) SyncNativePolicies(ctx context.Context, policies []*NativePolicy) (*SyncResult, error) {
	_, span := b.tracer.Start(ctx, "bigquery.SyncNativePolicies",
		trace.WithAttributes(attribute.String("data_source_id", b.ds.ID),
			attribute.Int("policy_count", len(policies))))
	defer span.End()

	start := time.Now()
	result := &SyncResult{Engine: EngineBigQuery, DataSourceID: b.ds.ID, PoliciesTotal: len(policies)}

	// Ensure governance dataset exists.
	if err := b.ensureDataset(ctx, "governance"); err != nil {
		return result, err
	}

	for _, pol := range policies {
		if err := b.applyPolicy(ctx, pol); err != nil {
			result.PoliciesErr++
			result.Errors = append(result.Errors, fmt.Errorf("policy %s: %w", pol.PolicyID, err))
			b.log.Error().Err(err).Str("policy_id", pol.PolicyID).Msg("bigquery: policy apply failed")
		} else {
			result.PoliciesOK++
		}
	}

	result.Duration = time.Since(start)
	return result, nil
}

func (b *bigqueryConnector) ensureDataset(ctx context.Context, datasetID string) error {
	ds := b.client.Dataset(datasetID)
	_, err := ds.Metadata(ctx)
	if err == nil {
		return nil // already exists
	}
	meta := &bigquery.DatasetMetadata{
		Description: "Governance Platform authorized views and masking objects",
		Location:    "US",
	}
	if err := ds.Create(ctx, meta); err != nil {
		// Ignore "already exists" errors that may arise from race conditions.
		if !strings.Contains(err.Error(), "Already Exists") {
			return fmt.Errorf("bigquery: create governance dataset: %w", err)
		}
	}
	return nil
}

func (b *bigqueryConnector) applyPolicy(ctx context.Context, pol *NativePolicy) error {
	// Build column projection.
	var projection string
	if pol.ColumnName == "" {
		projection = "*"
	} else {
		// Column masking via SQL expression (NULL, hash, partial, '[REDACTED]').
		maskExpr := bqMaskExpression(pol.MaskKind, fmt.Sprintf("`%s`", pol.ColumnName))
		projection = fmt.Sprintf("* EXCEPT(`%s`), %s AS `%s`",
			pol.ColumnName, maskExpr, pol.ColumnName)
	}

	whereClause := ""
	if pol.RowFilter != "" {
		whereClause = "WHERE " + pol.RowFilter
	}

	viewName := fmt.Sprintf("gov_%s_%s_%s",
		sanitizeBQLabel(pol.TableSchema),
		sanitizeBQLabel(pol.TableName),
		pol.PolicyID[:8])

	viewSQL := fmt.Sprintf(
		"SELECT %s FROM `%s.%s.%s` %s",
		projection,
		b.projectID, pol.TableSchema, pol.TableName,
		whereClause,
	)

	view := b.client.Dataset("governance").Table(viewName)
	meta := &bigquery.TableMetadata{
		ViewDefinition: &bigquery.ViewDefinition{Query: viewSQL, UseLegacySQL: false},
		Description:    fmt.Sprintf("Governance authorized view for policy %s", pol.PolicyID),
	}

	// Check if view exists; update if so, create if not.
	existing, err := view.Metadata(ctx)
	if err == nil && existing != nil {
		// Update existing view.
		update := bigquery.TableMetadataToUpdate{
			ViewDefinition: meta.ViewDefinition,
		}
		if _, err := view.Update(ctx, update, ""); err != nil {
			return fmt.Errorf("bigquery: update authorized view %s: %w", viewName, err)
		}
	} else {
		if err := view.Create(ctx, meta); err != nil {
			if !strings.Contains(err.Error(), "Already Exists") {
				return fmt.Errorf("bigquery: create authorized view %s: %w", viewName, err)
			}
		}
	}
	return nil
}

func bqMaskExpression(kind, colExpr string) string {
	switch kind {
	case "null":
		return "NULL"
	case "hash":
		return fmt.Sprintf("TO_HEX(SHA256(CAST(%s AS STRING)))", colExpr)
	case "partial":
		return fmt.Sprintf(
			"CONCAT(SUBSTR(CAST(%s AS STRING),1,2), REPEAT('*', GREATEST(0, LENGTH(CAST(%s AS STRING))-4)), SUBSTR(CAST(%s AS STRING),-2))",
			colExpr, colExpr, colExpr,
		)
	case "redact":
		return "'[REDACTED]'"
	default:
		return "NULL"
	}
}

// Crawl queries BigQuery INFORMATION_SCHEMA.COLUMNS for all datasets.
func (b *bigqueryConnector) Crawl(ctx context.Context) (*CatalogDelta, error) {
	_, span := b.tracer.Start(ctx, "bigquery.Crawl",
		trace.WithAttributes(attribute.String("data_source_id", b.ds.ID)))
	defer span.End()

	q := b.client.Query(`
		SELECT
			table_schema,
			table_name,
			column_name,
			data_type,
			CASE is_nullable WHEN 'YES' THEN true ELSE false END AS nullable,
			ordinal_position
		FROM ` + "`" + b.projectID + "`.INFORMATION_SCHEMA.COLUMN_FIELD_PATHS" + `
		WHERE table_schema != 'governance'
		ORDER BY table_schema, table_name, ordinal_position
	`)
	q.Labels = b.sessionLabels

	it, err := q.Read(ctx)
	if err != nil {
		return nil, fmt.Errorf("bigquery: crawl query: %w", err)
	}

	var cols []ColumnInfo
	for {
		var row []bigquery.Value
		if err := it.Next(&row); err != nil {
			if err == iterator.Done {
				break
			}
			return nil, fmt.Errorf("bigquery: crawl iterate: %w", err)
		}
		if len(row) < 6 {
			continue
		}
		col := ColumnInfo{
			SchemaName: fmt.Sprintf("%v", row[0]),
			TableName:  fmt.Sprintf("%v", row[1]),
			ColumnName: fmt.Sprintf("%v", row[2]),
			DataType:   fmt.Sprintf("%v", row[3]),
		}
		if row[4] != nil {
			col.Nullable = row[4].(bool)
		}
		if row[5] != nil {
			if v, ok := row[5].(int64); ok {
				col.ColumnPosition = int(v)
			}
		}
		cols = append(cols, col)
	}
	return &CatalogDelta{Added: cols}, nil
}

// Execute runs a parameterized BigQuery query.
// Args are passed as query parameters (StandardSQL named params).
// Results are pre-fetched up to MaxRows and returned as a bqResultStream.
func (b *bigqueryConnector) Execute(ctx context.Context, q *Query) (ResultStream, error) {
	_, span := b.tracer.Start(ctx, "bigquery.Execute",
		trace.WithAttributes(
			attribute.String("data_source_id", b.ds.ID),
			attribute.String("trace_id", q.TraceID),
		))
	defer span.End()

	bqq := b.client.Query(q.SQL)
	bqq.Labels = b.sessionLabels
	if bqq.Labels == nil {
		bqq.Labels = make(map[string]string)
	}
	bqq.Labels["gov_trace_id"] = sanitizeBQLabel(q.TraceID)

	// Convert args to BigQuery query parameters.
	for i, arg := range q.Args {
		bqq.Parameters = append(bqq.Parameters, bigquery.QueryParameter{
			Name:  fmt.Sprintf("p%d", i+1),
			Value: arg,
		})
	}

	it, err := bqq.Read(ctx)
	if err != nil {
		return nil, fmt.Errorf("bigquery: execute: %w", err)
	}

	// Eagerly read up to MaxRows.
	maxRows := q.MaxRows
	if maxRows <= 0 {
		maxRows = 10000
	}
	var rows []bqRow
	var cols []string
	colsSet := false
	for i := 0; i < maxRows; i++ {
		var row []bigquery.Value
		if err := it.Next(&row); err != nil {
			if err == iterator.Done {
				break
			}
			return nil, fmt.Errorf("bigquery: iterate: %w", err)
		}
		if !colsSet {
			schema := it.Schema
			for _, f := range schema {
				cols = append(cols, f.Name)
			}
			colsSet = true
		}
		m := make(bqRow, len(cols))
		for j, f := range cols {
			if j < len(row) {
				m[f] = row[j]
			}
		}
		rows = append(rows, m)
	}

	return newBQResultStream(rows, cols), nil
}

func (b *bigqueryConnector) Capabilities() map[string]bool {
	return map[string]bool{
		"row_filter":         true,
		"column_mask":        true,
		"native_rls":         false,
		"ddm":                false,
		"row_access_policy":  false,
		"policy_tags":        true, // BigQuery column-level Policy Tags (stub)
		"authorized_views":   true,
		"transactions":       false,
		"information_schema": true,
	}
}

func (b *bigqueryConnector) Close() error {
	if b.client != nil {
		return b.client.Close()
	}
	return nil
}

// parseBigQueryDSN extracts project ID and optional credentials file from a
// bigquery:// URI.  Format: bigquery://project-id[?credentials_file=path]
func parseBigQueryDSN(dsn string) (projectID, credsFile string) {
	dsn = strings.TrimPrefix(dsn, "bigquery://")
	if idx := strings.Index(dsn, "?"); idx != -1 {
		params := dsn[idx+1:]
		projectID = dsn[:idx]
		for _, kv := range strings.Split(params, "&") {
			if strings.HasPrefix(kv, "credentials_file=") {
				credsFile = strings.TrimPrefix(kv, "credentials_file=")
			}
		}
	} else {
		projectID = dsn
	}
	return
}

// sanitizeBQLabel ensures a string is valid for a BigQuery label value
// (lowercase letters, numbers, hyphens; max 63 chars).
func sanitizeBQLabel(s string) string {
	s = strings.ToLower(s)
	s = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, s)
	if len(s) > 63 {
		s = s[:63]
	}
	return s
}
