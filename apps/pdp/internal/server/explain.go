package server

import (
	"context"
	"fmt"
	"strings"

	pdpv1 "github.com/governance-platform/pkg/pdpv1"
	"github.com/governance-platform/policy-engine/engine"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Explain returns the authorization decision plus a human-readable explanation
// with matched rule IDs and evaluation trace. It always bypasses cache so the
// trace is fresh (used by support engineers and the admin console simulator).
func (s *Server) Explain(ctx context.Context, req *pdpv1.DecideRequest) (*pdpv1.DecisionExplanation, error) {
	sc, err := s.parseSessionFromRequest(req)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "invalid session context: %v", err)
	}

	ctx, span := startSpan(ctx, "pdp.Explain", sc.TenantID, sc.UserID, req.Action, req.Resource)
	defer span.End()

	policies, versionStr, err := s.getPolicies(ctx, sc.TenantID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "load policies: %v", err)
	}

	// Explain always runs in explain mode (no cache).
	d := engine.Decide(sc.TenantID, policies, engine.EvalRequest{
		SessionContext: sc,
		Action:         req.Action,
		ResourceURI:    req.Resource,
		ContextAttrs:   req.Context,
		ExplainMode:    true,
	})

	result := decisionToProto(&d, versionStr)
	humanReadable := buildHumanReadable(req, &d)
	return &pdpv1.DecisionExplanation{
		Decision:        result,
		MatchedRuleIds:  d.MatchedPolicyIDs,
		HumanReadable:   humanReadable,
		EvaluationTrace: d.EvaluationTrace,
	}, nil
}

func buildHumanReadable(req *pdpv1.DecideRequest, d *engine.Decision) string {
	var sb strings.Builder
	if d.Effect == engine.EffectPermit {
		sb.WriteString(fmt.Sprintf("PERMIT: action=%q resource=%q\n", req.Action, req.Resource))
		if len(d.AllowedColumns) > 0 {
			sb.WriteString(fmt.Sprintf("  Allowed columns: %s\n", strings.Join(d.AllowedColumns, ", ")))
		}
		if len(d.ColumnMasks) > 0 {
			sb.WriteString("  Column masks applied: ")
			for col, mask := range d.ColumnMasks {
				sb.WriteString(fmt.Sprintf("%s→%s ", col, mask))
			}
			sb.WriteString("\n")
		}
		if len(d.MatchedPolicyIDs) > 0 {
			sb.WriteString(fmt.Sprintf("  Matched policies: %s\n", strings.Join(d.MatchedPolicyIDs, ", ")))
		}
	} else {
		sb.WriteString(fmt.Sprintf("DENY: %s\n", d.Reason))
		if len(d.MatchedPolicyIDs) > 0 {
			sb.WriteString(fmt.Sprintf("  Triggered by: %s\n", strings.Join(d.MatchedPolicyIDs, ", ")))
		}
		if len(d.EvaluationTrace) > 0 {
			sb.WriteString("  Evaluation trace:\n")
			for _, t := range d.EvaluationTrace {
				sb.WriteString("    " + t + "\n")
			}
		}
	}
	return sb.String()
}
