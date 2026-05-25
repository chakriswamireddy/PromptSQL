package connectors

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	// Databricks SQL driver — imported for side-effect registration.
	_ "github.com/databricks/databricks-sql-go"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// databricksConnector implements Connector for Databricks (Unity Catalog).
//
// Minimum supported version: Databricks Runtime 12.0+ with Unity Catalog enabled.
//
// Session context propagation uses SET session.user_id = '...' which is
// readable within Unity Catalog Row Filters and Column Masks as
// CURRENT_USER() / session variables.
//
// Native enforcement:
//   - Row Filters: CREATE FUNCTION ... RETURN <bool_expr>, then
//     ALTER TABLE ... SET ROW FILTER <fn> ON (column, ...)
//   - Column Masks: CREATE FUNCTION ... RETURN <masked_expr>, then
//     ALTER TABLE ... ALTER COLUMN ... SET MASK <fn>
//
// Both are Unity Catalog features requiring Databricks Runtime 12.0+ and
// Unity Catalog-enabled metastore.
type databricksConnector struct {
	db     *sql.DB
	log    zerolog.Logger
	tracer trace.Tracer
	ds     *DataSource
}

func newDatabricksConnector(log zerolog.Logger, tracer trace.Tracer) *databricksConnector {
	return &databricksConnector{log: log, tracer: tracer}
}

func (d *databricksConnector) Engine() Engine { return EngineDatabricks }

// Connect opens a *sql.DB using the Databricks SQL driver.
// DSN format: token:<token>@<workspace>.azuredatabricks.net:443/sql/1.0/warehouses/<id>
func (d *databricksConnector) Connect(ctx context.Context, ds *DataSource) error {
	d.ds = ds
	db, err := sql.Open("databricks", ds.DSN)
	if err != nil {
		return fmt.Errorf("databricks: open: %w", err)
	}
	db.SetMaxOpenConns(5)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(30 * time.Minute)

	if err := db.PingContext(ctx); err != nil {
		db.Close() //nolint:errcheck
		return fmt.Errorf("databricks: ping: %w", err)
	}
	d.db = db
	d.log.Info().Str("data_source_id", ds.ID).Msg("databricks: connected")
	return nil
}

// EnforceContext sets Databricks session variables that Unity Catalog row
// filters and column masks can read via the built-in session() function or
// custom UDFs that call spark_catalog.governance.get_session_var().
func (d *databricksConnector) EnforceContext(ctx context.Context, sc *SessionContext) error {
	vars := []struct{ name, val string }{
		{"gov.tenant_id", sc.TenantID},
		{"gov.user_id", sc.UserID},
		{"gov.session_id", sc.SessionID},
		{"gov.roles", strings.Join(sc.Roles, ",")},
	}
	for _, v := range vars {
		// SET is a Spark SQL statement accepted by Databricks SQL.
		stmt := fmt.Sprintf("SET `%s` = ?", v.name)
		if _, err := d.db.ExecContext(ctx, stmt, v.val); err != nil {
			return fmt.Errorf("databricks: set %s: %w", v.name, err)
		}
	}
	return nil
}

// PrepareUDFs creates masking scalar functions in the governance catalog/schema.
// These are referenced by Column Mask policies.
func (d *databricksConnector) PrepareUDFs(ctx context.Context) error {
	_, span := d.tracer.Start(ctx, "databricks.PrepareUDFs",
		trace.WithAttributes(attribute.String("data_source_id", d.ds.ID)))
	defer span.End()

	// Ensure governance schema exists in the Unity Catalog.
	if _, err := d.db.ExecContext(ctx, "CREATE SCHEMA IF NOT EXISTS governance"); err != nil {
		return fmt.Errorf("databricks: create governance schema: %w", err)
	}

	udfs := []string{
		// mask_null
		`CREATE OR REPLACE FUNCTION governance.mask_null(v STRING)
		 RETURNS STRING
		 RETURN NULL`,

		// mask_hash — SHA-256 via built-in sha2().
		`CREATE OR REPLACE FUNCTION governance.mask_hash(v STRING)
		 RETURNS STRING
		 RETURN sha2(v, 256)`,

		// mask_partial — first 2, stars, last 2.
		`CREATE OR REPLACE FUNCTION governance.mask_partial(v STRING)
		 RETURNS STRING
		 RETURN CASE
		   WHEN length(v) <= 4 THEN repeat('*', length(v))
		   ELSE concat(left(v,2), repeat('*', length(v)-4), right(v,2))
		 END`,

		// mask_redact
		`CREATE OR REPLACE FUNCTION governance.mask_redact(v STRING)
		 RETURNS STRING
		 RETURN '[REDACTED]'`,
	}

	for _, udf := range udfs {
		if _, err := d.db.ExecContext(ctx, udf); err != nil {
			return fmt.Errorf("databricks: prepare udf: %w", err)
		}
	}
	return nil
}

// SyncNativePolicies applies Unity Catalog Row Filters and Column Masks.
// Each policy results in:
//  1. A filter function (CREATE OR REPLACE FUNCTION governance.gov_rf_<id>)
//  2. ALTER TABLE ... SET ROW FILTER <fn> ON (columns)
//  3. A mask function (CREATE OR REPLACE FUNCTION governance.gov_cm_<id>)
//  4. ALTER TABLE ... ALTER COLUMN ... SET MASK <fn>
func (d *databricksConnector) SyncNativePolicies(ctx context.Context, policies []*NativePolicy) (*SyncResult, error) {
	_, span := d.tracer.Start(ctx, "databricks.SyncNativePolicies",
		trace.WithAttributes(attribute.String("data_source_id", d.ds.ID),
			attribute.Int("policy_count", len(policies))))
	defer span.End()

	start := time.Now()
	result := &SyncResult{Engine: EngineDatabricks, DataSourceID: d.ds.ID, PoliciesTotal: len(policies)}

	if _, err := d.db.ExecContext(ctx, "CREATE SCHEMA IF NOT EXISTS governance"); err != nil {
		return result, fmt.Errorf("databricks: create governance schema: %w", err)
	}

	for _, pol := range policies {
		if err := d.applyPolicy(ctx, pol); err != nil {
			result.PoliciesErr++
			result.Errors = append(result.Errors, fmt.Errorf("policy %s: %w", pol.PolicyID, err))
			d.log.Error().Err(err).Str("policy_id", pol.PolicyID).Msg("databricks: policy apply failed")
		} else {
			result.PoliciesOK++
		}
	}

	result.Duration = time.Since(start)
	return result, nil
}

func (d *databricksConnector) applyPolicy(ctx context.Context, pol *NativePolicy) error {
	policyID8 := pol.PolicyID[:8]
	qualTable := fmt.Sprintf("`%s`.`%s`",
		databricksSafeIdent(pol.TableSchema),
		databricksSafeIdent(pol.TableName))

	// Step 1: Row Filter.
	if pol.RowFilter != "" {
		rfFnName := fmt.Sprintf("governance.gov_rf_%s_%s_%s",
			databricksSafeIdent(pol.TableSchema),
			databricksSafeIdent(pol.TableName),
			policyID8)

		createFn := fmt.Sprintf(`
			CREATE OR REPLACE FUNCTION %s()
			RETURNS BOOLEAN
			RETURN %s
		`, rfFnName, pol.RowFilter)

		if _, err := d.db.ExecContext(ctx, createFn); err != nil {
			return fmt.Errorf("databricks: create row filter fn %s: %w", rfFnName, err)
		}

		// Detach existing row filter first (idempotent).
		_, _ = d.db.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s DROP ROW FILTER", qualTable))

		setRF := fmt.Sprintf("ALTER TABLE %s SET ROW FILTER %s ON ()", qualTable, rfFnName)
		if _, err := d.db.ExecContext(ctx, setRF); err != nil {
			return fmt.Errorf("databricks: set row filter on %s: %w", qualTable, err)
		}
	}

	// Step 2: Column Mask.
	if pol.ColumnName != "" {
		cmFnName := fmt.Sprintf("governance.gov_cm_%s_%s_%s_%s",
			databricksSafeIdent(pol.TableSchema),
			databricksSafeIdent(pol.TableName),
			databricksSafeIdent(pol.ColumnName),
			policyID8)

		maskExpr := databricksMaskExpression(pol.MaskKind, "v")
		createFn := fmt.Sprintf(`
			CREATE OR REPLACE FUNCTION %s(v STRING)
			RETURNS STRING
			RETURN %s
		`, cmFnName, maskExpr)

		if _, err := d.db.ExecContext(ctx, createFn); err != nil {
			return fmt.Errorf("databricks: create column mask fn %s: %w", cmFnName, err)
		}

		// Drop existing mask first.
		dropMask := fmt.Sprintf("ALTER TABLE %s ALTER COLUMN `%s` DROP MASK",
			qualTable, databricksSafeIdent(pol.ColumnName))
		_, _ = d.db.ExecContext(ctx, dropMask)

		setMask := fmt.Sprintf("ALTER TABLE %s ALTER COLUMN `%s` SET MASK %s",
			qualTable, databricksSafeIdent(pol.ColumnName), cmFnName)
		if _, err := d.db.ExecContext(ctx, setMask); err != nil {
			return fmt.Errorf("databricks: set column mask on %s.%s.%s: %w",
				pol.TableSchema, pol.TableName, pol.ColumnName, err)
		}
	}

	return nil
}

func databricksMaskExpression(kind, varName string) string {
	switch kind {
	case "null":
		return "NULL"
	case "hash":
		return fmt.Sprintf("sha2(%s, 256)", varName)
	case "partial":
		return fmt.Sprintf("governance.mask_partial(%s)", varName)
	case "redact":
		return "'[REDACTED]'"
	default:
		return "NULL"
	}
}

// Crawl queries system.information_schema.columns for all user catalogs.
func (d *databricksConnector) Crawl(ctx context.Context) (*CatalogDelta, error) {
	_, span := d.tracer.Start(ctx, "databricks.Crawl",
		trace.WithAttributes(attribute.String("data_source_id", d.ds.ID)))
	defer span.End()

	rows, err := d.db.QueryContext(ctx, `
		SELECT
			table_schema,
			table_name,
			column_name,
			data_type,
			CASE is_nullable WHEN 'YES' THEN true ELSE false END AS nullable,
			ordinal_position,
			column_default,
			comment
		FROM system.information_schema.columns
		WHERE table_schema NOT IN ('information_schema','governance','system')
		ORDER BY table_schema, table_name, ordinal_position
	`)
	if err != nil {
		return nil, fmt.Errorf("databricks: crawl query: %w", err)
	}
	defer rows.Close()

	var cols []ColumnInfo
	for rows.Next() {
		var col ColumnInfo
		var nullable bool
		var colDefault, comment sql.NullString
		if err := rows.Scan(
			&col.SchemaName, &col.TableName, &col.ColumnName, &col.DataType,
			&nullable, &col.ColumnPosition, &colDefault, &comment,
		); err != nil {
			return nil, fmt.Errorf("databricks: crawl scan: %w", err)
		}
		col.Nullable = nullable
		if colDefault.Valid {
			col.ColumnDefault = &colDefault.String
		}
		if comment.Valid && comment.String != "" {
			col.ColumnComment = &comment.String
		}
		cols = append(cols, col)
	}
	return &CatalogDelta{Added: cols}, rows.Err()
}

// Execute runs a parameterized Databricks SQL query.  Uses ? placeholders.
func (d *databricksConnector) Execute(ctx context.Context, q *Query) (ResultStream, error) {
	_, span := d.tracer.Start(ctx, "databricks.Execute",
		trace.WithAttributes(
			attribute.String("data_source_id", d.ds.ID),
			attribute.String("trace_id", q.TraceID),
		))
	defer span.End()

	rows, err := d.db.QueryContext(ctx, q.SQL, q.Args...)
	if err != nil {
		return nil, fmt.Errorf("databricks: execute: %w", err)
	}
	return newSQLResultStream(rows, q.MaxRows)
}

func (d *databricksConnector) Capabilities() map[string]bool {
	return map[string]bool{
		"row_filter":         true, // Unity Catalog Row Filters
		"column_mask":        true, // Unity Catalog Column Masks
		"native_rls":         false,
		"ddm":                false,
		"row_access_policy":  false,
		"unity_catalog":      true,
		"transactions":       false,
		"information_schema": true,
	}
}

func (d *databricksConnector) Close() error {
	if d.db != nil {
		return d.db.Close()
	}
	return nil
}

func databricksSafeIdent(s string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			return r
		}
		return '_'
	}, s)
}
