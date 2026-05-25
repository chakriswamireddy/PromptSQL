// Package equivalence provides a semantic equivalence test harness for
// verifying that a platform NativePolicy produces identical visible result
// sets across different database engines.
//
// Usage in integration tests:
//
//	canon := &equivalence.CanonicalPolicy{...}
//	sql, err := equivalence.RewriteForEngine(canon, connectors.EngineMySQL)
//	err = equivalence.CompareRowsets(connectors.EngineMySQL, sql, expected, actual)
package equivalence

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/governance-platform/pkg/connectors"
)

// CanonicalPolicy is a platform-normalised policy description used as the
// source of truth when comparing engine-specific rewrites.
type CanonicalPolicy struct {
	// ID is the platform policy UUID.
	ID string
	// TableSchema / TableName identify the target relation.
	TableSchema string
	TableName   string
	// ColumnMasks maps column name to masking kind.
	// Keys: "null" | "hash" | "redact" | "partial"
	ColumnMasks map[string]string
	// RowFilter is a SQL predicate in ANSI SQL that restricts visible rows.
	// It must not reference any engine-specific functions.
	RowFilter string
	// Roles lists the roles this policy applies to.
	Roles []string
}

// RewriteForEngine translates a CanonicalPolicy into an engine-specific SQL
// fragment (typically a SELECT statement for the view or filter function body).
//
// The returned SQL is suitable for use in connector SyncNativePolicies calls
// and in equivalence integration tests.
func RewriteForEngine(policy *CanonicalPolicy, engine connectors.Engine) (string, error) {
	if policy == nil {
		return "", fmt.Errorf("equivalence: nil policy")
	}

	switch engine {
	case connectors.EnginePostgres:
		return rewritePostgres(policy), nil
	case connectors.EngineMySQL:
		return rewriteMySQL(policy), nil
	case connectors.EngineSQLServer:
		return rewriteSQLServer(policy), nil
	case connectors.EngineOracle:
		return rewriteOracle(policy), nil
	case connectors.EngineSnowflake:
		return rewriteSnowflake(policy), nil
	case connectors.EngineBigQuery:
		return rewriteBigQuery(policy), nil
	case connectors.EngineDatabricks:
		return rewriteDatabricks(policy), nil
	case connectors.EngineMongoDB:
		return rewriteMongoDB(policy), nil
	default:
		return "", fmt.Errorf("equivalence: unknown engine %q", engine)
	}
}

func rewritePostgres(p *CanonicalPolicy) string {
	projection := buildPostgresProjection(p.ColumnMasks)
	where := ""
	if p.RowFilter != "" {
		where = " WHERE " + p.RowFilter
	}
	return fmt.Sprintf(
		`CREATE OR REPLACE VIEW governance.gov_%s_%s AS SELECT %s FROM "%s"."%s"%s`,
		sanitize(p.TableSchema), sanitize(p.TableName),
		projection,
		p.TableSchema, p.TableName, where,
	)
}

func buildPostgresProjection(masks map[string]string) string {
	if len(masks) == 0 {
		return "*"
	}
	parts := []string{"*"}
	for col, kind := range masks {
		fn := pgMaskFn(kind)
		parts = append(parts, fmt.Sprintf(`governance.%s("%s") AS "%s"`, fn, col, col))
	}
	return joinProjection(parts)
}

func pgMaskFn(kind string) string {
	switch kind {
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

func rewriteMySQL(p *CanonicalPolicy) string {
	projection := buildMySQLProjection(p.ColumnMasks)
	where := ""
	if p.RowFilter != "" {
		where = " WHERE " + p.RowFilter
	}
	return fmt.Sprintf(
		"CREATE OR REPLACE VIEW `governance`.`gov_%s_%s` AS SELECT %s FROM `%s`.`%s`%s",
		sanitize(p.TableSchema), sanitize(p.TableName),
		projection,
		p.TableSchema, p.TableName, where,
	)
}

func buildMySQLProjection(masks map[string]string) string {
	if len(masks) == 0 {
		return "*"
	}
	parts := []string{"t.*"}
	for col, kind := range masks {
		fn := mysqlMaskFnName(kind)
		parts = append(parts, fmt.Sprintf("governance.%s(t.`%s`) AS `%s`", fn, col, col))
	}
	return joinProjection(parts)
}

func mysqlMaskFnName(kind string) string {
	switch kind {
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

func rewriteSQLServer(p *CanonicalPolicy) string {
	where := ""
	if p.RowFilter != "" {
		where = " WHERE " + p.RowFilter
	}
	return fmt.Sprintf(
		"CREATE OR ALTER VIEW [governance].[gov_%s_%s] AS SELECT * FROM [%s].[%s]%s",
		sanitize(p.TableSchema), sanitize(p.TableName),
		p.TableSchema, p.TableName, where,
	)
}

func rewriteOracle(p *CanonicalPolicy) string {
	where := ""
	if p.RowFilter != "" {
		where = " WHERE " + p.RowFilter
	}
	return fmt.Sprintf(
		`CREATE OR REPLACE VIEW GOVERNANCE.GOV_%s_%s AS SELECT * FROM "%s"."%s"%s`,
		sanitize(p.TableSchema), sanitize(p.TableName),
		p.TableSchema, p.TableName, where,
	)
}

func rewriteSnowflake(p *CanonicalPolicy) string {
	where := ""
	if p.RowFilter != "" {
		where = " WHERE " + p.RowFilter
	}
	return fmt.Sprintf(
		"CREATE OR REPLACE VIEW GOVERNANCE.GOV_%s_%s AS SELECT * FROM %s.%s%s",
		sanitize(p.TableSchema), sanitize(p.TableName),
		p.TableSchema, p.TableName, where,
	)
}

func rewriteBigQuery(p *CanonicalPolicy) string {
	where := ""
	if p.RowFilter != "" {
		where = " WHERE " + p.RowFilter
	}
	return fmt.Sprintf(
		"SELECT * FROM `%s.%s`%s",
		p.TableSchema, p.TableName, where,
	)
}

func rewriteDatabricks(p *CanonicalPolicy) string {
	where := ""
	if p.RowFilter != "" {
		where = " WHERE " + p.RowFilter
	}
	return fmt.Sprintf(
		"CREATE OR REPLACE VIEW governance.gov_%s_%s AS SELECT * FROM `%s`.`%s`%s",
		sanitize(p.TableSchema), sanitize(p.TableName),
		p.TableSchema, p.TableName, where,
	)
}

func rewriteMongoDB(p *CanonicalPolicy) string {
	// MongoDB rewrites are expressed as aggregation pipeline JSON.
	stages := []map[string]any{}
	if p.RowFilter != "" {
		// Wrap as a $comment noting the ANSI filter (MongoDB uses its own syntax).
		stages = append(stages, map[string]any{
			"$match": map[string]any{"$comment": p.RowFilter},
		})
	}
	if len(p.ColumnMasks) > 0 {
		proj := map[string]any{}
		for col, kind := range p.ColumnMasks {
			if kind == "null" {
				proj[col] = 0
			} else {
				proj[col] = "[MASKED]" // placeholder
			}
		}
		stages = append(stages, map[string]any{"$project": proj})
	}
	b, _ := json.Marshal(stages)
	return string(b)
}

// CompareRowsets checks that two rowsets (expected, actual) contain the same
// rows regardless of order.  Each row is a map[string]any with string-typed keys.
//
// Returns nil if the rowsets are semantically equivalent, or a descriptive
// error listing the first discrepancy found.
func CompareRowsets(engine connectors.Engine, query string, expected, actual []map[string]any) error {
	if len(expected) != len(actual) {
		return fmt.Errorf(
			"equivalence[%s] rowset size mismatch: expected %d rows, got %d (query: %s)",
			engine, len(expected), len(actual), query,
		)
	}

	// Normalise to sorted JSON strings for order-independent comparison.
	expNorm, err := normaliseRowset(expected)
	if err != nil {
		return fmt.Errorf("equivalence[%s] normalise expected: %w", engine, err)
	}
	actNorm, err := normaliseRowset(actual)
	if err != nil {
		return fmt.Errorf("equivalence[%s] normalise actual: %w", engine, err)
	}

	sort.Strings(expNorm)
	sort.Strings(actNorm)

	for i := range expNorm {
		if expNorm[i] != actNorm[i] {
			return fmt.Errorf(
				"equivalence[%s] row %d mismatch:\n  expected: %s\n  actual:   %s",
				engine, i, expNorm[i], actNorm[i],
			)
		}
	}
	return nil
}

// normaliseRowset serialises each row to a canonical JSON string for comparison.
func normaliseRowset(rows []map[string]any) ([]string, error) {
	out := make([]string, len(rows))
	for i, row := range rows {
		// Sort keys for determinism.
		b, err := json.Marshal(sortedMap(row))
		if err != nil {
			return nil, err
		}
		out[i] = string(b)
	}
	return out, nil
}

// sortedMap returns a slice of key-value pairs sorted by key, producing
// deterministic JSON output.
func sortedMap(m map[string]any) []any {
	type kv struct {
		K string
		V any
	}
	pairs := make([]kv, 0, len(m))
	for k, v := range m {
		pairs = append(pairs, kv{K: k, V: v})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].K < pairs[j].K })
	// Return as []interface{} so json.Marshal preserves order.
	result := make([]any, 0, len(pairs)*2)
	for _, p := range pairs {
		result = append(result, p.K, p.V)
	}
	return result
}

// joinProjection joins column expressions for a SELECT list, deduplicating
// when both wildcard and named columns are present.
func joinProjection(parts []string) string {
	if len(parts) == 0 {
		return "*"
	}
	// Keep only the first occurrence of any column name.
	seen := make(map[string]struct{})
	var out []string
	for _, p := range parts {
		if _, ok := seen[p]; !ok {
			seen[p] = struct{}{}
			out = append(out, p)
		}
	}
	result := ""
	for i, p := range out {
		if i > 0 {
			result += ", "
		}
		result += p
	}
	return result
}

func sanitize(s string) string {
	result := make([]byte, 0, len(s))
	for _, c := range []byte(s) {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' {
			result = append(result, c)
		} else {
			result = append(result, '_')
		}
	}
	if len(result) > 50 {
		result = result[:50]
	}
	return string(result)
}
