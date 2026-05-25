// Package connectors defines the engine-agnostic connector abstraction used
// across PEP enforcement, schema crawling, and the native-policy syncer.
//
// Design principles:
//   - No engine-specific code escapes this package boundary.
//   - All DSNs flow in via DataSource.SecretRef → resolved by caller from Vault.
//   - Every Execute call uses parameterized queries; raw string concatenation with
//     user input is prohibited.
//   - EnforceContext must be called once per connection before any query so the
//     engine-native session context carries tenant_id / user_id.
package connectors

import (
	"context"
	"time"
)

// Engine identifies the database engine for a data source.
type Engine string

const (
	EnginePostgres   Engine = "postgres"
	EngineMySQL      Engine = "mysql"
	EngineSQLServer  Engine = "sqlserver"
	EngineOracle     Engine = "oracle"
	EngineSnowflake  Engine = "snowflake"
	EngineBigQuery   Engine = "bigquery"
	EngineDatabricks Engine = "databricks"
	EngineMongoDB    Engine = "mongodb"
)

// ValidEngines is the set of known engines for input validation.
var ValidEngines = map[Engine]struct{}{
	EnginePostgres:   {},
	EngineMySQL:      {},
	EngineSQLServer:  {},
	EngineOracle:     {},
	EngineSnowflake:  {},
	EngineBigQuery:   {},
	EngineDatabricks: {},
	EngineMongoDB:    {},
}

// ColumnInfo holds the raw metadata discovered from any supported database.
// The field set is a superset of all engines; engine-specific fields that are
// not available are left as zero values.
type ColumnInfo struct {
	SchemaName      string
	TableName       string
	ColumnName      string
	DataType        string
	Nullable        bool
	ColumnPosition  int
	ColumnDefault   *string
	TableComment    *string
	ColumnComment   *string
	SampleValues    []string
	FKReferences    []FKRef
	IndexNames      []string
}

// FKRef describes a foreign-key target.
type FKRef struct {
	ToSchema string `json:"to_schema"`
	ToTable  string `json:"to_table"`
	ToColumn string `json:"to_column"`
}

// CatalogDelta is the result of comparing a fresh crawl against the last stored
// snapshot. Used by the schema-crawler differ.
type CatalogDelta struct {
	Added   []ColumnInfo
	Removed []ColumnInfo
	Changed []ColumnInfo
}

// Query is a single read or write query for Execute.
// SQL must use the engine-native parameter placeholder style (e.g. $1 for
// Postgres, ? for MySQL/SQL Server, :1 for Oracle).  The framework does NOT
// transform placeholders — callers must use the correct style.
// Use the helper pkg/connectors/placeholder for portability.
type Query struct {
	SQL     string
	Args    []any
	MaxRows int
	TraceID string
}

// ResultStream is a forward-only cursor over the rows returned by Execute.
// Callers must always call Close, even on error paths.
type ResultStream interface {
	// Next advances the cursor. Returns false on exhaustion or error.
	Next() bool
	// Scan copies the current row into dest (column order).
	Scan(dest ...any) error
	// Columns returns the column names in result order.
	Columns() []string
	// Err returns any iteration error encountered after Next returns false.
	Err() error
	// Close releases underlying resources.
	Close() error
}

// SessionContext carries the identity that must be propagated into every
// engine connection before executing queries.  Values are validated by the
// PDP before being passed here; they are never derived from raw user input.
type SessionContext struct {
	TenantID  string
	UserID    string
	SessionID string
	Roles     []string
}

// DataSource is the resolved descriptor for a target database.
// DSN is populated by the caller from Vault using SecretRef; it must never be
// logged or included in error messages.
type DataSource struct {
	ID        string
	Engine    Engine
	DSN       string  // resolved from Vault; never log
	Database  string
	Schema    string
	SecretRef string  // Vault path used to resolve DSN
}

// NativePolicy describes a single row-filter + column-mask policy to be
// applied as a native database construct (view, RLS predicate, DDM rule, etc.).
type NativePolicy struct {
	// TableSchema is the schema (namespace) that owns the table.
	TableSchema string
	// TableName is the target table or collection.
	TableName string
	// ColumnName is the column subject to masking.  Empty means row-filter only.
	ColumnName string
	// MaskKind specifies the masking transformation.
	// Valid values: "null" | "hash" | "redact" | "partial" | "none".
	MaskKind string
	// RowFilter is a SQL predicate (or engine-equivalent) restricting visible rows.
	// Must be a safe, constant expression — no user input allowed.
	RowFilter string
	// PolicyID is the platform policy UUID for audit correlation.
	PolicyID string
	// PolicyVersion is the policy_set_version tag for idempotency checks.
	PolicyVersion string
}

// SyncResult summarises the outcome of a SyncNativePolicies call.
type SyncResult struct {
	Engine        Engine
	DataSourceID  string
	PoliciesTotal int
	PoliciesOK    int
	PoliciesErr   int
	Skipped       bool   // true when sync_version has not changed
	SyncVersion   string
	Duration      time.Duration
	Errors        []error
}

// Connector is the engine-agnostic interface that every database connector
// must implement.  One Connector instance per DataSource; not goroutine-safe
// unless documented otherwise.
type Connector interface {
	// Engine returns the database engine type.
	Engine() Engine

	// Connect establishes and validates the connection to the target database.
	// Must be called once before any other method.
	Connect(ctx context.Context, ds *DataSource) error

	// Crawl introspects the database schema and returns a CatalogDelta compared
	// to the previous snapshot.  Implementations must use read-only credentials.
	Crawl(ctx context.Context) (*CatalogDelta, error)

	// EnforceContext injects the SessionContext into the current database session
	// using the engine-native mechanism (SET LOCAL, session_context, DBMS_SESSION,
	// ALTER SESSION, etc.).  Must be called per-connection before Execute.
	EnforceContext(ctx context.Context, sc *SessionContext) error

	// PrepareUDFs installs masking UDFs / stored functions the connector requires.
	// Idempotent: safe to call on every deploy.  May be a no-op for engines with
	// native masking (Snowflake DDM, BigQuery policy tags).
	PrepareUDFs(ctx context.Context) error

	// SyncNativePolicies translates platform policies to engine-native constructs
	// (views, RLS predicates, DDM policies, authorized views, etc.) and applies
	// them atomically where the engine supports transactions.
	SyncNativePolicies(ctx context.Context, policies []*NativePolicy) (*SyncResult, error)

	// Execute runs a parameterized query and returns a streaming result cursor.
	// MaxRows in Query bounds the result set; 0 means unlimited.
	// Callers MUST close the ResultStream.
	Execute(ctx context.Context, q *Query) (ResultStream, error)

	// Capabilities returns a map of engine capabilities.
	// Standard keys: "row_filter", "column_mask", "native_rls", "ddm",
	//                "row_access_policy", "transactions", "information_schema".
	Capabilities() map[string]bool

	// Close releases all resources held by the connector (connection pools,
	// gRPC channels, BigQuery clients, etc.).
	Close() error
}
