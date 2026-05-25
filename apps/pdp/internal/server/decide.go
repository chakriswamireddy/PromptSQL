package server

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	pkgauth "github.com/governance-platform/pkg/auth"
	pdpv1 "github.com/governance-platform/pkg/pdpv1"
	pdpmetrics "github.com/governance-platform/pdp/internal/metrics"
	"github.com/governance-platform/pdp/internal/cache"
	"github.com/governance-platform/policy-engine/engine"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Decide evaluates a single authorization decision.
// Hot path: L1 hit < 100 µs; L2 hit < 1 ms; cold path < 25 ms.
func (s *Server) Decide(ctx context.Context, req *pdpv1.DecideRequest) (*pdpv1.Decision, error) {
	start := time.Now()

	sc, err := s.parseSessionFromRequest(req)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "invalid session context: %v", err)
	}

	ctx, span := startSpan(ctx, "pdp.Decide", sc.TenantID, sc.UserID, req.Action, req.Resource)
	defer span.End()

	// Phase 13: inject riskScore into SessionContext before policy evaluation.
	sc.RiskScore = s.fetchRiskScore(ctx, sc.TenantID, sc.UserID)
	if sc.RiskScore != nil {
		span.SetAttributes(attribute.Int("pdp.risk_score", *sc.RiskScore))
	}

	// Phase 14: evaluate auto-response playbook for this risk score.
	// Short-circuit BEFORE cache to ensure fresh playbook decisions.
	if sc.RiskScore != nil && s.playbooks != nil {
		if override := s.evaluatePlaybook(ctx, sc, req, *sc.RiskScore, span); override != nil {
			go s.emitAudit(context.Background(), sc, req.Action, req.Resource, nil)
			return override, nil
		}
	}

	policies, versionStr, err := s.getPolicies(ctx, sc.TenantID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "load policies: %v", err)
	}

	cacheKey := cache.DecisionKey{
		TenantID:      sc.TenantID,
		UserID:        sc.UserID,
		Action:        req.Action,
		ResourceURI:   req.Resource,
		PolicyVersion: versionStr,
		ContextAttrs:  req.Context,
	}

	// Cache lookup with singleflight.
	d, cacheLayer, found := s.cache.Get(ctx, cacheKey)
	if !found {
		var evalErr error
		d, evalErr = s.cache.Do(cacheKey, func() (*engine.Decision, error) {
			decision := engine.Decide(sc.TenantID, policies, engine.EvalRequest{
				SessionContext: sc,
				Action:         req.Action,
				ResourceURI:    req.Resource,
				ContextAttrs:   req.Context,
			})
			return &decision, nil
		})
		if evalErr != nil {
			return nil, status.Errorf(codes.Internal, "evaluate: %v", evalErr)
		}
		cacheLayer = "miss"
		s.cache.Set(ctx, cacheKey, d)
	}

	result := decisionToProto(d, versionStr)
	recordDecisionSpan(span, result, cacheLayer, versionStr)
	observeDecision(string(d.Effect), cacheLayer, sc.TenantID, start)
	pdpmetrics.DecisionDuration.WithLabelValues(cacheLayer).Observe(time.Since(start).Seconds())

	go s.emitAudit(context.Background(), sc, req.Action, req.Resource, d)

	return result, nil
}

// evaluatePlaybook checks the tenant's risk playbook and returns an override
// decision when the risk score triggers step-up, mask, or block actions.
// Returns nil when normal policy evaluation should continue.
func (s *Server) evaluatePlaybook(
	ctx context.Context,
	sc *pkgauth.SessionContext,
	req *pdpv1.DecideRequest,
	score int,
	span trace.Span,
) *pdpv1.Decision {
	pb, err := s.playbooks.GetActive(ctx, sc.TenantID)
	if err != nil || pb == nil {
		return nil
	}
	result := pb.Evaluate(score)
	span.SetAttributes(
		attribute.String("pdp.playbook_action", result.Action),
		attribute.String("pdp.risk_tier", result.Tier),
	)
	pdpmetrics.PlaybookActionTotal.WithLabelValues(result.Action, result.Tier, sc.TenantID).Inc()

	switch result.Action {
	case "block":
		return &pdpv1.Decision{
			Effect: pdpv1.Effect_EFFECT_DENY,
			Reason: "auto_response: risk score exceeds block threshold",
		}

	case "step_up":
		// Obligation: require MFA before the next query succeeds.
		if s.obligation == nil {
			// Obligation service not configured — degrade to allow with tag.
			return nil
		}
		tokenStr, _, err := s.obligation.Issue(sc.TenantID, sc.UserID, sc.JTI, "risk_threshold", score)
		if err != nil {
			s.log.Error().Err(err).Msg("failed to issue obligation token")
			return nil
		}
		return &pdpv1.Decision{
			Effect: pdpv1.Effect_EFFECT_DENY,
			Reason: "auto_response: step-up MFA required",
			Obligations: []*pdpv1.Obligation{
				{
					Type: "require_mfa",
					Params: map[string]string{
						"obligation_token": tokenStr,
						"mfa_window_sec":   "300",
						"reason":           "risk_threshold",
					},
				},
			},
		}

	case "mask":
		// Return a permit with additional masking obligation injected.
		return &pdpv1.Decision{
			Effect: pdpv1.Effect_EFFECT_PERMIT,
			Reason: "auto_response: heightened masking applied",
			Obligations: []*pdpv1.Obligation{
				{Type: "apply_heightened_mask", Params: map[string]string{}},
			},
		}

	default:
		return nil
	}
}

// BulkDecide evaluates multiple decisions in a single round-trip.
// Each item is evaluated independently; partial failures return per-item errors.
func (s *Server) BulkDecide(ctx context.Context, req *pdpv1.BulkDecideRequest) (*pdpv1.BulkDecideResponse, error) {
	resp := &pdpv1.BulkDecideResponse{
		Items: make([]*pdpv1.BulkDecideItem, len(req.Requests)),
	}
	for i, r := range req.Requests {
		item := &pdpv1.BulkDecideItem{Request: r}
		d, err := s.Decide(ctx, r)
		if err != nil {
			item.Error = err.Error()
		} else {
			item.Decision = d
		}
		resp.Items[i] = item
	}
	return resp, nil
}

// emitAudit sends a decision to the audit pipeline.
// In Phase 3 this is a no-op placeholder; Phase 5 wires in the Kafka producer.
func (s *Server) emitAudit(_ context.Context, sc interface{ GetUserID() string; GetTenantID() string }, action, resource string, d *engine.Decision) {
	// Phase 5 will replace this with an actual audit-client SDK call.
	_ = sc
	_ = action
	_ = resource
	_ = d
}
