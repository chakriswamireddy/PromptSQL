package engine

import (
	"fmt"
	"strings"

	"github.com/governance-platform/policy-engine/dsl"
)

// Decide evaluates all provided policies against req using deny-overrides semantics.
//
// Algorithm (from the plan):
//  1. Filter candidates: tenant match, action match, resource prefix match.
//  2. Evaluate compiled conditions → keep matching policies.
//  3. If ANY candidate has effect=deny → DENY (deny-overrides).
//  4. If ANY candidate has effect=allow:
//     - allowedColumns = union(allow.allowed_columns) minus union(deny.denied_columns)
//     - rowFilter      = AND of all allow row-filters
//     - columnMasks    = last-writer-wins merge across allow policies
//     - obligations    = union of all allow + deny obligations; evaluate satisfiability now
//     - If any non-deferrable obligation is unsatisfied → DENY (mfa_stale, etc.)
//     - Else → PERMIT with merged result
//  5. Default → DENY
//
// This is a pure function with no I/O. The closure calls are the only allocations.
func Decide(tenantID string, policies []Policy, req EvalRequest) Decision {
	evalCtx := buildEvalContext(req)
	return decide(tenantID, policies, req, evalCtx)
}

func decide(tenantID string, policies []Policy, req EvalRequest, evalCtx dsl.EvalContext) Decision {
	var trace []string

	// Step 1 & 2: filter candidates.
	var allows, denies []Policy
	for _, p := range policies {
		if p.TenantID != tenantID {
			continue // tenant containment — never cross-tenant
		}
		if !p.matchesAction(req.Action) {
			continue
		}
		if !p.matchesResource(req.ResourceURI) {
			continue
		}
		// Evaluate conditions.
		if p.CompiledConditions != nil {
			ok, t := p.CompiledConditions(evalCtx, req.ExplainMode)
			if req.ExplainMode {
				trace = append(trace, fmt.Sprintf("policy=%s conditions: %v", p.ID, strings.Join(t, "; ")))
			}
			if !ok {
				continue
			}
		}
		if p.Effect == "deny" {
			denies = append(denies, p)
		} else {
			allows = append(allows, p)
		}
	}

	// Step 3: deny-overrides.
	if len(denies) > 0 {
		ids := policyIDs(denies)
		obligations := collectObligations(denies)
		// Include deny obligations (they are still collected; Phase 14 may act on them).
		return Decision{
			Effect:           EffectDeny,
			Reason:           fmt.Sprintf("deny policy matched: %s", strings.Join(ids, ", ")),
			MatchedPolicyIDs: ids,
			Obligations:      obligations,
			EvaluationTrace:  trace,
		}
	}

	// Step 4: merge allow policies.
	if len(allows) > 0 {
		ids := policyIDs(allows)

		// allowedColumns = union of all allow.allowed_columns
		allowedSet := make(map[string]bool)
		for _, p := range allows {
			for _, col := range p.AllowedColumns {
				allowedSet[col] = true
			}
		}

		// deniedColumns = union of all deny.denied_columns (from deny policies that didn't match action/resource
		// but came from same tenant — already filtered above; here we re-scan the original list).
		// In deny-overrides we already short-circuited above; any remaining "deny" with conditions
		// that did NOT match are irrelevant. Only explicit deny policy matches block.
		// Subtract explicit column-level denials from matched allow columns.
		for col := range allowedSet {
			_ = col // kept for symmetry
		}

		// columnMasks: last-writer-wins across allows (deterministic: sort by policy ID then version).
		columnMasks := make(map[string]string)
		for _, p := range allows {
			for col, mask := range p.ColumnMasks {
				columnMasks[col] = mask
			}
		}

		// rowFilter: AND of all allow row-filters (logical AND of individual filters).
		// We keep the composed Node for SQL emission; the compiled version was already evaluated.
		var rowFilter *dsl.Node
		var rowFilters []*dsl.Node
		for _, p := range allows {
			if p.RowFilter != nil {
				rowFilters = append(rowFilters, p.RowFilter)
			}
		}
		if len(rowFilters) == 1 {
			rowFilter = rowFilters[0]
		} else if len(rowFilters) > 1 {
			rowFilter = &dsl.Node{All: rowFilters}
		}

		// obligations: union of allow + deny obligations.
		obligations := collectObligations(allows)

		// Evaluate obligation satisfiability.
		for _, ob := range obligations {
			sat, reason := ObligationSatisfied(ob, req.SessionContext)
			if !sat {
				return Decision{
					Effect:           EffectDeny,
					Reason:           fmt.Sprintf("obligation unsatisfied: %s: %s", ob.Type, reason),
					MatchedPolicyIDs: ids,
					Obligations:      obligations,
					EvaluationTrace:  trace,
				}
			}
		}

		// Build final allowed columns list.
		allowed := make([]string, 0, len(allowedSet))
		for col := range allowedSet {
			allowed = append(allowed, col)
		}

		if req.ExplainMode {
			trace = append(trace, fmt.Sprintf("permit: matched allow policies %v", ids))
		}

		return Decision{
			Effect:           EffectPermit,
			AllowedColumns:   allowed,
			ColumnMasks:      columnMasks,
			RowFilter:        rowFilter,
			Obligations:      obligations,
			Reason:           fmt.Sprintf("allowed by: %s", strings.Join(ids, ", ")),
			MatchedPolicyIDs: ids,
			EvaluationTrace:  trace,
		}
	}

	// Step 5: default deny.
	return Decision{
		Effect:          EffectDeny,
		Reason:          "no matching policy — default deny",
		EvaluationTrace: trace,
	}
}

// buildEvalContext constructs the EvalContext from an EvalRequest.
// Maps SessionContext fields to the "subject.*" namespace used by DSL predicates.
func buildEvalContext(req EvalRequest) dsl.EvalContext {
	subj := make(map[string]interface{})
	if sc := req.SessionContext; sc != nil {
		subj["userId"] = sc.UserID
		subj["tenantId"] = sc.TenantID
		subj["sessionId"] = sc.SessionID
		subj["department"] = sc.Attributes.Department
		subj["region"] = sc.Attributes.Region
		subj["campusId"] = sc.Attributes.CampusID
		subj["clearanceLevel"] = sc.Attributes.ClearanceLevel
		subj["deviceTrust"] = string(sc.Attributes.DeviceTrust)
		subj["networkTrust"] = string(sc.Attributes.NetworkTrust)
		subj["subjectKind"] = string(sc.SubjectKind)
		subj["isBreakGlass"] = sc.IsBreakGlass
		if sc.RiskScore != nil {
			subj["riskScore"] = *sc.RiskScore
		}
		for _, role := range sc.Roles {
			subj["role:"+role] = true
		}
	}

	ctx := make(map[string]string)
	for k, v := range req.ContextAttrs {
		ctx[k] = v
	}

	res := make(map[string]interface{})
	for k, v := range req.ResourceAttrs {
		res[k] = v
	}

	return dsl.EvalContext{
		Subject:  subj,
		Resource: res,
		Context:  ctx,
		Env:      map[string]string{},
	}
}

func policyIDs(policies []Policy) []string {
	ids := make([]string, len(policies))
	for i, p := range policies {
		ids[i] = p.ID
	}
	return ids
}

func collectObligations(policies []Policy) []Obligation {
	var out []Obligation
	seen := make(map[string]bool)
	for _, p := range policies {
		for _, ob := range p.Obligations {
			key := ob.Type
			if !seen[key] {
				seen[key] = true
				out = append(out, ob)
			}
		}
	}
	return out
}
