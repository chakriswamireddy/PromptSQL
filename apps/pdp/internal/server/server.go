// Package server implements the PDPServer gRPC interface.
package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pkgauth "github.com/governance-platform/pkg/auth"
	"github.com/governance-platform/pkg/logging"
	pdpv1 "github.com/governance-platform/pkg/pdpv1"
	pdpmetrics "github.com/governance-platform/pdp/internal/metrics"
	"github.com/governance-platform/pdp/internal/cache"
	"github.com/governance-platform/pdp/internal/invalidation"
	"github.com/governance-platform/pdp/internal/store"
	"github.com/governance-platform/policy-engine/engine"
)

const tracerName = "pdp"

// Server implements pdpv1.PDPServer.
type Server struct {
	pdpv1.UnimplementedPDPServer

	store    *store.Store
	cache    *cache.Cache
	hmac     *pkgauth.HMACService
	sub      *invalidation.Subscriber
	versions *invalidation.VersionStore
	log      logging.Logger

	// policyBundles holds compiled policy lists per tenant.
	mu      sync.RWMutex
	bundles map[string][]engine.Policy
}

// Config holds dependencies for Server.
type Config struct {
	Store    *store.Store
	Cache    *cache.Cache
	HMAC     *pkgauth.HMACService
	Sub      *invalidation.Subscriber
	Versions *invalidation.VersionStore
	Log      logging.Logger
}

// New creates a Server and registers the invalidation callback.
func New(cfg Config) *Server {
	s := &Server{
		store:    cfg.Store,
		cache:    cfg.Cache,
		hmac:     cfg.HMAC,
		sub:      cfg.Sub,
		versions: cfg.Versions,
		log:      cfg.Log,
		bundles:  make(map[string][]engine.Policy),
	}
	return s
}

// InvalidateCallback is called by the pub/sub subscriber when a tenant's policies change.
func (s *Server) InvalidateCallback(ctx context.Context, tenantID string, _ []string) {
	s.log.Info().Str("tenant", tenantID).Msg("reloading policies after invalidation")
	policies, compErrs, err := s.store.LoadActivePolicies(ctx, tenantID)
	if err != nil {
		s.log.Error().Err(err).Str("tenant", tenantID).Msg("reload failed")
		pdpmetrics.InvalidateTotal.WithLabelValues("reload_error").Inc()
		return
	}
	for _, ce := range compErrs {
		s.log.Warn().Err(ce).Str("tenant", tenantID).Msg("policy compile error — policy skipped")
		pdpmetrics.CompilePanicTotal.Inc()
	}
	s.mu.Lock()
	s.bundles[tenantID] = policies
	s.mu.Unlock()
	s.cache.EvictTenant(tenantID)
	pdpmetrics.ActivePolicies.WithLabelValues(tenantID).Set(float64(len(policies)))
}

// getPolicies returns the cached policy bundle for tenantID, loading from DB if not present.
func (s *Server) getPolicies(ctx context.Context, tenantID string) ([]engine.Policy, string, error) {
	s.mu.RLock()
	bundle, ok := s.bundles[tenantID]
	s.mu.RUnlock()

	ver, err := s.store.PolicySetVersion(ctx, tenantID)
	if err != nil {
		// Non-fatal: use whatever we have.
		ver = s.versions.Get(tenantID)
	}
	versionStr := store.VersionKey(ver)

	if !ok {
		// Cold start: load from DB.
		loaded, compErrs, loadErr := s.store.LoadActivePolicies(ctx, tenantID)
		if loadErr != nil {
			return nil, "", fmt.Errorf("load policies: %w", loadErr)
		}
		for _, ce := range compErrs {
			s.log.Warn().Err(ce).Str("tenant", tenantID).Msg("policy compile error")
			pdpmetrics.CompilePanicTotal.Inc()
		}
		s.mu.Lock()
		s.bundles[tenantID] = loaded
		s.mu.Unlock()
		// Start pub/sub subscription and poller for this tenant.
		s.sub.Subscribe(ctx, tenantID)
		s.sub.StartPoller(ctx, tenantID, s.store.PolicySetVersion)
		s.versions.Set(tenantID, ver)
		pdpmetrics.ActivePolicies.WithLabelValues(tenantID).Set(float64(len(loaded)))
		bundle = loaded
	}
	return bundle, versionStr, nil
}

// verifySession decodes and HMAC-verifies the inbound SessionContext bytes.
func (s *Server) verifySession(ctxB64, sigB64, keyID string) (*pkgauth.SessionContext, error) {
	sc, err := s.hmac.Verify(ctxB64, sigB64, keyID)
	if err != nil {
		return nil, fmt.Errorf("hmac verify: %w", err)
	}
	return sc, nil
}

// decisionToProto converts an engine.Decision to a pdpv1.Decision wire message.
func decisionToProto(d *engine.Decision, versionStr string) *pdpv1.Decision {
	effect := pdpv1.Effect_EFFECT_DENY
	if d.Effect == engine.EffectPermit {
		effect = pdpv1.Effect_EFFECT_PERMIT
	}
	result := &pdpv1.Decision{
		Effect:           effect,
		AllowedColumns:   d.AllowedColumns,
		ColumnMasks:      d.ColumnMasks,
		Reason:           d.Reason,
		PolicySetVersion: versionStr,
		MatchedPolicyIds: d.MatchedPolicyIDs,
		TtlSeconds:       int32(5 * 60), // 5 minutes default TTL
	}
	if d.RowFilter != nil {
		b, _ := json.Marshal(d.RowFilter)
		result.RowFilter = &pdpv1.RowFilter{AstJson: b}
	}
	for _, ob := range d.Obligations {
		result.Obligations = append(result.Obligations, &pdpv1.Obligation{
			Type:   ob.Type,
			Params: ob.Params,
		})
	}
	return result
}

// startSpan creates an OTel span with standard PDP attributes.
func startSpan(ctx context.Context, name, tenantID, userID, action, resource string) (context.Context, trace.Span) {
	tr := otel.Tracer(tracerName)
	return tr.Start(ctx, name,
		trace.WithAttributes(
			attribute.String("pdp.tenant_id", tenantID),
			attribute.String("pdp.user_id", userID),
			attribute.String("pdp.action", action),
			attribute.String("pdp.resource", resource),
		),
	)
}

// recordDecisionSpan adds decision-specific attributes to a span.
func recordDecisionSpan(span trace.Span, d *pdpv1.Decision, cacheLayer string, policyVer string) {
	span.SetAttributes(
		attribute.String("pdp.effect", d.Effect.String()),
		attribute.String("pdp.cache_layer", cacheLayer),
		attribute.String("pdp.policy_set_version", policyVer),
		attribute.StringSlice("pdp.matched_policy_ids", d.MatchedPolicyIds),
	)
}

// observeDecision records Prometheus metrics for a completed decision.
func observeDecision(effect string, cacheLayer, tenantID string, start time.Time) {
	pdpmetrics.DecisionDuration.WithLabelValues(cacheLayer).Observe(time.Since(start).Seconds())
	pdpmetrics.DecisionTotal.WithLabelValues(effect, cacheLayer, tenantID).Inc()
}

// featureDisabledError returns the standard gRPC error when the feature flag is off.
func featureDisabledError() error {
	return status.Error(codes.NotFound, "feature_disabled: pdp-v1 is not enabled")
}

// parseSessionFromRequest decodes and verifies the SessionContext from a DecideRequest.
func (s *Server) parseSessionFromRequest(req *pdpv1.DecideRequest) (*pkgauth.SessionContext, error) {
	ctxB64 := base64.StdEncoding.EncodeToString(req.SubjectSessionContext)
	sigB64 := base64.StdEncoding.EncodeToString(req.SubjectSessionSig)
	return s.verifySession(ctxB64, sigB64, req.SubjectSessionKeyId)
}
