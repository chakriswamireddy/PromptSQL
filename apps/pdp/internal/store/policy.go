// Package store queries active policies from PostgreSQL for the PDP.
// All queries go through pkg/db.WithSession to enforce SET LOCAL discipline.
package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"
	pkgdb "github.com/governance-platform/pkg/db"
	"github.com/governance-platform/policy-engine/dsl"
	"github.com/governance-platform/policy-engine/engine"
)

// Store reads policies from the PostgreSQL control-plane database.
type Store struct {
	db *pkgdb.Pool
}

// New creates a Store wrapping the given connection pool.
func New(db *pkgdb.Pool) *Store { return &Store{db: db} }

// systemSession satisfies pkg/db.SessionData using the internal PDP service identity.
// The PDP reads with app_read; it never writes policies.
type systemSession struct {
	tenantID string
}

func (s *systemSession) GetUserID() string    { return "pdp-service" }
func (s *systemSession) GetTenantID() string  { return s.tenantID }
func (s *systemSession) GetSessionID() string { return "pdp-system" }
func (s *systemSession) GetRequestID() string { return "" }
func (s *systemSession) GetTraceID() string   { return "" }
func (s *systemSession) GetBreakGlass() bool  { return false }

// LoadActivePolicies fetches all active policies for tenantID and compiles their DSL closures.
// Broken closures are skipped (not deny-all) and logged via the returned error slice.
func (s *Store) LoadActivePolicies(ctx context.Context, tenantID string) ([]engine.Policy, []error, error) {
	var rows []policyRow
	sd := &systemSession{tenantID: tenantID}
	err := s.db.WithSession(ctx, sd, func(tx pgx.Tx) error {
		sql := `
SELECT id, tenant_id, name, version, effect, action,
       resource_match, conditions, obligations,
       allowed_columns, denied_columns, row_filter,
       jsonb_object_agg(COALESCE(cm.col,''), COALESCE(cm.mask,'')) FILTER (WHERE cm.col IS NOT NULL) AS column_masks
FROM policies p
LEFT JOIN LATERAL (
  SELECT jsonb_object_keys(p.conditions) AS col,
         p.conditions->>jsonb_object_keys(p.conditions) AS mask
) cm ON false
WHERE p.tenant_id = $1 AND p.status = 'active'
GROUP BY p.id
ORDER BY p.created_at`
		// Simpler query without the lateral join for column_masks (stored in separate field):
		sql = `
SELECT id, tenant_id, name, version, effect, action,
       resource_match, conditions, obligations,
       allowed_columns, denied_columns, row_filter
FROM policies
WHERE tenant_id = $1 AND status = 'active'
ORDER BY created_at`
		pgrows, err := tx.Query(ctx, sql, tenantID)
		if err != nil {
			return fmt.Errorf("store: load policies: %w", err)
		}
		defer pgrows.Close()
		for pgrows.Next() {
			var r policyRow
			if err := pgrows.Scan(
				&r.id, &r.tenantID, &r.name, &r.version,
				&r.effect, &r.action,
				&r.resourceMatch, &r.conditions, &r.obligations,
				&r.allowedColumns, &r.deniedColumns, &r.rowFilter,
			); err != nil {
				return fmt.Errorf("store: scan: %w", err)
			}
			rows = append(rows, r)
		}
		return pgrows.Err()
	})
	if err != nil {
		return nil, nil, err
	}
	return compilePolicies(rows)
}

// PolicySetVersion returns the current policy_set_version for a tenant.
func (s *Store) PolicySetVersion(ctx context.Context, tenantID string) (int64, error) {
	var version int64
	sd := &systemSession{tenantID: tenantID}
	err := s.db.WithSession(ctx, sd, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx,
			"SELECT COALESCE(version, 0) FROM policy_set_versions WHERE tenant_id = $1",
			tenantID,
		)
		return row.Scan(&version)
	})
	if err != nil {
		return 0, fmt.Errorf("store: policy_set_version: %w", err)
	}
	return version, nil
}

// policyRow is the raw database row before DSL compilation.
type policyRow struct {
	id             string
	tenantID       string
	name           string
	version        int
	effect         string
	action         string
	resourceMatch  []byte // JSON
	conditions     []byte // JSON
	obligations    []byte // JSON
	allowedColumns []string
	deniedColumns  []string
	rowFilter      []byte // JSON
}

func compilePolicies(rows []policyRow) ([]engine.Policy, []error, error) {
	policies := make([]engine.Policy, 0, len(rows))
	var errs []error
	for _, r := range rows {
		p, compErr := compileRow(r)
		if compErr != nil {
			errs = append(errs, fmt.Errorf("policy %s: %w", r.id, compErr))
			continue // skip broken policy; others continue
		}
		policies = append(policies, *p)
	}
	return policies, errs, nil
}

func compileRow(r policyRow) (p *engine.Policy, retErr error) {
	defer func() {
		if rec := recover(); rec != nil {
			retErr = fmt.Errorf("panic during compile: %v", rec)
		}
	}()

	p = &engine.Policy{
		ID:             r.id,
		TenantID:       r.tenantID,
		Name:           r.name,
		Version:        r.version,
		Effect:         r.effect,
		Action:         r.action,
		AllowedColumns: r.allowedColumns,
		DeniedColumns:  r.deniedColumns,
	}

	// Extract resource prefix from resource_match JSON.
	if len(r.resourceMatch) > 0 {
		var rm map[string]interface{}
		if err := json.Unmarshal(r.resourceMatch, &rm); err == nil {
			if prefix, ok := rm["prefix"].(string); ok {
				p.ResourcePrefix = prefix
			}
			if action, ok := rm["action"].(string); ok && p.Action == "" {
				p.Action = action
			}
		}
	}

	// Compile conditions DSL.
	if len(r.conditions) > 0 && string(r.conditions) != "null" {
		node, err := dsl.Parse(r.conditions)
		if err != nil {
			return nil, fmt.Errorf("parse conditions: %w", err)
		}
		if err := dsl.Validate(node); err != nil {
			return nil, fmt.Errorf("validate conditions: %w", err)
		}
		fn, err := dsl.Compile(node)
		if err != nil {
			return nil, fmt.Errorf("compile conditions: %w", err)
		}
		p.ConditionsNode = node
		p.CompiledConditions = fn
	}

	// Compile row filter DSL.
	if len(r.rowFilter) > 0 && string(r.rowFilter) != "null" {
		node, err := dsl.Parse(r.rowFilter)
		if err != nil {
			return nil, fmt.Errorf("parse row_filter: %w", err)
		}
		if err := dsl.Validate(node); err != nil {
			return nil, fmt.Errorf("validate row_filter: %w", err)
		}
		fn, err := dsl.Compile(node)
		if err != nil {
			return nil, fmt.Errorf("compile row_filter: %w", err)
		}
		p.RowFilter = node
		p.CompiledRowFilter = fn
	}

	// Parse obligations.
	if len(r.obligations) > 0 && string(r.obligations) != "null" {
		var obs []struct {
			Type   string            `json:"type"`
			Params map[string]string `json:"params"`
		}
		if err := json.Unmarshal(r.obligations, &obs); err == nil {
			for _, ob := range obs {
				p.Obligations = append(p.Obligations, engine.Obligation{
					Type:   ob.Type,
					Params: ob.Params,
				})
			}
		}
	}

	return p, nil
}

// VersionKey formats the policy set version as a string for cache keys.
func VersionKey(version int64) string {
	return strconv.FormatInt(version, 10)
}
