// Package engine implements the deny-overrides decision algorithm.
// It is intentionally a pure library with no I/O so it can be imported by
// both the runtime PDP service and the admin console simulator — guaranteeing
// identical decisions in both contexts.
package engine

import (
	"strings"
	"time"

	"github.com/governance-platform/pkg/auth"
	"github.com/governance-platform/policy-engine/dsl"
)

// Effect is the authorization decision outcome.
type Effect string

const (
	EffectPermit Effect = "PERMIT"
	EffectDeny   Effect = "DENY"
)

// Obligation is a post-decision action required from the caller
// (e.g., "require_mfa_within", "audit_every_row").
type Obligation struct {
	Type   string
	Params map[string]string
}

// ObligationSatisfied reports whether the obligation is satisfiable
// given the current SessionContext. Returns (satisfied bool, reason string).
func ObligationSatisfied(o Obligation, sc *auth.SessionContext) (bool, string) {
	switch o.Type {
	case "require_mfa_within":
		window := o.Params["window"]
		if window == "" {
			return false, "require_mfa_within: missing window param"
		}
		d, err := time.ParseDuration(window)
		if err != nil {
			return false, "require_mfa_within: invalid window: " + err.Error()
		}
		if sc.MFAAt == nil {
			return false, "require_mfa_within: no MFA recorded"
		}
		if time.Since(*sc.MFAAt) > d {
			return false, "require_mfa_within: mfa_stale"
		}
		return true, ""
	default:
		// Unknown obligation types are deferred (non-blocking in V1).
		return true, ""
	}
}

// Policy is the engine's view of a stored policy row.
// The DSL conditions are pre-compiled into ConditionFns at activation time.
type Policy struct {
	ID             string
	TenantID       string
	Name           string
	Version        int
	Effect         string // "allow" | "deny"
	Action         string // exact match or "*"
	ResourcePrefix string // URI prefix; empty = match all
	// CompiledConditions is nil when the policy has no conditions (matches always).
	CompiledConditions dsl.ConditionFn
	// ConditionsNode is kept for SQL emitter and explain.
	ConditionsNode *dsl.Node
	AllowedColumns []string
	DeniedColumns  []string
	// ColumnMasks: column name → mask expression (e.g. "REDACTED", SHA256, etc.)
	ColumnMasks map[string]string
	// RowFilter is compiled separately for SQL emission.
	RowFilter *dsl.Node
	// CompiledRowFilter for runtime evaluation (not SQL).
	CompiledRowFilter dsl.ConditionFn
	Obligations       []Obligation
}

// Decision is the output of the decision algorithm.
type Decision struct {
	Effect           Effect
	AllowedColumns   []string
	DeniedColumns    []string
	ColumnMasks      map[string]string
	RowFilter        *dsl.Node // nil if no row filter applicable
	Obligations      []Obligation
	Reason           string
	MatchedPolicyIDs []string
	// EvaluationTrace is populated only in explain mode.
	EvaluationTrace []string
}

// EvalRequest is the input to the decision algorithm.
type EvalRequest struct {
	SessionContext   *auth.SessionContext
	Action           string
	ResourceURI      string
	// ContextAttrs carries caller-supplied row-level attributes.
	ContextAttrs     map[string]string
	// ResourceAttrs carries resolved resource classification attributes.
	ResourceAttrs    map[string]interface{}
	// ExplainMode causes trace messages to be collected.
	ExplainMode      bool
}

// matchesAction reports whether the policy action matches the requested action.
// "*" is a wildcard; otherwise exact match is required.
func (p *Policy) matchesAction(action string) bool {
	return p.Action == "*" || strings.EqualFold(p.Action, action)
}

// matchesResource reports whether the policy resource prefix matches the request URI.
// A policy at a higher level of the URI tree matches all descendants.
func (p *Policy) matchesResource(resourceURI string) bool {
	if p.ResourcePrefix == "" || p.ResourcePrefix == "*" {
		return true
	}
	return resourceURI == p.ResourcePrefix ||
		strings.HasPrefix(resourceURI, p.ResourcePrefix+"/")
}
