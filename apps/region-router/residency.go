package main

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// DataResidency enumerates the legal/compliance constraints a tenant may carry.
type DataResidency string

const (
	ResidencyUS    DataResidency = "us"
	ResidencyEU    DataResidency = "eu"
	ResidencyAPAC  DataResidency = "apac"
	ResidencyMulti DataResidency = "multi"
)

// regionForResidency returns the canonical write region for a residency constraint.
var regionForResidency = map[DataResidency]string{
	ResidencyUS:    "us-east-1",
	ResidencyEU:    "eu-west-1",
	ResidencyAPAC:  "ap-southeast-1",
	ResidencyMulti: "",  // multi means use home_region; no override
}

// TenantRegion holds the routing metadata for a single tenant.
type TenantRegion struct {
	TenantID      string
	HomeRegion    string
	DataResidency DataResidency
}

// ResidencyStore fetches per-tenant routing metadata from the control-plane DB.
// Results are cached for 60 s to avoid a DB round-trip on every request.
type ResidencyStore struct {
	db    *sql.DB
	cache *ttlCache[string, TenantRegion]
	tracer trace.Tracer
}

func newResidencyStore(db *sql.DB, tracer trace.Tracer) *ResidencyStore {
	return &ResidencyStore{
		db:    db,
		cache: newTTLCache[string, TenantRegion](60 * time.Second),
		tracer: tracer,
	}
}

func (s *ResidencyStore) Get(ctx context.Context, tenantID string) (TenantRegion, error) {
	ctx, span := s.tracer.Start(ctx, "residency.Get", trace.WithAttributes(
		attribute.String("tenant_id", tenantID),
	))
	defer span.End()

	if v, ok := s.cache.Get(tenantID); ok {
		span.SetAttributes(attribute.Bool("cache_hit", true))
		return v, nil
	}

	var tr TenantRegion
	err := s.db.QueryRowContext(ctx,
		// SET LOCAL enforces RLS for the query. The region-router uses a read-only
		// service role (governance_reader) that has SELECT on tenants but cannot write.
		`SET LOCAL ROLE governance_reader;
		 SELECT id::text, home_region, data_residency
		 FROM tenants WHERE id = $1 AND deleted_at IS NULL`,
		tenantID,
	).Scan(&tr.TenantID, &tr.HomeRegion, &tr.DataResidency)
	if err == sql.ErrNoRows {
		return TenantRegion{}, fmt.Errorf("tenant %q not found", tenantID)
	}
	if err != nil {
		return TenantRegion{}, fmt.Errorf("residency lookup: %w", err)
	}

	s.cache.Set(tenantID, tr)
	span.SetAttributes(attribute.String("home_region", tr.HomeRegion), attribute.String("data_residency", string(tr.DataResidency)))
	return tr, nil
}

// routeRequest decides which upstream region should handle req.
// Rules (in priority order):
//  1. EU/APAC residency tenants MUST go to their residency region — cross-region refused.
//  2. Write requests (POST/PUT/PATCH/DELETE) go to home_region.
//  3. Read requests (GET/HEAD) go to local region (latency wins).
func routeRequest(tr TenantRegion, localRegion string, r *http.Request) (targetRegion string, reason string) {
	// Residency hard-wall: if the residency region differs from local, redirect.
	if tr.DataResidency != ResidencyMulti && tr.DataResidency != ResidencyUS {
		if forced, ok := regionForResidency[tr.DataResidency]; ok && forced != "" {
			return forced, "data_residency"
		}
	}

	// Write affinity: mutations must hit home_region (single writer).
	switch r.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return tr.HomeRegion, "write_affinity"
	}

	// Reads can be served locally.
	return localRegion, "local_read"
}
