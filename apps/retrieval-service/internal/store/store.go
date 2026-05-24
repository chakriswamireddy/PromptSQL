// Package store handles all retrieval-service database queries under SET LOCAL discipline.
package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SessionCtx carries the per-request tenant/user context for SET LOCAL.
type SessionCtx struct {
	TenantID  string
	UserID    string
	UserRoles []string
	// Arbitrary subject attributes, e.g. {"department":"eng"}.
	SubjectAttrs map[string]string
}

type Store struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// withSession acquires a connection, applies SET LOCAL session variables, and
// passes it to fn. The connection is returned to the pool after fn returns.
func (s *Store) withSession(ctx context.Context, sess SessionCtx, fn func(pgx.Tx) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	roleSQL := `SELECT set_config('app.tenant_id',$1,true),
	                   set_config('app.user_id',$2,true),
	                   set_config('app.user_roles',$3,true)`
	if _, err := tx.Exec(ctx, roleSQL,
		sess.TenantID,
		sess.UserID,
		encodeRoles(sess.UserRoles),
	); err != nil {
		return fmt.Errorf("set session config: %w", err)
	}

	// Forward arbitrary subject attributes so RLS and ACL queries can use them.
	for k, v := range sess.SubjectAttrs {
		if _, err := tx.Exec(ctx,
			`SELECT set_config($1,$2,true)`,
			"app.subject."+k, v,
		); err != nil {
			return fmt.Errorf("set subject attr %q: %w", k, err)
		}
	}

	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func encodeRoles(roles []string) string {
	if len(roles) == 0 {
		return "{}"
	}
	out := "{"
	for i, r := range roles {
		if i > 0 {
			out += ","
		}
		out += r
	}
	return out + "}"
}

// ── Schema metadata ───────────────────────────────────────────────────────────

type TableMeta struct {
	SchemaName  string
	TableName   string
	Description string
	Columns     []ColumnMeta
}

type ColumnMeta struct {
	ColumnID       string
	ColumnName     string
	DataType       string
	Nullable       bool
	Classification string // public|internal|confidential|restricted
	MaskRule       string // mask function name, or ""
	SampleValues   []string
	Description    string
	FKRefs         []FKRef
}

type FKRef struct {
	ToSchema string
	ToTable  string
	ToColumn string
}

// ListTableColumns returns all non-quarantined columns for dataSourceID ordered
// by (schema, table, column_position).
func (s *Store) ListTableColumns(ctx context.Context, sess SessionCtx, dataSourceID string) ([]ColumnMeta, error) {
	var results []ColumnMeta
	err := s.withSession(ctx, sess, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT
			  sm.id,
			  sm.schema_name,
			  sm.table_name,
			  sm.column_name,
			  sm.data_type,
			  sm.nullable,
			  COALESCE(dc.level,'public')    AS classification,
			  COALESCE(cm.mask_rule,'')       AS mask_rule,
			  sm.sample_values,
			  COALESCE(sm.column_comment,'') AS description,
			  sm.fk_references
			FROM schema_metadata sm
			LEFT JOIN data_classifications dc ON dc.column_id = sm.id
			LEFT JOIN column_masks cm         ON cm.column_id = sm.id
			WHERE sm.tenant_id     = current_setting('app.tenant_id')::uuid
			  AND sm.data_source_id = $1::uuid
			  AND sm.quarantine    = false
			  AND sm.dropped_at   IS NULL
			ORDER BY sm.schema_name, sm.table_name, sm.column_position`,
			dataSourceID,
		)
		if err != nil {
			return fmt.Errorf("list columns: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var col ColumnMeta
			var fkJSON []byte
			if err := rows.Scan(
				&col.ColumnID,
				new(string), // schema_name (used by grouping below)
				new(string), // table_name
				&col.ColumnName,
				&col.DataType,
				&col.Nullable,
				&col.Classification,
				&col.MaskRule,
				&col.SampleValues,
				&col.Description,
				&fkJSON,
			); err != nil {
				return fmt.Errorf("scan column: %w", err)
			}
			results = append(results, col)
		}
		return rows.Err()
	})
	return results, err
}

// ListTablesWithColumns returns tables grouped with their columns, for the
// snapshot builder.
func (s *Store) ListTablesWithColumns(ctx context.Context, sess SessionCtx, dataSourceID string) ([]TableMeta, error) {
	type rawRow struct {
		schemaName     string
		tableName      string
		tableDesc      string
		colID          string
		colName        string
		dataType       string
		nullable       bool
		classification string
		maskRule       string
		sampleValues   []string
		colDesc        string
		fkRefsJSON     []byte
	}

	var rows []rawRow
	err := s.withSession(ctx, sess, func(tx pgx.Tx) error {
		pgRows, err := tx.Query(ctx, `
			SELECT
			  sm.schema_name,
			  sm.table_name,
			  COALESCE(sm.table_comment,'')  AS table_desc,
			  sm.id                          AS col_id,
			  sm.column_name,
			  sm.data_type,
			  sm.nullable,
			  COALESCE(dc.level,'public')    AS classification,
			  COALESCE(cm.mask_rule,'')      AS mask_rule,
			  sm.sample_values,
			  COALESCE(sm.column_comment,'') AS col_desc,
			  sm.fk_references
			FROM schema_metadata sm
			LEFT JOIN data_classifications dc ON dc.column_id = sm.id
			LEFT JOIN column_masks cm         ON cm.column_id = sm.id
			WHERE sm.tenant_id      = current_setting('app.tenant_id')::uuid
			  AND sm.data_source_id = $1::uuid
			  AND sm.quarantine     = false
			  AND sm.dropped_at    IS NULL
			ORDER BY sm.schema_name, sm.table_name, sm.column_position`,
			dataSourceID,
		)
		if err != nil {
			return fmt.Errorf("list tables: %w", err)
		}
		defer pgRows.Close()
		for pgRows.Next() {
			var r rawRow
			if err := pgRows.Scan(
				&r.schemaName, &r.tableName, &r.tableDesc,
				&r.colID, &r.colName, &r.dataType, &r.nullable,
				&r.classification, &r.maskRule,
				&r.sampleValues, &r.colDesc, &r.fkRefsJSON,
			); err != nil {
				return fmt.Errorf("scan: %w", err)
			}
			rows = append(rows, r)
		}
		return pgRows.Err()
	})
	if err != nil {
		return nil, err
	}

	// Group into tables.
	tableIndex := map[string]int{}
	var tables []TableMeta
	for _, r := range rows {
		key := r.schemaName + "." + r.tableName
		idx, ok := tableIndex[key]
		if !ok {
			idx = len(tables)
			tableIndex[key] = idx
			tables = append(tables, TableMeta{
				SchemaName:  r.schemaName,
				TableName:   r.tableName,
				Description: r.tableDesc,
			})
		}
		tables[idx].Columns = append(tables[idx].Columns, ColumnMeta{
			ColumnID:       r.colID,
			ColumnName:     r.colName,
			DataType:       r.dataType,
			Nullable:       r.nullable,
			Classification: r.classification,
			MaskRule:       r.maskRule,
			SampleValues:   r.sampleValues,
			Description:    r.colDesc,
		})
	}
	return tables, nil
}

// ── Doc retrieval ─────────────────────────────────────────────────────────────

type DocChunk struct {
	ID             string
	CorpusID       string
	ChunkText      string
	Classification string
	Similarity     float64
	Metadata       []byte
}

// FindSimilarChunks runs pgvector cosine similarity with ACL filter.
// queryEmbedding is a []float32 serialised as a PG vector literal.
func (s *Store) FindSimilarChunks(
	ctx context.Context,
	sess SessionCtx,
	dataSourceIDs []string,
	queryVec []float32,
	topK int,
	minSimilarity float64,
) ([]DocChunk, error) {
	var chunks []DocChunk
	err := s.withSession(ctx, sess, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT
			  id,
			  corpus_id::text,
			  chunk_text,
			  classification,
			  1 - (embedding <=> $1::vector)  AS similarity,
			  COALESCE(metadata,'{}')
			FROM doc_chunks
			WHERE tenant_id = current_setting('app.tenant_id')::uuid
			  AND quarantine = false
			  AND (
			    acl_users @> ARRAY[current_setting('app.user_id',true)::uuid]
			    OR acl_roles && current_setting('app.user_roles',true)::text[]
			    OR acl_users = '{}'
			  )
			  AND ($2::uuid[] IS NULL OR corpus_id = ANY($2::uuid[]))
			  AND 1 - (embedding <=> $1::vector) >= $3
			ORDER BY embedding <=> $1::vector
			LIMIT $4`,
			vecLiteral(queryVec),
			dataSourceIDs,
			minSimilarity,
			topK,
		)
		if err != nil {
			return fmt.Errorf("pgvector query: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var c DocChunk
			if err := rows.Scan(&c.ID, &c.CorpusID, &c.ChunkText, &c.Classification, &c.Similarity, &c.Metadata); err != nil {
				return fmt.Errorf("scan chunk: %w", err)
			}
			chunks = append(chunks, c)
		}
		return rows.Err()
	})
	return chunks, err
}

// vecLiteral converts a float32 slice to a PostgreSQL vector literal.
func vecLiteral(v []float32) string {
	if len(v) == 0 {
		return "[]"
	}
	s := "["
	for i, f := range v {
		if i > 0 {
			s += ","
		}
		s += fmt.Sprintf("%g", f)
	}
	return s + "]"
}

// ── LLM provider routing ──────────────────────────────────────────────────────

type ProviderRoute struct {
	ProviderName    string
	Model           string
	Priority        int
	ZeroRetention   bool
	PrivateOnly     bool
	ResidencyRegion string
}

// GetProviderRoutes returns routes for a tenant+classification ordered by priority.
func (s *Store) GetProviderRoutes(ctx context.Context, sess SessionCtx, classification string) ([]ProviderRoute, error) {
	var routes []ProviderRoute
	err := s.withSession(ctx, sess, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT provider_name, model, priority, zero_retention, private_only,
			       COALESCE(residency_region,'')
			  FROM llm_provider_routes
			 WHERE tenant_id     = current_setting('app.tenant_id')::uuid
			   AND classification = $1
			 ORDER BY priority ASC`,
			classification,
		)
		if err != nil {
			return fmt.Errorf("get provider routes: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var r ProviderRoute
			if err := rows.Scan(&r.ProviderName, &r.Model, &r.Priority,
				&r.ZeroRetention, &r.PrivateOnly, &r.ResidencyRegion,
			); err != nil {
				return fmt.Errorf("scan route: %w", err)
			}
			routes = append(routes, r)
		}
		return rows.Err()
	})
	return routes, err
}

// ── Denylist ──────────────────────────────────────────────────────────────────

// ListDenyPhrases returns per-tenant (and optionally per-corpus) denylist phrases.
func (s *Store) ListDenyPhrases(ctx context.Context, sess SessionCtx, corpusID string) ([]string, error) {
	var phrases []string
	err := s.withSession(ctx, sess, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT phrase FROM tenant_denylist
			 WHERE tenant_id = current_setting('app.tenant_id')::uuid
			   AND (corpus_id IS NULL OR corpus_id = $1::uuid)`,
			corpusID,
		)
		if err != nil {
			return fmt.Errorf("list deny phrases: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				return err
			}
			phrases = append(phrases, p)
		}
		return rows.Err()
	})
	return phrases, err
}

// ── Quarantine sweeper ────────────────────────────────────────────────────────

func (s *Store) ReleaseQuarantinedChunks(ctx context.Context) (int, error) {
	var released int
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := tx.QueryRow(ctx, `SELECT release_quarantined_chunks(NULL)`).Scan(&released); err != nil {
		return 0, fmt.Errorf("release quarantine: %w", err)
	}
	return released, tx.Commit(ctx)
}

// ── Schema version ────────────────────────────────────────────────────────────

type DataSourceVersion struct {
	SchemaVersion    string
	PolicySetVersion string
}

func (s *Store) GetDataSourceVersion(ctx context.Context, sess SessionCtx, dataSourceID string) (DataSourceVersion, error) {
	var v DataSourceVersion
	err := s.withSession(ctx, sess, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT
			  COALESCE(
			    (SELECT MAX(id::text) FROM crawl_runs
			      WHERE data_source_id = $1::uuid AND status = 'success'),
			    'none'
			  ) AS schema_version,
			  COALESCE(
			    (SELECT MAX(version::text) FROM policy_set_versions
			      WHERE tenant_id = current_setting('app.tenant_id')::uuid),
			    'none'
			  ) AS policy_set_version`,
			dataSourceID,
		).Scan(&v.SchemaVersion, &v.PolicySetVersion)
	})
	return v, err
}
