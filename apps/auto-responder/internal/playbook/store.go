// Package playbook manages risk-response playbook CRUD in the auto-responder service.
package playbook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// ErrNotFound is returned when a playbook is not found.
var ErrNotFound = errors.New("playbook: not found")

// Tier describes one risk-score band and the auto-response action.
type Tier struct {
	Min    int            `json:"min"`
	Max    int            `json:"max"`
	Action string         `json:"action"`
	Params map[string]any `json:"params"`
}

// Playbook is the auto-response configuration for a tenant.
type Playbook struct {
	ID                 string          `json:"id"`
	TenantID           string          `json:"tenant_id"`
	Version            int             `json:"version"`
	Tiers              []Tier          `json:"tiers"`
	EscalationTargets  json.RawMessage `json:"escalation_targets"`
	Active             bool            `json:"active"`
	PauseAutoResponse  bool            `json:"pause_auto_response"`
	CreatedBy          string          `json:"created_by"`
	CreatedAt          time.Time       `json:"created_at"`
	ActivatedAt        *time.Time      `json:"activated_at,omitempty"`
}

// EvalResult is the outcome of evaluating a playbook against a risk score.
type EvalResult struct {
	Action string
	Tier   string
	Params map[string]any
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
			return EvalResult{Action: t.Action, Tier: tierName(score), Params: t.Params}
		}
	}
	return EvalResult{Action: "normal", Tier: "unknown"}
}

func tierName(score int) string {
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

const cacheKeyFmt = "playbook:active:%s"
const cacheTTL = 60 * time.Second

// Store provides database access for playbook management.
type Store struct {
	pool *pgxpool.Pool
	rdb  *redis.Client
}

// NewStore creates a Store.
func NewStore(pool *pgxpool.Pool, rdb *redis.Client) *Store {
	return &Store{pool: pool, rdb: rdb}
}

// Create inserts a new (inactive) playbook version.
func (s *Store) Create(ctx context.Context, tenantID, createdBy string, tiersJSON, escalationJSON json.RawMessage) (string, error) {
	const q = `
		INSERT INTO risk_playbooks (tenant_id, tiers, escalation_targets, created_by, version)
		VALUES ($1, $2, $3, $4,
		    COALESCE((SELECT MAX(version) FROM risk_playbooks WHERE tenant_id=$1), 0)+1
		)
		RETURNING id`
	var id string
	err := s.pool.QueryRow(ctx, q, tenantID, tiersJSON, escalationJSON, createdBy).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("playbook create: %w", err)
	}
	return id, nil
}

// Activate marks a playbook as active (and deactivates the previous one).
func (s *Store) Activate(ctx context.Context, tenantID, playbookID, actorID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("playbook activate: begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Verify the playbook belongs to the tenant.
	var exists bool
	err = tx.QueryRow(ctx,
		`SELECT true FROM risk_playbooks WHERE id=$1 AND tenant_id=$2`,
		playbookID, tenantID).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("playbook activate: check: %w", err)
	}

	// Deactivate current active.
	_, err = tx.Exec(ctx,
		`UPDATE risk_playbooks SET active=false WHERE tenant_id=$1 AND active=true`,
		tenantID)
	if err != nil {
		return fmt.Errorf("playbook activate: deactivate: %w", err)
	}

	// Activate the target.
	_, err = tx.Exec(ctx,
		`UPDATE risk_playbooks SET active=true, activated_at=now() WHERE id=$1`,
		playbookID)
	if err != nil {
		return fmt.Errorf("playbook activate: activate: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("playbook activate: commit: %w", err)
	}

	// Invalidate cache so PDP picks up the new playbook within 60s.
	if s.rdb != nil {
		s.rdb.Del(ctx, fmt.Sprintf(cacheKeyFmt, tenantID))
	}
	return nil
}

// GetActive returns the active playbook for the tenant, using Redis cache.
func (s *Store) GetActive(ctx context.Context, tenantID string) (*Playbook, error) {
	key := fmt.Sprintf(cacheKeyFmt, tenantID)

	if s.rdb != nil {
		raw, err := s.rdb.Get(ctx, key).Bytes()
		if err == nil {
			var p Playbook
			if json.Unmarshal(raw, &p) == nil {
				return &p, nil
			}
		}
	}

	const q = `
		SELECT id, tenant_id, version, tiers, escalation_targets, active,
		       pause_auto_response, created_by, created_at, activated_at
		FROM risk_playbooks
		WHERE tenant_id=$1 AND active=true
		ORDER BY version DESC LIMIT 1`

	p, err := s.scan(s.pool.QueryRow(ctx, q, tenantID))
	if errors.Is(err, pgx.ErrNoRows) {
		return DefaultPlaybook, nil
	}
	if err != nil {
		return DefaultPlaybook, nil
	}

	if s.rdb != nil {
		if b, e := json.Marshal(p); e == nil {
			s.rdb.Set(ctx, key, b, cacheTTL)
		}
	}
	return p, nil
}

// GetByID returns a specific playbook version by ID.
func (s *Store) GetByID(ctx context.Context, tenantID, id string) (*Playbook, error) {
	const q = `
		SELECT id, tenant_id, version, tiers, escalation_targets, active,
		       pause_auto_response, created_by, created_at, activated_at
		FROM risk_playbooks
		WHERE id=$1 AND tenant_id=$2`
	p, err := s.scan(s.pool.QueryRow(ctx, q, id, tenantID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

// List returns all playbook versions for a tenant.
func (s *Store) List(ctx context.Context, tenantID string) ([]*Playbook, error) {
	const q = `
		SELECT id, tenant_id, version, tiers, escalation_targets, active,
		       pause_auto_response, created_by, created_at, activated_at
		FROM risk_playbooks
		WHERE tenant_id=$1
		ORDER BY version DESC`

	rows, err := s.pool.Query(ctx, q, tenantID)
	if err != nil {
		return nil, fmt.Errorf("playbook list: %w", err)
	}
	defer rows.Close()

	var out []*Playbook
	for rows.Next() {
		p, err := s.scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

type rowScanner interface{ Scan(...any) error }

func (s *Store) scan(row rowScanner) (*Playbook, error) {
	var p Playbook
	var tiersJSON, escalationJSON []byte
	err := row.Scan(
		&p.ID, &p.TenantID, &p.Version, &tiersJSON, &escalationJSON,
		&p.Active, &p.PauseAutoResponse, &p.CreatedBy, &p.CreatedAt, &p.ActivatedAt,
	)
	if err != nil {
		return nil, err
	}
	if len(tiersJSON) > 0 {
		_ = json.Unmarshal(tiersJSON, &p.Tiers)
	}
	if len(escalationJSON) > 0 {
		p.EscalationTargets = escalationJSON
	}
	return &p, nil
}
