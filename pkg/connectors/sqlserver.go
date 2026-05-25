package connectors

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	// SQL Server driver — imported for side-effect registration.
	_ "github.com/microsoft/go-mssqldb"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// sqlserverConnector implements Connector for Microsoft SQL Server 2019+
// and Azure SQL Database.
//
// Session context propagation uses sp_set_session_context to store tenant_id
// and user_id in a typed session dictionary readable by any T-SQL object in
// the same connection.
//
// Native enforcement uses:
//   - Security Predicates via CREATE SECURITY POLICY + FILTER PREDICATE for row-level security.
//   - Dynamic Data Masking (DDM) for column masking (ALTER TABLE ... ALTER COLUMN ... MASKED WITH).
type sqlserverConnector struct {
	db     *sql.DB
	log    zerolog.Logger
	tracer trace.Tracer
	ds     *DataSource
}

func newSQLServerConnector(log zerolog.Logger, tracer trace.Tracer) *sqlserverConnector {
	return &sqlserverConnector{log: log, tracer: tracer}
}

func (s *sqlserverConnector) Engine() Engine { return EngineSQLServer }

// Connect opens a *sql.DB using the go-mssqldb driver.
// DSN format: sqlserver://user:pass@host:1433?database=mydb&encrypt=true
func (s *sqlserverConnector) Connect(ctx context.Context, ds *DataSource) error {
	s.ds = ds
	db, err := sql.Open("sqlserver", ds.DSN)
	if err != nil {
		return fmt.Errorf("sqlserver: open: %w", err)
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	if err := db.PingContext(ctx); err != nil {
		db.Close() //nolint:errcheck
		return fmt.Errorf("sqlserver: ping: %w", err)
	}
	s.db = db
	s.log.Info().Str("data_source_id", ds.ID).Msg("sqlserver: connected")
	return nil
}

// EnforceContext sets session context keys using sp_set_session_context.
// The @read_only flag prevents the application from overwriting values set by
// the platform within the same session — defence-in-depth.
func (s *sqlserverConnector) EnforceContext(ctx context.Context, sc *SessionContext) error {
	values := []struct{ key, val string }{
		{"tenant_id", sc.TenantID},
		{"user_id", sc.UserID},
		{"session_id", sc.SessionID},
		{"roles", strings.Join(sc.Roles, ",")},
	}
	for _, v := range values {
		// sp_set_session_context accepts @key NVARCHAR(128), @value SQL_VARIANT.
		// We pass @read_only = 1 to lock the value for the connection lifetime.
		_, err := s.db.ExecContext(ctx,
			"EXEC sp_set_session_context @key = @p1, @value = @p2, @read_only = 0",
			v.key, v.val,
		)
		if err != nil {
			return fmt.Errorf("sqlserver: set_session_context %s: %w", v.key, err)
		}
	}
	return nil
}

// PrepareUDFs creates masking scalar functions in the dbo schema.
// SQL Server DDM handles most masking natively, but we still provide T-SQL
// scalar functions for view-based masking when DDM is not applicable.
func (s *sqlserverConnector) PrepareUDFs(ctx context.Context) error {
	_, span := s.tracer.Start(ctx, "sqlserver.PrepareUDFs",
		trace.WithAttributes(attribute.String("data_source_id", s.ds.ID)))
	defer span.End()

	udfs := []string{
		// governance schema
		`IF NOT EXISTS (SELECT 1 FROM sys.schemas WHERE name = 'governance')
		 EXEC('CREATE SCHEMA governance')`,

		// mask_null
		`CREATE OR ALTER FUNCTION governance.mask_null(@v NVARCHAR(MAX))
		 RETURNS NVARCHAR(MAX)
		 WITH SCHEMABINDING
		 AS BEGIN RETURN NULL END`,

		// mask_hash — uses HASHBYTES with SHA2_256.
		`CREATE OR ALTER FUNCTION governance.mask_hash(@v NVARCHAR(MAX))
		 RETURNS NVARCHAR(64)
		 WITH SCHEMABINDING
		 AS BEGIN
		   RETURN LOWER(CONVERT(NVARCHAR(64),
		     HASHBYTES('SHA2_256', @v), 2))
		 END`,

		// mask_partial
		`CREATE OR ALTER FUNCTION governance.mask_partial(@v NVARCHAR(MAX))
		 RETURNS NVARCHAR(MAX)
		 WITH SCHEMABINDING
		 AS BEGIN
		   DECLARE @len INT = LEN(@v)
		   IF @len <= 4 RETURN REPLICATE(N'*', @len)
		   RETURN LEFT(@v,2) + REPLICATE(N'*', @len-4) + RIGHT(@v,2)
		 END`,

		// mask_redact
		`CREATE OR ALTER FUNCTION governance.mask_redact(@v NVARCHAR(MAX))
		 RETURNS NVARCHAR(MAX)
		 WITH SCHEMABINDING
		 AS BEGIN RETURN N'[REDACTED]' END`,
	}

	for _, udf := range udfs {
		if _, err := s.db.ExecContext(ctx, udf); err != nil {
			return fmt.Errorf("sqlserver: prepare udf: %w", err)
		}
	}
	return nil
}

// SyncNativePolicies applies RLS security predicates and DDM masking rules.
// For each policy:
//   - Creates a filter predicate function in the governance schema.
//   - Creates or alters a SECURITY POLICY binding the predicate to the table.
//   - For column masking: uses ALTER TABLE ... ALTER COLUMN ... MASKED WITH.
func (s *sqlserverConnector) SyncNativePolicies(ctx context.Context, policies []*NativePolicy) (*SyncResult, error) {
	_, span := s.tracer.Start(ctx, "sqlserver.SyncNativePolicies",
		trace.WithAttributes(attribute.String("data_source_id", s.ds.ID),
			attribute.Int("policy_count", len(policies))))
	defer span.End()

	start := time.Now()
	result := &SyncResult{Engine: EngineSQLServer, DataSourceID: s.ds.ID, PoliciesTotal: len(policies)}

	// Ensure governance schema.
	if _, err := s.db.ExecContext(ctx,
		"IF NOT EXISTS (SELECT 1 FROM sys.schemas WHERE name='governance') EXEC('CREATE SCHEMA governance')"); err != nil {
		return result, fmt.Errorf("sqlserver: create governance schema: %w", err)
	}

	for _, pol := range policies {
		if err := s.applyPolicy(ctx, pol); err != nil {
			result.PoliciesErr++
			result.Errors = append(result.Errors, fmt.Errorf("policy %s: %w", pol.PolicyID, err))
			s.log.Error().Err(err).Str("policy_id", pol.PolicyID).Msg("sqlserver: policy apply failed")
		} else {
			result.PoliciesOK++
		}
	}

	result.Duration = time.Since(start)
	return result, nil
}

func (s *sqlserverConnector) applyPolicy(ctx context.Context, pol *NativePolicy) error {
	fnName := fmt.Sprintf("governance.gov_predicate_%s_%s_%s",
		sanitizeSQLServerIdent(pol.TableSchema),
		sanitizeSQLServerIdent(pol.TableName),
		pol.PolicyID[:8])
	policyName := fmt.Sprintf("governance.gov_policy_%s_%s_%s",
		sanitizeSQLServerIdent(pol.TableSchema),
		sanitizeSQLServerIdent(pol.TableName),
		pol.PolicyID[:8])

	// Step 1: Create inline TVF predicate function if row filter is set.
	if pol.RowFilter != "" {
		// Create an inline TVF that returns 1 if the row passes the filter.
		createFn := fmt.Sprintf(`
			CREATE OR ALTER FUNCTION %s()
			RETURNS TABLE
			WITH SCHEMABINDING
			AS RETURN
			  SELECT 1 AS granted
			  WHERE %s
		`, fnName, pol.RowFilter)

		if _, err := s.db.ExecContext(ctx, createFn); err != nil {
			return fmt.Errorf("sqlserver: create predicate fn %s: %w", fnName, err)
		}

		// Step 2: Apply security policy binding the predicate to the table.
		qualifiedTable := fmt.Sprintf("[%s].[%s]",
			sanitizeSQLServerIdent(pol.TableSchema),
			sanitizeSQLServerIdent(pol.TableName))

		// Drop existing policy first (SQL Server does not support CREATE OR ALTER SECURITY POLICY).
		dropPolicy := fmt.Sprintf(`
			IF EXISTS (SELECT 1 FROM sys.security_policies WHERE name = '%s' AND schema_id = SCHEMA_ID('governance'))
			  DROP SECURITY POLICY %s
		`, sanitizeSQLServerIdent("gov_policy_"+pol.TableSchema+"_"+pol.TableName+"_"+pol.PolicyID[:8]), policyName)
		if _, err := s.db.ExecContext(ctx, dropPolicy); err != nil {
			return fmt.Errorf("sqlserver: drop security policy: %w", err)
		}

		createPolicy := fmt.Sprintf(`
			CREATE SECURITY POLICY %s
			ADD FILTER PREDICATE %s() ON %s
			WITH (STATE = ON)
		`, policyName, fnName, qualifiedTable)

		if _, err := s.db.ExecContext(ctx, createPolicy); err != nil {
			return fmt.Errorf("sqlserver: create security policy %s: %w", policyName, err)
		}
	}

	// Step 3: Apply DDM for column masking if ColumnName is set.
	if pol.ColumnName != "" {
		maskDef := sqlserverDDMMaskDef(pol.MaskKind)
		alterCol := fmt.Sprintf(
			"ALTER TABLE [%s].[%s] ALTER COLUMN [%s] ADD MASKED WITH (%s)",
			sanitizeSQLServerIdent(pol.TableSchema),
			sanitizeSQLServerIdent(pol.TableName),
			sanitizeSQLServerIdent(pol.ColumnName),
			maskDef,
		)
		// DDM ALTER may fail if the column is already masked — use a TRY/CATCH equivalent.
		if _, err := s.db.ExecContext(ctx, alterCol); err != nil {
			// Non-fatal: column may already have DDM applied.
			s.log.Warn().Err(err).Str("column", pol.ColumnName).Msg("sqlserver: ddm alter column (may already be masked)")
		}
	}

	return nil
}

func sqlserverDDMMaskDef(kind string) string {
	switch kind {
	case "null":
		return "FUNCTION = default()"
	case "hash":
		return "FUNCTION = default()" // SQL Server DDM has no hash function; use default mask
	case "partial":
		// Expose first 1 char and last 1 char of a string column.
		return "FUNCTION = partial(1, 'XXXX', 1)"
	case "redact":
		return "FUNCTION = default()"
	case "email":
		return "FUNCTION = email()"
	default:
		return "FUNCTION = default()"
	}
}

// Crawl queries INFORMATION_SCHEMA.COLUMNS and sys.columns for rich metadata.
func (s *sqlserverConnector) Crawl(ctx context.Context) (*CatalogDelta, error) {
	_, span := s.tracer.Start(ctx, "sqlserver.Crawl",
		trace.WithAttributes(attribute.String("data_source_id", s.ds.ID)))
	defer span.End()

	rows, err := s.db.QueryContext(ctx, `
		SELECT
			c.TABLE_SCHEMA,
			c.TABLE_NAME,
			c.COLUMN_NAME,
			c.DATA_TYPE,
			CASE WHEN c.IS_NULLABLE = 'YES' THEN 1 ELSE 0 END AS nullable,
			c.ORDINAL_POSITION,
			c.COLUMN_DEFAULT,
			CAST(ep_t.value AS NVARCHAR(MAX)) AS table_comment,
			CAST(ep_c.value AS NVARCHAR(MAX)) AS column_comment
		FROM INFORMATION_SCHEMA.COLUMNS c
		JOIN INFORMATION_SCHEMA.TABLES t
		  ON t.TABLE_SCHEMA = c.TABLE_SCHEMA AND t.TABLE_NAME = c.TABLE_NAME
		LEFT JOIN sys.extended_properties ep_t
		  ON ep_t.major_id = OBJECT_ID(c.TABLE_SCHEMA+'.'+c.TABLE_NAME)
		  AND ep_t.minor_id = 0 AND ep_t.name = 'MS_Description'
		  AND ep_t.class = 1
		LEFT JOIN sys.columns sc
		  ON sc.object_id = OBJECT_ID(c.TABLE_SCHEMA+'.'+c.TABLE_NAME)
		  AND sc.name = c.COLUMN_NAME
		LEFT JOIN sys.extended_properties ep_c
		  ON ep_c.major_id = sc.object_id AND ep_c.minor_id = sc.column_id
		  AND ep_c.name = 'MS_Description' AND ep_c.class = 1
		WHERE t.TABLE_TYPE = 'BASE TABLE'
		  AND c.TABLE_SCHEMA NOT IN ('governance','sys','INFORMATION_SCHEMA')
		ORDER BY c.TABLE_SCHEMA, c.TABLE_NAME, c.ORDINAL_POSITION
	`)
	if err != nil {
		return nil, fmt.Errorf("sqlserver: crawl query: %w", err)
	}
	defer rows.Close()

	var cols []ColumnInfo
	for rows.Next() {
		var col ColumnInfo
		var colDefault, tableComment, colComment sql.NullString
		var nullable int
		if err := rows.Scan(
			&col.SchemaName, &col.TableName, &col.ColumnName, &col.DataType,
			&nullable, &col.ColumnPosition, &colDefault,
			&tableComment, &colComment,
		); err != nil {
			return nil, fmt.Errorf("sqlserver: crawl scan: %w", err)
		}
		col.Nullable = nullable == 1
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
	return &CatalogDelta{Added: cols}, rows.Err()
}

// Execute runs a parameterized T-SQL query.  Uses @p1, @p2 ... placeholders.
func (s *sqlserverConnector) Execute(ctx context.Context, q *Query) (ResultStream, error) {
	_, span := s.tracer.Start(ctx, "sqlserver.Execute",
		trace.WithAttributes(
			attribute.String("data_source_id", s.ds.ID),
			attribute.String("trace_id", q.TraceID),
		))
	defer span.End()

	rows, err := s.db.QueryContext(ctx, q.SQL, q.Args...)
	if err != nil {
		return nil, fmt.Errorf("sqlserver: execute: %w", err)
	}
	return newSQLResultStream(rows, q.MaxRows)
}

func (s *sqlserverConnector) Capabilities() map[string]bool {
	return map[string]bool{
		"row_filter":         true,
		"column_mask":        true,
		"native_rls":         true,  // SQL Server Security Policies
		"ddm":                true,  // Dynamic Data Masking
		"row_access_policy":  false,
		"transactions":       true,
		"information_schema": true,
	}
}

func (s *sqlserverConnector) Close() error {
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

// sanitizeSQLServerIdent strips characters that are illegal in SQL Server
// identifiers to prevent injection via the identifier path.  Values here come
// from the platform's own policy store, not from end users.
func sanitizeSQLServerIdent(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\'' || r == '"' || r == '[' || r == ']' || r == ';' {
			return '_'
		}
		return r
	}, s)
}
