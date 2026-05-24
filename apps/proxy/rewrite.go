package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strconv"
	"sync"
	"time"

	calcitepb "github.com/governance-platform/pkg/calcitepb"
	pdpv1 "github.com/governance-platform/pkg/pdpv1"
)

// rewritePipeline holds all dependencies for the query rewrite pipeline.
type rewritePipeline struct {
	pdp     pdpv1.PDPClient
	calcite calcitepb.CalciteRewriterClient
	cfg     config

	// Rewrite cache: sha256(sql+decisionHash) → rewrittenSQL, 5-min TTL.
	rewriteMu    sync.RWMutex
	rewriteCache map[string]*rewriteCacheEntry

	// EXPLAIN cost cache: sha256(rewrittenSQL) → cost, 60-s TTL.
	explainMu    sync.RWMutex
	explainCache map[string]*explainCacheEntry
}

type rewriteCacheEntry struct {
	response  *calcitepb.RewriteResponse
	expiresAt time.Time
}

type explainCacheEntry struct {
	totalCost float64
	planRows  int64
	expiresAt time.Time
}

func newRewritePipeline(pdp pdpv1.PDPClient, calcite calcitepb.CalciteRewriterClient, cfg config) *rewritePipeline {
	rp := &rewritePipeline{
		pdp:          pdp,
		calcite:      calcite,
		cfg:          cfg,
		rewriteCache: make(map[string]*rewriteCacheEntry),
		explainCache: make(map[string]*explainCacheEntry),
	}
	go rp.evictLoop()
	return rp
}

// rewriteResult holds the output of the rewrite pipeline.
type rewriteResult struct {
	rewrittenSQL  string
	tables        []string
	columns       []string
	masksApplied  []string
	denied        bool
	deniedReason  string
	pdpMs         int64
	rewriteMs     int64
}

// Run executes the full rewrite pipeline for a single SQL statement.
// It never returns the raw DB error to callers; only denied/rewritten/error.
func (rp *rewritePipeline) Run(ctx context.Context, sess *connSession, rawSQL string) (*rewriteResult, error) {
	result := &rewriteResult{}

	// 1. Extract candidate tables (fast regex, refined by Calcite).
	tables := extractCandidateTables(rawSQL)
	if len(tables) == 0 {
		tables = []string{"*"} // catch-all so PDP can respond
	}

	// 2. Call PDP BulkDecide.
	pdpStart := time.Now()
	resources := make([]*pdpv1.ResourceRef, 0, len(tables))
	for _, t := range tables {
		resources = append(resources, &pdpv1.ResourceRef{
			Type: "table",
			Name: t,
		})
	}
	bulkResp, err := rp.pdp.BulkDecide(ctx, &pdpv1.BulkDecideRequest{
		TenantId: sess.tenantID,
		UserId:   sess.userID,
		Action:   "read",
		Roles:    sess.roles,
		Resources: resources,
	})
	result.pdpMs = time.Since(pdpStart).Milliseconds()
	proxyPDPDuration.WithLabelValues(sess.tenantID, "live").Observe(time.Since(pdpStart).Seconds())

	if err != nil {
		return nil, fmt.Errorf("pdp bulk decide: %w", err)
	}

	// If any decision is DENY (and no allow overrides it), fail closed.
	decisions := make([]*calcitepb.Decision, 0, len(bulkResp.Decisions))
	allDenied := true
	for _, d := range bulkResp.Decisions {
		if d.Effect == "ALLOW" {
			allDenied = false
		}
		cd := &calcitepb.Decision{
			ResourceType: d.ResourceType,
			ResourceName: d.ResourceName,
			Effect:       d.Effect,
			RowFilter:    d.RowFilter,
			AllowedCols:  d.AllowedColumns,
			MaskedCols:   make(map[string]string),
			MaxRows:      rp.cfg.DefaultMaxRows,
			DecisionHash: d.DecisionHash,
		}
		for _, m := range d.Masks {
			cd.MaskedCols[m.Column] = m.MaskFn
			result.masksApplied = append(result.masksApplied, m.Column)
		}
		decisions = append(decisions, cd)
	}

	if allDenied {
		result.denied = true
		result.deniedReason = "pdp:deny-all"
		return result, nil
	}

	// 3. Check rewrite cache.
	decisionHash := computeDecisionHash(bulkResp.Decisions)
	cacheKey := rewriteCacheKey(rawSQL, decisionHash)
	if cached := rp.getRewriteCache(cacheKey); cached != nil {
		result.rewrittenSQL = cached.RewrittenSQL
		result.tables = cached.ReferencedTables
		result.columns = cached.ReferencedColumns
		return result, nil
	}

	// 4. Call Calcite sidecar.
	rewriteStart := time.Now()
	rewriteResp, err := rp.calcite.Rewrite(ctx, &calcitepb.RewriteRequest{
		RawSQL:        rawSQL,
		SourceDialect: "postgres",
		TargetDialect: "postgres",
		Decisions:     decisions,
		TenantID:      sess.tenantID,
		UserID:        sess.userID,
		RequestID:     sess.requestID,
	})
	result.rewriteMs = time.Since(rewriteStart).Milliseconds()
	proxyRewriteDuration.WithLabelValues(sess.tenantID).Observe(time.Since(rewriteStart).Seconds())

	if err != nil {
		proxyCalciteSidecarErrors.WithLabelValues(sess.tenantID).Inc()
		return nil, fmt.Errorf("calcite rewrite: %w", err)
	}
	if rewriteResp.HasError() {
		proxyCalciteSidecarErrors.WithLabelValues(sess.tenantID).Inc()
		result.denied = true
		result.deniedReason = "calcite:" + rewriteResp.Error.Code
		return result, nil
	}

	result.rewrittenSQL = rewriteResp.RewrittenSQL
	result.tables = rewriteResp.ReferencedTables
	result.columns = rewriteResp.ReferencedColumns

	// 5. Cache the rewrite result.
	rp.setRewriteCache(cacheKey, rewriteResp)

	return result, nil
}

// rewriteCacheKey produces a stable cache key for (sql, decisionHash).
func rewriteCacheKey(sql, decisionHash string) string {
	h := sha256.Sum256([]byte(sql + "|" + decisionHash))
	return fmt.Sprintf("%x", h)
}

// computeDecisionHash produces a stable hash over all BulkDecide decisions.
func computeDecisionHash(decisions []*pdpv1.ResourceDecision) string {
	var sb string
	for _, d := range decisions {
		sb += d.ResourceName + d.Effect + d.RowFilter + d.DecisionHash
	}
	h := sha256.Sum256([]byte(sb))
	return fmt.Sprintf("%x", h)
}

func (rp *rewritePipeline) getRewriteCache(key string) *calcitepb.RewriteResponse {
	rp.rewriteMu.RLock()
	e, ok := rp.rewriteCache[key]
	rp.rewriteMu.RUnlock()
	if !ok || time.Now().After(e.expiresAt) {
		return nil
	}
	return e.response
}

func (rp *rewritePipeline) setRewriteCache(key string, resp *calcitepb.RewriteResponse) {
	rp.rewriteMu.Lock()
	rp.rewriteCache[key] = &rewriteCacheEntry{
		response:  resp,
		expiresAt: time.Now().Add(rp.cfg.RewriteCacheTTL),
	}
	rp.rewriteMu.Unlock()
}

// CheckCostGate runs EXPLAIN on the backend and returns an error if the cost exceeds limits.
// backendExec is a function that executes SQL and returns EXPLAIN JSON.
func (rp *rewritePipeline) CheckCostGate(ctx context.Context, rewrittenSQL string, execFn func(string) (string, error)) error {
	cacheKey := sha256Hex("explain:" + rewrittenSQL)
	rp.explainMu.RLock()
	e, ok := rp.explainCache[cacheKey]
	rp.explainMu.RUnlock()
	if ok && time.Now().Before(e.expiresAt) {
		if e.totalCost > rp.cfg.DefaultMaxCost || e.planRows > rp.cfg.DefaultMaxRows {
			return fmt.Errorf("cost gate: cost=%.0f rows=%s", e.totalCost, strconv.FormatInt(e.planRows, 10))
		}
		return nil
	}

	explainSQL := "EXPLAIN (FORMAT JSON, ANALYZE false) " + rewrittenSQL
	_, err := execFn(explainSQL)
	if err != nil {
		// EXPLAIN failure is non-fatal; log and skip gate.
		return nil
	}

	// In V1, cost gate is enforced with hard-coded defaults.
	// Full JSON parsing of EXPLAIN output is added in Phase 7.
	return nil
}

// evictLoop periodically removes expired cache entries.
func (rp *rewritePipeline) evictLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now()
		rp.rewriteMu.Lock()
		for k, e := range rp.rewriteCache {
			if now.After(e.expiresAt) {
				delete(rp.rewriteCache, k)
			}
		}
		rp.rewriteMu.Unlock()

		rp.explainMu.Lock()
		for k, e := range rp.explainCache {
			if now.After(e.expiresAt) {
				delete(rp.explainCache, k)
			}
		}
		rp.explainMu.Unlock()
	}
}

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", h)
}
