package connectors

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	// Snowflake driver — imported for side-effect registration.
	_ "github.com/snowflakedb/gosnowflake"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// snowflakeConnector implements Connector for Snowflake.
//
// Minimum supported version: Snowflake (SaaS — version is always current).
//
// Session context propagation uses ALTER SESSION SET <var> = '<value>' which
// stores values accessible within UDFs and Row Access Policies via CURRENT_SESSION().
// Because Snowflake shares the underlying VPS concept via Snowpark UDF / JavaScript
// UDFs, we use a combination of session variables and Row Access Policies (RAP).
//
// Native enforcement:
//   - Row Access Policies (RAP): CREATE ROW ACCESS POLICY ... AS ... RETURNS BOOLEAN
//   - Dynamic Data Masking (DDM): CREATE MASKING POLICY ... AS ... RETURNS ...
//   - Both attached to tables via ALTER TABLE.
type snowflakeConnector struct {
	db     *sql.DB
	log    zerolog.Logger
	tracer trace.Tracer
	ds     *DataSource
}

func newSnowflakeConnector(log zerolog.Logger, tracer trace.Tracer) *snowflakeConnector {
	return &snowflakeConnector{log: log, tracer: tracer}
}

func (s *snowflakeConnector) Engine() Engine { return EngineSnowflake }

// Connect opens a *sql.DB using the gosnowflake driver.
// DSN format: user:pass@account/database/schema?warehouse=WH&role=ROLE
// The DSN comes from Vault and is never logged.
func (s *snowflakeConnector) Connect(ctx context.Context, ds *DataSource) error {
	s.ds = ds
	db, err := sql.Open("snowflake", ds.DSN)
	if err != nil {
		return fmt.Errorf("snowflake: open: %w", err)
	}
	db.SetMaxOpenConns(5)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(30 * time.Minute)

	if err := db.PingContext(ctx); err != nil {
		db.Close() //nolint:errcheck
		return fmt.Errorf("snowflake: ping: %w", err)
	}
	s.db = db
	s.log.Info().Str("data_source_id", ds.ID).Msg("snowflake: connected")
	return nil
}

// EnforceContext sets Snowflake session parameters that Row Access Policies
// and Masking Policies can read via SYSTEM$PIPE_STATUS or session variables.
// Snowflake session parameters are set with ALTER SESSION SET.
func (s *snowflakeConnector) EnforceContext(ctx context.Context, sc *SessionContext) error {
	// Snowflake session variables: ALTER SESSION SET <var> = '<value>'
	// Values are platform-controlled (UUID, role list), not arbitrary user input.
	params := []struct{ name, val string }{
		{"GOV_TENANT_ID", sc.TenantID},
		{"GOV_USER_ID", sc.UserID},
		{"GOV_SESSION_ID", sc.SessionID},
		{"GOV_ROLES", strings.Join(sc.Roles, ",")},
	}
	for _, p := range params {
		stmt := fmt.Sprintf("ALTER SESSION SET %s = ?", p.name)
		if _, err := s.db.ExecContext(ctx, stmt, p.val); err != nil {
			return fmt.Errorf("snowflake: set session %s: %w", p.name, err)
		}
	}
	return nil
}

// PrepareUDFs creates Snowflake masking UDFs (JavaScript UDFs).
// These are referenced by Masking Policies.
func (s *snowflakeConnector) PrepareUDFs(ctx context.Context) error {
	_, span := s.tracer.Start(ctx, "snowflake.PrepareUDFs",
		trace.WithAttributes(attribute.String("data_source_id", s.ds.ID)))
	defer span.End()

	// Ensure the GOVERNANCE schema exists.
	if _, err := s.db.ExecContext(ctx, "CREATE SCHEMA IF NOT EXISTS GOVERNANCE"); err != nil {
		return fmt.Errorf("snowflake: create governance schema: %w", err)
	}

	udfs := []string{
		// MASK_NULL — always returns NULL.
		`CREATE OR REPLACE FUNCTION GOVERNANCE.MASK_NULL(V STRING)
		 RETURNS STRING
		 LANGUAGE SQL
		 AS $$ NULL $$`,

		// MASK_HASH — SHA2 256-bit hex.
		`CREATE OR REPLACE FUNCTION GOVERNANCE.MASK_HASH(V STRING)
		 RETURNS STRING
		 LANGUAGE SQL
		 AS $$ SHA2(V, 256) $$`,

		// MASK_PARTIAL — first 2, stars, last 2.
		`CREATE OR REPLACE FUNCTION GOVERNANCE.MASK_PARTIAL(V STRING)
		 RETURNS STRING
		 LANGUAGE JAVASCRIPT AS $$
		   if (!V || V.length <= 4) return '*'.repeat(V ? V.length : 0);
		   return V.slice(0,2) + '*'.repeat(V.length-4) + V.slice(-2);
		 $$`,

		// MASK_REDACT — fixed string.
		`CREATE OR REPLACE FUNCTION GOVERNANCE.MASK_REDACT(V STRING)
		 RETURNS STRING
		 LANGUAGE SQL
		 AS $$ '[REDACTED]' $$`,
	}

	for _, udf := range udfs {
		if _, err := s.db.ExecContext(ctx, udf); err != nil {
			return fmt.Errorf("snowflake: prepare udf: %w", err)
		}
	}
	return nil
}

// SyncNativePolicies creates Snowflake Row Access Policies and Masking Policies
// and attaches them to the target tables via ALTER TABLE.
func (s *snowflakeConnector) SyncNativePolicies(ctx context.Context, policies []*NativePolicy) (*SyncResult, error) {
	_, span := s.tracer.Start(ctx, "snowflake.SyncNativePolicies",
		trace.WithAttributes(attribute.String("data_source_id", s.ds.ID),
			attribute.Int("policy_count", len(policies))))
	defer span.End()

	start := time.Now()
	result := &SyncResult{Engine: EngineSnowflake, DataSourceID: s.ds.ID, PoliciesTotal: len(policies)}

	if _, err := s.db.ExecContext(ctx, "CREATE SCHEMA IF NOT EXISTS GOVERNANCE"); err != nil {
		return result, fmt.Errorf("snowflake: create governance schema: %w", err)
	}

	for _, pol := range policies {
		if err := s.applyPolicy(ctx, pol); err != nil {
			result.PoliciesErr++
			result.Errors = append(result.Errors, fmt.Errorf("policy %s: %w", pol.PolicyID, err))
			s.log.Error().Err(err).Str("policy_id", pol.PolicyID).Msg("snowflake: policy apply failed")
		} else {
			result.PoliciesOK++
		}
	}

	result.Duration = time.Since(start)
	return result, nil
}

func (s *snowflakeConnector) applyPolicy(ctx context.Context, pol *NativePolicy) error {
	policyID8 := strings.ToUpper(pol.PolicyID[:8])
	schemaUpper := strings.ToUpper(pol.TableSchema)
	tableUpper := strings.ToUpper(pol.TableName)

	// Row Access Policy for row filtering.
	if pol.RowFilter != "" {
		rapName := fmt.Sprintf("GOVERNANCE.GOV_RAP_%s_%s_%s",
			snowflakeSafeIdent(schemaUpper),
			snowflakeSafeIdent(tableUpper),
			policyID8)

		createRAP := fmt.Sprintf(`
			CREATE OR REPLACE ROW ACCESS POLICY %s
			AS () RETURNS BOOLEAN ->
			  %s
		`, rapName, pol.RowFilter)

		if _, err := s.db.ExecContext(ctx, createRAP); err != nil {
			return fmt.Errorf("snowflake: create RAP %s: %w", rapName, err)
		}

		// Attach to table (detach first for idempotency).
		detach := fmt.Sprintf(
			"ALTER TABLE %s.%s DROP ALL ROW ACCESS POLICIES",
			schemaUpper, tableUpper)
		// Non-fatal if no existing policies.
		_, _ = s.db.ExecContext(ctx, detach)

		attach := fmt.Sprintf(
			"ALTER TABLE %s.%s ADD ROW ACCESS POLICY %s ON ()",
			schemaUpper, tableUpper, rapName)
		if _, err := s.db.ExecContext(ctx, attach); err != nil {
			return fmt.Errorf("snowflake: attach RAP to %s.%s: %w", schemaUpper, tableUpper, err)
		}
	}

	// Dynamic Data Masking policy for column masking.
	if pol.ColumnName != "" {
		colUpper := strings.ToUpper(pol.ColumnName)
		mpName := fmt.Sprintf("GOVERNANCE.GOV_MP_%s_%s_%s_%s",
			snowflakeSafeIdent(schemaUpper),
			snowflakeSafeIdent(tableUpper),
			snowflakeSafeIdent(colUpper),
			policyID8)

		maskExpr := snowflakeMaskExpression(pol.MaskKind, "VAL")
		createMP := fmt.Sprintf(`
			CREATE OR REPLACE MASKING POLICY %s
			AS (VAL STRING) RETURNS STRING ->
			  CASE
			    WHEN CURRENT_SESSION() IS NOT NULL THEN %s
			    ELSE VAL
			  END
		`, mpName, maskExpr)

		if _, err := s.db.ExecContext(ctx, createMP); err != nil {
			return fmt.Errorf("snowflake: create masking policy %s: %w", mpName, err)
		}

		// Attach masking policy to the column.
		detachMP := fmt.Sprintf(
			"ALTER TABLE %s.%s MODIFY COLUMN %s UNSET MASKING POLICY",
			schemaUpper, tableUpper, colUpper)
		_, _ = s.db.ExecContext(ctx, detachMP)

		attachMP := fmt.Sprintf(
			"ALTER TABLE %s.%s MODIFY COLUMN %s SET MASKING POLICY %s",
			schemaUpper, tableUpper, colUpper, mpName)
		if _, err := s.db.ExecContext(ctx, attachMP); err != nil {
			return fmt.Errorf("snowflake: attach masking policy to %s.%s.%s: %w",
				schemaUpper, tableUpper, colUpper, err)
		}
	}

	return nil
}

func snowflakeMaskExpression(kind, varName string) string {
	switch kind {
	case "null":
		return "NULL"
	case "hash":
		return fmt.Sprintf("GOVERNANCE.MASK_HASH(%s)", varName)
	case "partial":
		return fmt.Sprintf("GOVERNANCE.MASK_PARTIAL(%s)", varName)
	case "redact":
		return fmt.Sprintf("GOVERNANCE.MASK_REDACT(%s)", varName)
	default:
		return "NULL"
	}
}

// Crawl queries Snowflake INFORMATION_SCHEMA.COLUMNS for all non-system schemas.
func (s *snowflakeConnector) Crawl(ctx context.Context) (*CatalogDelta, error) {
	_, span := s.tracer.Start(ctx, "snowflake.Crawl",
		trace.WithAttributes(attribute.String("data_source_id", s.ds.ID)))
	defer span.End()

	rows, err := s.db.QueryContext(ctx, `
		SELECT
			TABLE_SCHEMA,
			TABLE_NAME,
			COLUMN_NAME,
			DATA_TYPE,
			CASE IS_NULLABLE WHEN 'YES' THEN 1 ELSE 0 END AS nullable,
			ORDINAL_POSITION,
			COLUMN_DEFAULT,
			COMMENT AS col_comment
		FROM INFORMATION_SCHEMA.COLUMNS
		WHERE TABLE_SCHEMA NOT IN ('INFORMATION_SCHEMA','GOVERNANCE')
		ORDER BY TABLE_SCHEMA, TABLE_NAME, ORDINAL_POSITION
	`)
	if err != nil {
		return nil, fmt.Errorf("snowflake: crawl query: %w", err)
	}
	defer rows.Close()

	var cols []ColumnInfo
	for rows.Next() {
		var col ColumnInfo
		var nullable int
		var colDefault, colComment sql.NullString
		if err := rows.Scan(
			&col.SchemaName, &col.TableName, &col.ColumnName, &col.DataType,
			&nullable, &col.ColumnPosition, &colDefault, &colComment,
		); err != nil {
			return nil, fmt.Errorf("snowflake: crawl scan: %w", err)
		}
		col.Nullable = nullable == 1
		if colDefault.Valid {
			col.ColumnDefault = &colDefault.String
		}
		if colComment.Valid && colComment.String != "" {
			col.ColumnComment = &colComment.String
		}
		cols = append(cols, col)
	}
	return &CatalogDelta{Added: cols}, rows.Err()
}

// Execute runs a parameterized Snowflake SQL query.  Uses ? placeholders.
func (s *snowflakeConnector) Execute(ctx context.Context, q *Query) (ResultStream, error) {
	_, span := s.tracer.Start(ctx, "snowflake.Execute",
		trace.WithAttributes(
			attribute.String("data_source_id", s.ds.ID),
			attribute.String("trace_id", q.TraceID),
		))
	defer span.End()

	rows, err := s.db.QueryContext(ctx, q.SQL, q.Args...)
	if err != nil {
		return nil, fmt.Errorf("snowflake: execute: %w", err)
	}
	return newSQLResultStream(rows, q.MaxRows)
}

func (s *snowflakeConnector) Capabilities() map[string]bool {
	return map[string]bool{
		"row_filter":        true,
		"column_mask":       true,
		"native_rls":        false,
		"ddm":               true,  // Dynamic Data Masking
		"row_access_policy": true,  // Row Access Policies
		"transactions":      false, // Snowflake supports DML transactions but not DDL rollback
		"information_schema": true,
	}
}

func (s *snowflakeConnector) Close() error {
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

func snowflakeSafeIdent(s string) string {
	s = strings.Map(func(r rune) rune {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			return r
		}
		return '_'
	}, strings.ToUpper(s))
	if len(s) > 40 {
		s = s[:40]
	}
	return s
}
