// Package playbook loads and caches the active risk playbook for a tenant.
package playbook

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

const cacheKeyFmt = "playbook:active:%s"
const cacheTTL = 60 * time.Second

// Tier describes one risk-score band and the action to take.
type Tier struct {
	Min    int            `json:"min"`
	Max    int            `json:"max"`
	Action string         `json:"action"`
	Params map[string]any `json:"params"`
}

// Playbook is the active auto-response configuration for a tenant.
type Playbook struct {
	ID                uuid   `json:"-"`
	TenantID          string `json:"-"`
	Version           int    `json:"version"`
	Tiers             []Tier `json:"tiers"`
	PauseAutoResponse bool   `json:"pause_auto_response"`
}

// uuid is a placeholder for scanning; we just need the string.
type uuid = [16]byte

// EvalResult is the outcome of evaluating a playbook against a risk score.
type EvalResult struct {
	Action  string
	Tier    string
	Params  map[string]any
}

// DefaultPlaybook is the fallback used when no playbook is configured.
var DefaultPlaybook = &Playbook{
	Tiers: []Tier{
		{Min: 0, Max: 40, Action: "normal", Params: map[string]any{}},
		{Min: 41, Max: 70, Action: "tag", Params: map[string]any{}},
		{Min: 71, Max: 85, Action: "step_up", Params: map[string]any{"mfa_window_sec": 300}},
		{Min: 86, Max: 95, Action: "mask", Params: map[string]any{}},
		{Min: 96, Max: 100, Action: "block", Params: map[string]any{}},
	},
}

// Evaluate returns the action for the given risk score.
func (p *Playbook) Evaluate(score int) EvalResult {
	if p.PauseAutoResponse {
		return EvalResult{Action: "normal", Tier: "paused"}
	}
	for _, t := range p.Tiers {
		if score >= t.Min && score <= t.Max {
			tier := tierName(t.Action, score)
			return EvalResult{Action: t.Action, Tier: tier, Params: t.Params}
		}
	}
	return EvalResult{Action: "normal", Tier: "unknown"}
}

func tierName(action string, score int) string {
	switch {
	case score <= 40:
		return "normal"
	case score <= 70:
		return "elevated"
	case score <= 85:
		return "high"
	case score <= 95:
		return "critical"
	default:
		return "extreme"
	}
}

// Store fetches the active playbook for a tenant, with Redis caching.
type Store struct {
	pool *pgxpool.Pool
	rdb  *redis.Client
}

// NewStore creates a Store.
func NewStore(pool *pgxpool.Pool, rdb *redis.Client) *Store {
	return &Store{pool: pool, rdb: rdb}
}

// GetActive returns the active playbook for the tenant, falling back to DefaultPlaybook.
func (s *Store) GetActive(ctx context.Context, tenantID string) (*Playbook, error) {
	key := fmt.Sprintf(cacheKeyFmt, tenantID)

	// L1: Redis cache.
	if s.rdb != nil {
		raw, err := s.rdb.Get(ctx, key).Bytes()
		if err == nil {
			var p Playbook
			if json.Unmarshal(raw, &p) == nil {
				return &p, nil
			}
		}
	}

	// L2: PostgreSQL.
	p, err := s.loadFromDB(ctx, tenantID)
	if err != nil {
		return DefaultPlaybook, nil
	}

	// Populate cache.
	if s.rdb != nil {
		if b, err := json.Marshal(p); err == nil {
			s.rdb.Set(ctx, key, b, cacheTTL)
		}
	}
	return p, nil
}

// Invalidate flushes the cached playbook for a tenant.
func (s *Store) Invalidate(ctx context.Context, tenantID string) {
	if s.rdb != nil {
		s.rdb.Del(ctx, fmt.Sprintf(cacheKeyFmt, tenantID))
	}
}

func (s *Store) loadFromDB(ctx context.Context, tenantID string) (*Playbook, error) {
	const q = `
		SELECT tiers, pause_auto_response, version
		FROM risk_playbooks
		WHERE tenant_id = $1 AND active = true
		ORDER BY version DESC LIMIT 1`

	var tiersJSON []byte
	var p Playbook
	err := s.pool.QueryRow(ctx, q, tenantID).Scan(&tiersJSON, &p.PauseAutoResponse, &p.Version)
	if err != nil {
		return nil, fmt.Errorf("playbook: load from DB: %w", err)
	}
	if err := json.Unmarshal(tiersJSON, &p.Tiers); err != nil {
		return nil, fmt.Errorf("playbook: unmarshal tiers: %w", err)
	}
	p.TenantID = tenantID
	return &p, nil
}
