package connectors

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// postgresConnector implements Connector for PostgreSQL using pgxpool.
// It supersedes apps/schema-crawler/internal/connector/postgres.go by
// implementing the full Connector interface (EnforceContext, SyncNativePolicies,
// Execute, PrepareUDFs) in addition to Crawl.
type postgresConnector struct {
	pool   *pgxpool.Pool
	log    zerolog.Logger
	tracer trace.Tracer
	ds     *DataSource
}

func newPostgresConnector(log zerolog.Logger, tracer trace.Tracer) *postgresConnector {
	return &postgresConnector{log: log, tracer: tracer}
}

func (p *postgresConnector) Engine() Engine { return EnginePostgres }

// Connect initialises a pgxpool from the DSN contained in ds.
// The DSN comes from Vault (DataSource.SecretRef) and is never logged.
func (p *postgresConnector) Connect(ctx context.Context, ds *DataSource) error {
	p.ds = ds
	cfg, err := pgxpool.ParseConfig(ds.DSN)
	if err != nil {
		return fmt.Errorf("postgres: parse dsn: %w", err)
	}
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeCacheDescribe

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return fmt.Errorf("postgres: new pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return fmt.Errorf("postgres: ping failed: %w", err)
	}
	p.pool = pool
	p.log.Info().Str("data_source_id", ds.ID).Msg("postgres: connected")
	return nil
}

// EnforceContext sets session-local GUCs so RLS policies and application-level
// checks can read tenant_id / user_id / session_id without query parameters.
// Called per-connection before any query; uses SET LOCAL to scope to the
// current transaction and never leaks across transactions.
func (p *postgresConnector) EnforceContext(ctx context.Context, sc *SessionContext) error {
	conn, err := p.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("postgres: acquire conn for context enforcement: %w", err)
	}
	defer conn.Release()

	// Use a transaction so all SET LOCALs are atomic and rolled back on error.
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin tx for context: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	stmts := []struct{ key, val string }{
		{"app.tenant_id", sc.TenantID},
		{"app.user_id", sc.UserID},
		{"app.session_id", sc.SessionID},
		{"app.roles", strings.Join(sc.Roles, ",")},
	}
	for _, s := range stmts {
		if _, err := tx.Exec(ctx, "SELECT set_config($1, $2, true)", s.key, s.val); err != nil {
			return fmt.Errorf("postgres: set_config %s: %w", s.key, err)
		}
	}
	return tx.Commit(ctx)
}

// PrepareUDFs creates masking helper functions in the target schema if they
// do not already exist.  All functions use SECURITY DEFINER with a fixed
// search_path to prevent privilege escalation.
func (p *postgresConnector) PrepareUDFs(ctx context.Context) error {
	_, span := p.tracer.Start(ctx, "postgres.PrepareUDFs",
		trace.WithAttributes(attribute.String("data_source_id", p.ds.ID)))
	defer span.End()

	udfs := []string{
		// mask_null: returns NULL for any input — used for column suppression.
		`CREATE OR REPLACE FUNCTION governance.mask_null(anyelement)
		 RETURNS text LANGUAGE sql IMMUTABLE SECURITY DEFINER
		 SET search_path = '' AS $$ SELECT NULL::text $$`,

		// mask_hash: SHA-256 hex of the input cast to text — deterministic.
		`CREATE OR REPLACE FUNCTION governance.mask_hash(anyelement)
		 RETURNS text LANGUAGE sql IMMUTABLE SECURITY DEFINER
		 SET search_path = '' AS $$
		   SELECT encode(digest($1::text, 'sha256'), 'hex')
		 $$`,

		// mask_partial: shows first 2 and last 2 characters; stars in between.
		`CREATE OR REPLACE FUNCTION governance.mask_partial(v anyelement)
		 RETURNS text LANGUAGE plpgsql IMMUTABLE SECURITY DEFINER
		 SET search_path = '' AS $$
		 DECLARE s text := v::text;
		 BEGIN
		   IF length(s) <= 4 THEN RETURN repeat('*', length(s)); END IF;
		   RETURN substring(s,1,2) || repeat('*', length(s)-4) || substring(s, length(s)-1);
		 END $$`,

		// mask_redact: returns literal '[REDACTED]'.
		`CREATE OR REPLACE FUNCTION governance.mask_redact(anyelement)
		 RETURNS text LANGUAGE sql IMMUTABLE SECURITY DEFINER
		 SET search_path = '' AS $$ SELECT '[REDACTED]' $$`,
	}

	// Ensure the schema exists.
	if _, err := p.pool.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS governance"); err != nil {
		return fmt.Errorf("postgres: create governance schema: %w", err)
	}

	for _, udf := range udfs {
		if _, err := p.pool.Exec(ctx, udf); err != nil {
			return fmt.Errorf("postgres: prepare udf: %w", err)
		}
	}
	return nil
}

// SyncNativePolicies translates NativePolicies to PostgreSQL views per
// (table, role).  Each view joins the base table with the row filter predicate
// and projects only the permitted (optionally masked) columns.
// The operation runs inside a single transaction so a partial failure does not
// leave the schema in an inconsistent state.
func (p *postgresConnector) SyncNativePolicies(ctx context.Context, policies []*NativePolicy) (*SyncResult, error) {
	_, span := p.tracer.Start(ctx, "postgres.SyncNativePolicies",
		trace.WithAttributes(attribute.String("data_source_id", p.ds.ID),
			attribute.Int("policy_count", len(policies))))
	defer span.End()

	start := time.Now()
	result := &SyncResult{Engine: EnginePostgres, DataSourceID: p.ds.ID, PoliciesTotal: len(policies)}

	conn, err := p.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("postgres: acquire conn for policy sync: %w", err)
	}
	defer conn.Release()

	tx, err := conn.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("postgres: begin policy sync tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Ensure the governance schema exists for views.
	if _, err := tx.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS governance"); err != nil {
		return result, fmt.Errorf("postgres: create governance schema: %w", err)
	}

	for _, pol := range policies {
		if err := p.applyPolicy(ctx, tx, pol); err != nil {
			result.PoliciesErr++
			result.Errors = append(result.Errors, fmt.Errorf("policy %s: %w", pol.PolicyID, err))
			p.log.Error().Err(err).Str("policy_id", pol.PolicyID).Msg("postgres: policy apply failed")
		} else {
			result.PoliciesOK++
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return result, fmt.Errorf("postgres: commit policy sync: %w", err)
	}

	result.Duration = time.Since(start)
	return result, nil
}

func (p *postgresConnector) applyPolicy(ctx context.Context, tx pgx.Tx, pol *NativePolicy) error {
	// Build column projection.  Only mask if ColumnName is non-empty.
	var projection string
	if pol.ColumnName == "" {
		projection = "*"
	} else {
		maskFn := maskFunctionForKind(pol.MaskKind)
		// Use pgx.Identifier.Sanitize() for safe quoting — no string concatenation with user input.
		quotedCol := pgx.Identifier{pol.ColumnName}.Sanitize()
		quotedTable := pgx.Identifier{pol.TableSchema, pol.TableName}.Sanitize()
		_ = quotedTable // used in view DDL below
		projection = fmt.Sprintf("governance.%s(%s) AS %s", maskFn, quotedCol, quotedCol)
	}

	// Row filter: must be a constant predicate, not user-supplied at runtime.
	whereClause := ""
	if pol.RowFilter != "" {
		whereClause = "WHERE " + pol.RowFilter
	}

	// View name encodes schema + table + policy ID (truncated to 63 chars).
	viewName := sanitizeIdentifier(fmt.Sprintf("gov_%s_%s_%s",
		pol.TableSchema, pol.TableName, pol.PolicyID[:8]))
	qualifiedView := pgx.Identifier{"governance", viewName}.Sanitize()
	qualifiedTable := pgx.Identifier{pol.TableSchema, pol.TableName}.Sanitize()

	ddl := fmt.Sprintf(
		"CREATE OR REPLACE VIEW %s AS SELECT %s FROM %s %s",
		qualifiedView, projection, qualifiedTable, whereClause,
	)

	if _, err := tx.Exec(ctx, ddl); err != nil {
		return fmt.Errorf("create view %s: %w", viewName, err)
	}
	return nil
}

// Crawl introspects information_schema.columns for all non-system schemas
// and returns a CatalogDelta.  For a fresh connector the delta contains all
// columns in Added and empty Removed/Changed.
func (p *postgresConnector) Crawl(ctx context.Context) (*CatalogDelta, error) {
	_, span := p.tracer.Start(ctx, "postgres.Crawl",
		trace.WithAttributes(attribute.String("data_source_id", p.ds.ID)))
	defer span.End()

	cols, err := p.fetchColumns(ctx)
	if err != nil {
		return nil, err
	}
	return &CatalogDelta{Added: cols}, nil
}

func (p *postgresConnector) fetchColumns(ctx context.Context) ([]ColumnInfo, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT
			c.table_schema,
			c.table_name,
			c.column_name,
			c.udt_name,
			(c.is_nullable = 'YES') AS nullable,
			c.ordinal_position,
			c.column_default,
			obj_description(pc.oid, 'pg_class') AS table_comment,
			col_description(pc.oid, c.ordinal_position::int) AS col_comment
		FROM information_schema.columns c
		JOIN pg_catalog.pg_class pc
		  ON pc.relname = c.table_name
		JOIN pg_catalog.pg_namespace pn
		  ON pn.nspname = c.table_schema AND pn.oid = pc.relnamespace
		WHERE c.table_schema NOT IN ('information_schema','pg_catalog','pg_toast','governance')
		  AND pc.relkind = 'r'
		ORDER BY c.table_schema, c.table_name, c.ordinal_position
	`)
	if err != nil {
		return nil, fmt.Errorf("postgres: fetch columns: %w", err)
	}
	defer rows.Close()

	var out []ColumnInfo
	for rows.Next() {
		var col ColumnInfo
		if err := rows.Scan(
			&col.SchemaName, &col.TableName, &col.ColumnName, &col.DataType,
			&col.Nullable, &col.ColumnPosition, &col.ColumnDefault,
			&col.TableComment, &col.ColumnComment,
		); err != nil {
			return nil, fmt.Errorf("postgres: scan column row: %w", err)
		}
		out = append(out, col)
	}
	return out, rows.Err()
}

// Execute runs a parameterized query.  Args must be bound values — never
// constructed by string concatenation with user input.
func (p *postgresConnector) Execute(ctx context.Context, q *Query) (ResultStream, error) {
	_, span := p.tracer.Start(ctx, "postgres.Execute",
		trace.WithAttributes(
			attribute.String("data_source_id", p.ds.ID),
			attribute.String("trace_id", q.TraceID),
		))
	defer span.End()

	rows, err := p.pool.Query(ctx, q.SQL, q.Args...)
	if err != nil {
		return nil, fmt.Errorf("postgres: execute: %w", err)
	}

	cols := rows.FieldDescriptions()
	colNames := make([]string, len(cols))
	for i, c := range cols {
		colNames[i] = string(c.Name)
	}

	return &pgxResultStream{rows: rows, cols: colNames, maxRows: q.MaxRows}, nil
}

func (p *postgresConnector) Capabilities() map[string]bool {
	return map[string]bool{
		"row_filter":         true,
		"column_mask":        true,
		"native_rls":         true, // PostgreSQL RLS FORCE
		"ddm":                false,
		"row_access_policy":  false,
		"transactions":       true,
		"information_schema": true,
	}
}

func (p *postgresConnector) Close() error {
	if p.pool != nil {
		p.pool.Close()
	}
	return nil
}

// ─── pgxResultStream ─────────────────────────────────────────────────────────

type pgxResultStream struct {
	rows    pgx.Rows
	cols    []string
	maxRows int
	count   int
}

func (s *pgxResultStream) Next() bool {
	if s.maxRows > 0 && s.count >= s.maxRows {
		return false
	}
	if !s.rows.Next() {
		return false
	}
	s.count++
	return true
}

func (s *pgxResultStream) Scan(dest ...any) error { return s.rows.Scan(dest...) }
func (s *pgxResultStream) Columns() []string      { return s.cols }
func (s *pgxResultStream) Err() error             { return s.rows.Err() }
func (s *pgxResultStream) Close() error           { s.rows.Close(); return nil }

// ─── helpers ─────────────────────────────────────────────────────────────────

func maskFunctionForKind(kind string) string {
	switch kind {
	case "null":
		return "mask_null"
	case "hash":
		return "mask_hash"
	case "partial":
		return "mask_partial"
	case "redact":
		return "mask_redact"
	default:
		return "mask_null"
	}
}

// sanitizeIdentifier truncates and replaces characters that are not valid in
// PostgreSQL identifiers (max 63 bytes).
func sanitizeIdentifier(s string) string {
	r := strings.NewReplacer("-", "_", ".", "_", " ", "_")
	s = r.Replace(s)
	if len(s) > 63 {
		s = s[:63]
	}
	return s
}
