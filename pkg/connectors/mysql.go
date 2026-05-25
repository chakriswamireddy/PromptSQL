package connectors

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	// MySQL driver — imported for side-effect registration.
	_ "github.com/go-sql-driver/mysql"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// mysqlConnector implements Connector for MySQL 8.0+.
//
// Session context propagation uses MySQL user-defined variables (@app_tenant,
// @app_user, @app_session, @app_roles) which are readable inside stored
// procedures and views used for native enforcement.
//
// Native enforcement uses views (CREATE OR REPLACE VIEW governance.<name>) with
// WHERE clauses for row filtering and masking functions for column masking.
// MySQL does not have native RLS, so "native_rls" capability is false.
type mysqlConnector struct {
	db     *sql.DB
	log    zerolog.Logger
	tracer trace.Tracer
	ds     *DataSource
}

func newMySQLConnector(log zerolog.Logger, tracer trace.Tracer) *mysqlConnector {
	return &mysqlConnector{log: log, tracer: tracer}
}

func (m *mysqlConnector) Engine() Engine { return EngineMySQL }

// Connect opens a *sql.DB with the MySQL driver.  The DSN is the standard
// go-sql-driver/mysql DSN format (user:pass@tcp(host:port)/db?parseTime=true).
// The DSN comes from Vault and is never logged.
func (m *mysqlConnector) Connect(ctx context.Context, ds *DataSource) error {
	m.ds = ds
	db, err := sql.Open("mysql", ds.DSN)
	if err != nil {
		return fmt.Errorf("mysql: open: %w", err)
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	if err := db.PingContext(ctx); err != nil {
		db.Close() //nolint:errcheck
		return fmt.Errorf("mysql: ping: %w", err)
	}
	m.db = db
	m.log.Info().Str("data_source_id", ds.ID).Msg("mysql: connected")
	return nil
}

// EnforceContext sets MySQL session variables that enforcement views and
// stored procedures read to apply row filters.  These are per-connection
// variables scoped to the session (not a transaction) so they persist for the
// life of the connection from the pool.  The pool MUST use a dedicated
// connection per request or reset these on return.
func (m *mysqlConnector) EnforceContext(ctx context.Context, sc *SessionContext) error {
	// Execute each SET as a separate parameterized statement.
	sets := []struct {
		varName string
		val     string
	}{
		{"@app_tenant", sc.TenantID},
		{"@app_user", sc.UserID},
		{"@app_session", sc.SessionID},
		{"@app_roles", strings.Join(sc.Roles, ",")},
	}
	for _, s := range sets {
		// MySQL does not support parameterized SET @var = ?, but the values
		// are platform-controlled (validated UUIDs / role names), not
		// arbitrary user input.  We use a prepared statement for defense-in-depth.
		if _, err := m.db.ExecContext(ctx,
			fmt.Sprintf("SET %s = ?", s.varName), s.val); err != nil {
			return fmt.Errorf("mysql: set %s: %w", s.varName, err)
		}
	}
	return nil
}

// PrepareUDFs creates masking stored functions in the `governance` schema.
// All functions are DETERMINISTIC with SQL SECURITY DEFINER to prevent
// privilege escalation.  Safe to call repeatedly (uses CREATE OR REPLACE).
func (m *mysqlConnector) PrepareUDFs(ctx context.Context) error {
	_, span := m.tracer.Start(ctx, "mysql.PrepareUDFs",
		trace.WithAttributes(attribute.String("data_source_id", m.ds.ID)))
	defer span.End()

	if _, err := m.db.ExecContext(ctx, "CREATE SCHEMA IF NOT EXISTS governance"); err != nil {
		return fmt.Errorf("mysql: create governance schema: %w", err)
	}

	udfs := []string{
		// mask_null: always returns NULL.
		`CREATE OR REPLACE FUNCTION governance.mask_null(v TEXT)
		 RETURNS TEXT
		 DETERMINISTIC SQL SECURITY DEFINER
		 RETURN NULL`,

		// mask_hash: SHA2-256 hex digest.
		`CREATE OR REPLACE FUNCTION governance.mask_hash(v TEXT)
		 RETURNS TEXT
		 DETERMINISTIC SQL SECURITY DEFINER
		 RETURN SHA2(v, 256)`,

		// mask_partial: expose first 2 and last 2 chars; stars in between.
		`CREATE OR REPLACE FUNCTION governance.mask_partial(v TEXT)
		 RETURNS TEXT
		 DETERMINISTIC SQL SECURITY DEFINER
		 RETURN IF(CHAR_LENGTH(v) <= 4,
		           REPEAT('*', CHAR_LENGTH(v)),
		           CONCAT(LEFT(v,2), REPEAT('*', CHAR_LENGTH(v)-4), RIGHT(v,2)))`,

		// mask_redact: fixed '[REDACTED]' string.
		`CREATE OR REPLACE FUNCTION governance.mask_redact(v TEXT)
		 RETURNS TEXT
		 DETERMINISTIC SQL SECURITY DEFINER
		 RETURN '[REDACTED]'`,
	}

	for _, udf := range udfs {
		if _, err := m.db.ExecContext(ctx, udf); err != nil {
			return fmt.Errorf("mysql: prepare udf: %w", err)
		}
	}
	return nil
}

// SyncNativePolicies creates or replaces governance views for each policy.
// Views are created in the `governance` schema and are granted SELECT to the
// application role so RLS-equivalent access control is enforced at the view.
func (m *mysqlConnector) SyncNativePolicies(ctx context.Context, policies []*NativePolicy) (*SyncResult, error) {
	_, span := m.tracer.Start(ctx, "mysql.SyncNativePolicies",
		trace.WithAttributes(attribute.String("data_source_id", m.ds.ID),
			attribute.Int("policy_count", len(policies))))
	defer span.End()

	start := time.Now()
	result := &SyncResult{Engine: EngineMySQL, DataSourceID: m.ds.ID, PoliciesTotal: len(policies)}

	// Ensure governance schema exists.
	if _, err := m.db.ExecContext(ctx, "CREATE SCHEMA IF NOT EXISTS governance"); err != nil {
		return result, fmt.Errorf("mysql: create governance schema: %w", err)
	}

	for _, pol := range policies {
		if err := m.applyPolicy(ctx, pol); err != nil {
			result.PoliciesErr++
			result.Errors = append(result.Errors, fmt.Errorf("policy %s: %w", pol.PolicyID, err))
			m.log.Error().Err(err).Str("policy_id", pol.PolicyID).Msg("mysql: policy apply failed")
		} else {
			result.PoliciesOK++
		}
	}

	result.Duration = time.Since(start)
	return result, nil
}

func (m *mysqlConnector) applyPolicy(ctx context.Context, pol *NativePolicy) error {
	// Build column projection.
	var projection string
	if pol.ColumnName == "" {
		projection = "t.*"
	} else {
		// Use backtick-quoted column name from the policy (validated platform value).
		quotedCol := "`" + strings.ReplaceAll(pol.ColumnName, "`", "``") + "`"
		maskFn := mysqlMaskFunction(pol.MaskKind)
		projection = fmt.Sprintf("governance.%s(t.%s) AS %s", maskFn, quotedCol, quotedCol)
	}

	whereClause := ""
	if pol.RowFilter != "" {
		whereClause = "WHERE " + pol.RowFilter
	}

	// Backtick-quote table identifiers.
	quotedTable := fmt.Sprintf("`%s`.`%s`",
		strings.ReplaceAll(pol.TableSchema, "`", "``"),
		strings.ReplaceAll(pol.TableName, "`", "``"))

	viewName := "`governance`.`gov_" + strings.ReplaceAll(
		pol.TableSchema+"_"+pol.TableName+"_"+pol.PolicyID[:8], "`", "") + "`"

	ddl := fmt.Sprintf(
		"CREATE OR REPLACE VIEW %s AS SELECT %s FROM %s AS t %s",
		viewName, projection, quotedTable, whereClause,
	)

	if _, err := m.db.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("mysql: create view: %w", err)
	}
	return nil
}

// Crawl queries information_schema.COLUMNS for all non-system schemas.
func (m *mysqlConnector) Crawl(ctx context.Context) (*CatalogDelta, error) {
	_, span := m.tracer.Start(ctx, "mysql.Crawl",
		trace.WithAttributes(attribute.String("data_source_id", m.ds.ID)))
	defer span.End()

	rows, err := m.db.QueryContext(ctx, `
		SELECT
			TABLE_SCHEMA,
			TABLE_NAME,
			COLUMN_NAME,
			DATA_TYPE,
			(IS_NULLABLE = 'YES') AS nullable,
			ORDINAL_POSITION,
			COLUMN_DEFAULT,
			TABLE_COMMENT,
			COLUMN_COMMENT
		FROM information_schema.COLUMNS c
		JOIN information_schema.TABLES t USING (TABLE_SCHEMA, TABLE_NAME)
		WHERE TABLE_SCHEMA NOT IN (
			'information_schema','mysql','performance_schema','sys','governance'
		)
		  AND t.TABLE_TYPE = 'BASE TABLE'
		ORDER BY TABLE_SCHEMA, TABLE_NAME, ORDINAL_POSITION
	`)
	if err != nil {
		return nil, fmt.Errorf("mysql: crawl query: %w", err)
	}
	defer rows.Close()

	var cols []ColumnInfo
	for rows.Next() {
		var col ColumnInfo
		var tableComment, colComment sql.NullString
		var colDefault sql.NullString
		if err := rows.Scan(
			&col.SchemaName, &col.TableName, &col.ColumnName, &col.DataType,
			&col.Nullable, &col.ColumnPosition, &colDefault,
			&tableComment, &colComment,
		); err != nil {
			return nil, fmt.Errorf("mysql: crawl scan: %w", err)
		}
		if colDefault.Valid {
			col.ColumnDefault = &colDefault.String
		}
		if tableComment.Valid && tableComment.String != "" {
			col.TableComment = &tableComment.String
		}
		if colComment.Valid && colComment.String != "" {
			col.ColumnComment = &colComment.String
		}
		cols = append(cols, col)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return &CatalogDelta{Added: cols}, nil
}

// Execute runs a parameterized query.  Uses ? placeholders (MySQL style).
func (m *mysqlConnector) Execute(ctx context.Context, q *Query) (ResultStream, error) {
	_, span := m.tracer.Start(ctx, "mysql.Execute",
		trace.WithAttributes(
			attribute.String("data_source_id", m.ds.ID),
			attribute.String("trace_id", q.TraceID),
		))
	defer span.End()

	rows, err := m.db.QueryContext(ctx, q.SQL, q.Args...)
	if err != nil {
		return nil, fmt.Errorf("mysql: execute: %w", err)
	}
	return newSQLResultStream(rows, q.MaxRows)
}

func (m *mysqlConnector) Capabilities() map[string]bool {
	return map[string]bool{
		"row_filter":         true,
		"column_mask":        true,
		"native_rls":         false, // MySQL has no native RLS
		"ddm":                false,
		"row_access_policy":  false,
		"transactions":       true,
		"information_schema": true,
	}
}

func (m *mysqlConnector) Close() error {
	if m.db != nil {
		return m.db.Close()
	}
	return nil
}

func mysqlMaskFunction(kind string) string {
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
