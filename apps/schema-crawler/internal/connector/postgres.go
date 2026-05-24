// Package connector provides read-only introspection of PostgreSQL databases.
package connector

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ColumnInfo holds the raw metadata discovered from a target database.
type ColumnInfo struct {
	SchemaName     string
	TableName      string
	ColumnName     string
	DataType       string
	Nullable       bool
	ColumnPosition int
	ColumnDefault  *string
	TableComment   *string
	ColumnComment  *string
	SampleValues   []string
	FKReferences   []FKRef
	IndexNames     []string
}

// FKRef describes a foreign key target.
type FKRef struct {
	ToSchema string `json:"to_schema"`
	ToTable  string `json:"to_table"`
	ToColumn string `json:"to_column"`
}

// Postgres introspects a remote PostgreSQL instance via information_schema.
// conn must be a read-only connection obtained from Vault credentials.
type Postgres struct {
	pool         *pgxpool.Pool
	sampleMaxRows int
}

// NewPostgres creates a Postgres connector using the given DSN.
func NewPostgres(ctx context.Context, dsn string, sampleMaxRows int) (*Postgres, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}
	// Use repeatable-read to avoid races with active DDL.
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeCacheDescribe

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	return &Postgres{pool: pool, sampleMaxRows: sampleMaxRows}, nil
}

// Close releases the connection pool.
func (p *Postgres) Close() { p.pool.Close() }

// Introspect returns all column metadata for all non-system schemas.
func (p *Postgres) Introspect(ctx context.Context) ([]ColumnInfo, error) {
	cols, err := p.fetchColumns(ctx)
	if err != nil {
		return nil, err
	}

	fkMap, err := p.fetchForeignKeys(ctx)
	if err != nil {
		return nil, err
	}

	idxMap, err := p.fetchIndexes(ctx)
	if err != nil {
		return nil, err
	}

	for i := range cols {
		key := cols[i].SchemaName + "." + cols[i].TableName + "." + cols[i].ColumnName
		cols[i].FKReferences = fkMap[key]
		tableKey := cols[i].SchemaName + "." + cols[i].TableName
		cols[i].IndexNames = idxMap[tableKey]
	}

	return cols, nil
}

func (p *Postgres) fetchColumns(ctx context.Context) ([]ColumnInfo, error) {
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
		WHERE c.table_schema NOT IN ('information_schema', 'pg_catalog', 'pg_toast')
		  AND pc.relkind = 'r'
		ORDER BY c.table_schema, c.table_name, c.ordinal_position
	`)
	if err != nil {
		return nil, fmt.Errorf("fetch columns: %w", err)
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
			return nil, err
		}
		out = append(out, col)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return out, nil
}

func (p *Postgres) fetchForeignKeys(ctx context.Context) (map[string][]FKRef, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT
			kcu.table_schema  AS from_schema,
			kcu.table_name    AS from_table,
			kcu.column_name   AS from_column,
			ccu.table_schema  AS to_schema,
			ccu.table_name    AS to_table,
			ccu.column_name   AS to_column
		FROM information_schema.table_constraints tc
		JOIN information_schema.key_column_usage kcu
		  ON kcu.constraint_name = tc.constraint_name
		 AND kcu.table_schema = tc.table_schema
		JOIN information_schema.constraint_column_usage ccu
		  ON ccu.constraint_name = tc.constraint_name
		WHERE tc.constraint_type = 'FOREIGN KEY'
		  AND tc.table_schema NOT IN ('information_schema','pg_catalog')
	`)
	if err != nil {
		return nil, fmt.Errorf("fetch foreign keys: %w", err)
	}
	defer rows.Close()

	out := make(map[string][]FKRef)
	for rows.Next() {
		var fromSchema, fromTable, fromCol, toSchema, toTable, toCol string
		if err := rows.Scan(&fromSchema, &fromTable, &fromCol, &toSchema, &toTable, &toCol); err != nil {
			return nil, err
		}
		key := fromSchema + "." + fromTable + "." + fromCol
		out[key] = append(out[key], FKRef{ToSchema: toSchema, ToTable: toTable, ToColumn: toCol})
	}
	return out, rows.Err()
}

func (p *Postgres) fetchIndexes(ctx context.Context) (map[string][]string, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT
			n.nspname  AS schema_name,
			t.relname  AS table_name,
			i.relname  AS index_name
		FROM pg_catalog.pg_index ix
		JOIN pg_catalog.pg_class t ON t.oid = ix.indrelid
		JOIN pg_catalog.pg_class i ON i.oid = ix.indexrelid
		JOIN pg_catalog.pg_namespace n ON n.oid = t.relnamespace
		WHERE t.relkind = 'r'
		  AND n.nspname NOT IN ('information_schema','pg_catalog','pg_toast')
	`)
	if err != nil {
		return nil, fmt.Errorf("fetch indexes: %w", err)
	}
	defer rows.Close()

	out := make(map[string][]string)
	for rows.Next() {
		var schema, table, index string
		if err := rows.Scan(&schema, &table, &index); err != nil {
			return nil, err
		}
		key := schema + "." + table
		out[key] = append(out[key], index)
	}
	return out, rows.Err()
}

// SampleColumn returns up to maxRows distinct non-null values for a column.
// Must only be called for columns whose classification is public or internal.
// The query uses a read-only repeatable-read snapshot to avoid dirty reads.
func (p *Postgres) SampleColumn(ctx context.Context, schema, table, column string, maxRows int) ([]string, error) {
	// Use parameterized quoted identifiers to prevent SQL injection.
	query := fmt.Sprintf(
		`SELECT DISTINCT CAST(%s AS text) FROM %s.%s WHERE %s IS NOT NULL LIMIT %d`,
		pgx.Identifier{column}.Sanitize(),
		pgx.Identifier{schema}.Sanitize(),
		pgx.Identifier{table}.Sanitize(),
		pgx.Identifier{column}.Sanitize(),
		maxRows,
	)

	rows, err := p.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("sample %s.%s.%s: %w", schema, table, column, err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// FKRefsToJSON serialises FK references for storage.
func FKRefsToJSON(refs []FKRef) ([]byte, error) {
	if len(refs) == 0 {
		return []byte("[]"), nil
	}
	return json.Marshal(refs)
}
