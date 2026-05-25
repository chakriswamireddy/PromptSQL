package connectors

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	// Oracle driver — imported for side-effect registration.
	_ "github.com/sijms/go-ora/v2"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// oracleConnector implements Connector for Oracle Database 19c+.
//
// Session context propagation uses DBMS_SESSION.SET_IDENTIFIER to embed a
// client identifier in the Oracle session, plus DBMS_APPLICATION_INFO to store
// structured attributes.  DBMS_RLS (Virtual Private Database) provides
// row-level security natively.
//
// Minimum supported version: Oracle 19c (19.3+).
type oracleConnector struct {
	db     *sql.DB
	log    zerolog.Logger
	tracer trace.Tracer
	ds     *DataSource
}

func newOracleConnector(log zerolog.Logger, tracer trace.Tracer) *oracleConnector {
	return &oracleConnector{log: log, tracer: tracer}
}

func (o *oracleConnector) Engine() Engine { return EngineOracle }

// Connect opens a *sql.DB using the go-ora driver.
// DSN format: oracle://user:pass@host:1521/service_name
// The DSN comes from Vault and must never be logged.
func (o *oracleConnector) Connect(ctx context.Context, ds *DataSource) error {
	o.ds = ds
	db, err := sql.Open("oracle", ds.DSN)
	if err != nil {
		return fmt.Errorf("oracle: open: %w", err)
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(10 * time.Minute)

	if err := db.PingContext(ctx); err != nil {
		db.Close() //nolint:errcheck
		return fmt.Errorf("oracle: ping: %w", err)
	}
	o.db = db
	o.log.Info().Str("data_source_id", ds.ID).Msg("oracle: connected")
	return nil
}

// EnforceContext sets Oracle client identifier and application info via
// DBMS_SESSION.SET_IDENTIFIER and DBMS_APPLICATION_INFO.SET_CLIENT_INFO.
// These values are readable by VPD (DBMS_RLS) policy functions and by the
// Oracle Unified Audit trail.
//
// The identifier string is built as "tenant_id:user_id:session_id" (max 64 chars).
func (o *oracleConnector) EnforceContext(ctx context.Context, sc *SessionContext) error {
	// Compose identifier (Oracle limit is 64 bytes).
	identifier := sc.TenantID + ":" + sc.UserID + ":" + sc.SessionID
	if len(identifier) > 64 {
		identifier = identifier[:64]
	}
	roleStr := strings.Join(sc.Roles, ",")
	if len(roleStr) > 256 {
		roleStr = roleStr[:256]
	}

	// Use a single anonymous PL/SQL block to minimise round-trips.
	plsql := `BEGIN
	  DBMS_SESSION.SET_IDENTIFIER(:1);
	  DBMS_APPLICATION_INFO.SET_CLIENT_INFO(:2);
	  DBMS_APPLICATION_INFO.SET_MODULE(module_name => :3, action_name => :4);
	END;`

	if _, err := o.db.ExecContext(ctx, plsql,
		identifier, roleStr, "governance-platform", sc.TenantID,
	); err != nil {
		return fmt.Errorf("oracle: set context: %w", err)
	}
	return nil
}

// PrepareUDFs creates masking functions in the GOVERNANCE schema.
// Uses CREATE OR REPLACE so it is idempotent.
func (o *oracleConnector) PrepareUDFs(ctx context.Context) error {
	_, span := o.tracer.Start(ctx, "oracle.PrepareUDFs",
		trace.WithAttributes(attribute.String("data_source_id", o.ds.ID)))
	defer span.End()

	// Ensure the GOVERNANCE schema/user exists (controlled environment only).
	// In production, the DBA creates the schema; we attempt a no-op CREATE.
	udfs := []string{
		// mask_null
		`CREATE OR REPLACE FUNCTION GOVERNANCE.MASK_NULL(v IN VARCHAR2)
		 RETURN VARCHAR2 DETERMINISTIC IS
		 BEGIN RETURN NULL; END;`,

		// mask_hash — uses DBMS_CRYPTO SHA-256.
		`CREATE OR REPLACE FUNCTION GOVERNANCE.MASK_HASH(v IN VARCHAR2)
		 RETURN VARCHAR2 DETERMINISTIC IS
		   raw_val RAW(32767);
		   hashed  RAW(32);
		 BEGIN
		   raw_val := UTL_RAW.CAST_TO_RAW(v);
		   hashed  := DBMS_CRYPTO.HASH(raw_val, DBMS_CRYPTO.HASH_SH256);
		   RETURN LOWER(RAWTOHEX(hashed));
		 END;`,

		// mask_partial
		`CREATE OR REPLACE FUNCTION GOVERNANCE.MASK_PARTIAL(v IN VARCHAR2)
		 RETURN VARCHAR2 DETERMINISTIC IS
		   l PLS_INTEGER := LENGTH(v);
		 BEGIN
		   IF l IS NULL OR l <= 4 THEN
		     RETURN LPAD('*', NVL(l,0), '*');
		   END IF;
		   RETURN SUBSTR(v,1,2) || LPAD('*', l-4, '*') || SUBSTR(v,-2);
		 END;`,

		// mask_redact
		`CREATE OR REPLACE FUNCTION GOVERNANCE.MASK_REDACT(v IN VARCHAR2)
		 RETURN VARCHAR2 DETERMINISTIC IS
		 BEGIN RETURN '[REDACTED]'; END;`,
	}

	for _, udf := range udfs {
		if _, err := o.db.ExecContext(ctx, udf); err != nil {
			return fmt.Errorf("oracle: prepare udf: %w", err)
		}
	}
	return nil
}

// SyncNativePolicies registers DBMS_RLS policies for row-level security and
// applies Oracle Data Redaction for column masking.
// This uses Oracle Virtual Private Database (VPD) which transparently appends
// WHERE clauses to every query on the table.
func (o *oracleConnector) SyncNativePolicies(ctx context.Context, policies []*NativePolicy) (*SyncResult, error) {
	_, span := o.tracer.Start(ctx, "oracle.SyncNativePolicies",
		trace.WithAttributes(attribute.String("data_source_id", o.ds.ID),
			attribute.Int("policy_count", len(policies))))
	defer span.End()

	start := time.Now()
	result := &SyncResult{Engine: EngineOracle, DataSourceID: o.ds.ID, PoliciesTotal: len(policies)}

	for _, pol := range policies {
		if err := o.applyPolicy(ctx, pol); err != nil {
			result.PoliciesErr++
			result.Errors = append(result.Errors, fmt.Errorf("policy %s: %w", pol.PolicyID, err))
			o.log.Error().Err(err).Str("policy_id", pol.PolicyID).Msg("oracle: policy apply failed")
		} else {
			result.PoliciesOK++
		}
	}
	result.Duration = time.Since(start)
	return result, nil
}

func (o *oracleConnector) applyPolicy(ctx context.Context, pol *NativePolicy) error {
	schemaUpper := strings.ToUpper(pol.TableSchema)
	tableUpper := strings.ToUpper(pol.TableName)
	policyName := oracleSafeIdent("GOV_" + pol.PolicyID[:8])

	if pol.RowFilter != "" {
		// Register a DBMS_RLS policy for row-level enforcement.
		// The policy function must be in GOVERNANCE schema and return a predicate string.
		// We create an inline function that returns the static predicate.
		fnName := oracleSafeIdent("GOV_FN_" + pol.PolicyID[:8])
		fnBody := fmt.Sprintf(`
			CREATE OR REPLACE FUNCTION GOVERNANCE.%s(
			  schema_name IN VARCHAR2,
			  table_name  IN VARCHAR2)
			RETURN VARCHAR2 IS
			BEGIN
			  RETURN '%s';
			END;`, fnName, strings.ReplaceAll(pol.RowFilter, "'", "''"))

		if _, err := o.db.ExecContext(ctx, fnBody); err != nil {
			return fmt.Errorf("oracle: create vpd function %s: %w", fnName, err)
		}

		// Drop existing policy (idempotent).
		dropPol := fmt.Sprintf(`
			BEGIN
			  DBMS_RLS.DROP_POLICY(
			    object_schema  => '%s',
			    object_name    => '%s',
			    policy_name    => '%s'
			  );
			EXCEPTION WHEN OTHERS THEN NULL;
			END;`, schemaUpper, tableUpper, policyName)
		if _, err := o.db.ExecContext(ctx, dropPol); err != nil {
			return fmt.Errorf("oracle: drop vpd policy: %w", err)
		}

		// Add VPD policy.
		addPol := fmt.Sprintf(`
			BEGIN
			  DBMS_RLS.ADD_POLICY(
			    object_schema   => '%s',
			    object_name     => '%s',
			    policy_name     => '%s',
			    function_schema => 'GOVERNANCE',
			    policy_function => '%s',
			    statement_types => 'SELECT',
			    update_check    => FALSE,
			    enable          => TRUE
			  );
			END;`, schemaUpper, tableUpper, policyName, fnName)
		if _, err := o.db.ExecContext(ctx, addPol); err != nil {
			return fmt.Errorf("oracle: add vpd policy %s: %w", policyName, err)
		}
	}

	// Oracle Data Redaction for column masking.
	if pol.ColumnName != "" {
		colUpper := strings.ToUpper(pol.ColumnName)
		redactFn := oracleRedactFunction(pol.MaskKind, colUpper)
		addRedact := fmt.Sprintf(`
			BEGIN
			  DBMS_REDACT.ADD_POLICY(
			    object_schema  => '%s',
			    object_name    => '%s',
			    column_name    => '%s',
			    policy_name    => '%s',
			    function_type  => %s,
			    expression     => '1=1'
			  );
			EXCEPTION WHEN OTHERS THEN
			  -- Policy may already exist; alter it instead.
			  DBMS_REDACT.ALTER_POLICY(
			    object_schema  => '%s',
			    object_name    => '%s',
			    column_name    => '%s',
			    policy_name    => '%s',
			    action         => DBMS_REDACT.MODIFY_COLUMN,
			    function_type  => %s
			  );
			END;`,
			schemaUpper, tableUpper, colUpper, policyName+"_R", redactFn,
			schemaUpper, tableUpper, colUpper, policyName+"_R", redactFn)
		if _, err := o.db.ExecContext(ctx, addRedact); err != nil {
			o.log.Warn().Err(err).Str("column", pol.ColumnName).Msg("oracle: data redaction apply (non-fatal)")
		}
	}

	return nil
}

func oracleRedactFunction(kind, _ string) string {
	switch kind {
	case "null":
		return "DBMS_REDACT.NULLIFY"
	case "partial":
		return "DBMS_REDACT.PARTIAL"
	case "redact", "hash":
		return "DBMS_REDACT.FULL"
	default:
		return "DBMS_REDACT.NULLIFY"
	}
}

// Crawl queries ALL_TAB_COLUMNS which includes all tables accessible to the
// connected user.
func (o *oracleConnector) Crawl(ctx context.Context) (*CatalogDelta, error) {
	_, span := o.tracer.Start(ctx, "oracle.Crawl",
		trace.WithAttributes(attribute.String("data_source_id", o.ds.ID)))
	defer span.End()

	rows, err := o.db.QueryContext(ctx, `
		SELECT
			c.OWNER,
			c.TABLE_NAME,
			c.COLUMN_NAME,
			c.DATA_TYPE,
			CASE WHEN c.NULLABLE = 'Y' THEN 1 ELSE 0 END AS nullable,
			c.COLUMN_ID,
			c.DATA_DEFAULT,
			tc.COMMENTS AS table_comment,
			cc.COMMENTS AS col_comment
		FROM ALL_TAB_COLUMNS c
		LEFT JOIN ALL_TAB_COMMENTS tc
		  ON tc.OWNER = c.OWNER AND tc.TABLE_NAME = c.TABLE_NAME
		LEFT JOIN ALL_COL_COMMENTS cc
		  ON cc.OWNER = c.OWNER AND cc.TABLE_NAME = c.TABLE_NAME
		 AND cc.COLUMN_NAME = c.COLUMN_NAME
		WHERE c.OWNER NOT IN (
			'SYS','SYSTEM','GOVERNANCE','OUTLN','XDB','APEX_PUBLIC_USER',
			'WMSYS','CTXSYS','DBSNMP','MDSYS','ORDDATA','ORDPLUGINS','ORDSYS',
			'SI_INFORMTN_SCHEMA','SYSMAN','APPQOSSYS','DVSYS','EXFSYS','LBACSYS',
			'OJVMSYS','OLAPSYS','OWBSYS'
		)
		ORDER BY c.OWNER, c.TABLE_NAME, c.COLUMN_ID
	`)
	if err != nil {
		return nil, fmt.Errorf("oracle: crawl query: %w", err)
	}
	defer rows.Close()

	var cols []ColumnInfo
	for rows.Next() {
		var col ColumnInfo
		var nullable int
		var colDefault, tableComment, colComment sql.NullString
		if err := rows.Scan(
			&col.SchemaName, &col.TableName, &col.ColumnName, &col.DataType,
			&nullable, &col.ColumnPosition, &colDefault,
			&tableComment, &colComment,
		); err != nil {
			return nil, fmt.Errorf("oracle: crawl scan: %w", err)
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

// Execute runs a parameterized PL/SQL query.  Uses :1, :2, ... placeholders.
func (o *oracleConnector) Execute(ctx context.Context, q *Query) (ResultStream, error) {
	_, span := o.tracer.Start(ctx, "oracle.Execute",
		trace.WithAttributes(
			attribute.String("data_source_id", o.ds.ID),
			attribute.String("trace_id", q.TraceID),
		))
	defer span.End()

	rows, err := o.db.QueryContext(ctx, q.SQL, q.Args...)
	if err != nil {
		return nil, fmt.Errorf("oracle: execute: %w", err)
	}
	return newSQLResultStream(rows, q.MaxRows)
}

func (o *oracleConnector) Capabilities() map[string]bool {
	return map[string]bool{
		"row_filter":         true,
		"column_mask":        true,
		"native_rls":         true,  // Oracle VPD (DBMS_RLS)
		"ddm":                true,  // Oracle Data Redaction
		"row_access_policy":  false,
		"transactions":       true,
		"information_schema": false, // Oracle uses ALL_*/DBA_* views
	}
}

func (o *oracleConnector) Close() error {
	if o.db != nil {
		return o.db.Close()
	}
	return nil
}

// oracleSafeIdent truncates and upper-cases an identifier to be safe for Oracle
// (max 30 chars for pre-12.2; 128 for 12.2+; we use 30 to be conservative).
func oracleSafeIdent(s string) string {
	s = strings.ToUpper(strings.Map(func(r rune) rune {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			return r
		}
		return '_'
	}, s))
	if len(s) > 30 {
		s = s[:30]
	}
	return s
}
