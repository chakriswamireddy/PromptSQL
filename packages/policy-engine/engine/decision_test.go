package engine_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/governance-platform/pkg/auth"
	"github.com/governance-platform/policy-engine/dsl"
	"github.com/governance-platform/policy-engine/engine"
)

const testTenant = "tenant-a"

func baseSession() *auth.SessionContext {
	return &auth.SessionContext{
		UserID:   "user-1",
		TenantID: testTenant,
		Attributes: auth.SessionAttributes{
			Department: "finance",
			Region:     "us-east-1",
		},
		Roles: []string{"analyst"},
	}
}

func compileOrFatal(t *testing.T, raw string) dsl.ConditionFn {
	t.Helper()
	n, err := dsl.Parse([]byte(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := dsl.Validate(n); err != nil {
		t.Fatalf("validate: %v", err)
	}
	fn, err := dsl.Compile(n)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return fn
}

func TestDecide_DefaultDeny(t *testing.T) {
	d := engine.Decide(testTenant, nil, engine.EvalRequest{
		SessionContext: baseSession(),
		Action:         "SELECT",
		ResourceURI:    "pg://ds1/public/users",
	})
	if d.Effect != engine.EffectDeny {
		t.Fatalf("expected DENY, got %s: %s", d.Effect, d.Reason)
	}
}

func TestDecide_SimpleAllow(t *testing.T) {
	p := engine.Policy{
		ID:       "p1",
		TenantID: testTenant,
		Effect:   "allow",
		Action:   "SELECT",
		AllowedColumns: []string{"id", "name"},
	}
	d := engine.Decide(testTenant, []engine.Policy{p}, engine.EvalRequest{
		SessionContext: baseSession(),
		Action:         "SELECT",
		ResourceURI:    "pg://ds1/public/users",
	})
	if d.Effect != engine.EffectPermit {
		t.Fatalf("expected PERMIT, got %s", d.Effect)
	}
	if len(d.AllowedColumns) == 0 {
		t.Fatal("expected allowed columns")
	}
}

func TestDecide_DenyOverridesAllow(t *testing.T) {
	allow := engine.Policy{ID: "p-allow", TenantID: testTenant, Effect: "allow", Action: "SELECT", AllowedColumns: []string{"id"}}
	deny := engine.Policy{ID: "p-deny", TenantID: testTenant, Effect: "deny", Action: "SELECT"}
	d := engine.Decide(testTenant, []engine.Policy{allow, deny}, engine.EvalRequest{
		SessionContext: baseSession(),
		Action:         "SELECT",
		ResourceURI:    "pg://ds1/public/users",
	})
	if d.Effect != engine.EffectDeny {
		t.Fatalf("expected DENY, got %s", d.Effect)
	}
}

func TestDecide_Determinism(t *testing.T) {
	policies := []engine.Policy{
		{ID: "p1", TenantID: testTenant, Effect: "allow", Action: "SELECT", AllowedColumns: []string{"id"}},
		{ID: "p2", TenantID: testTenant, Effect: "allow", Action: "SELECT", AllowedColumns: []string{"email"}},
	}
	req := engine.EvalRequest{
		SessionContext: baseSession(),
		Action:         "SELECT",
		ResourceURI:    "pg://ds1/public/users",
	}
	d1 := engine.Decide(testTenant, policies, req)
	d2 := engine.Decide(testTenant, policies, req)
	if d1.Effect != d2.Effect {
		t.Fatal("non-deterministic effect")
	}
}

func TestDecide_TenantContainment(t *testing.T) {
	// Policy from a different tenant must never match.
	p := engine.Policy{
		ID:       "other-tenant-policy",
		TenantID: "tenant-b",
		Effect:   "allow",
		Action:   "*",
		AllowedColumns: []string{"id"},
	}
	d := engine.Decide(testTenant, []engine.Policy{p}, engine.EvalRequest{
		SessionContext: baseSession(),
		Action:         "SELECT",
		ResourceURI:    "pg://ds1/public/users",
	})
	if d.Effect != engine.EffectDeny {
		t.Fatal("cross-tenant policy must not grant access")
	}
}

func TestDecide_ConditionMatch(t *testing.T) {
	fn := compileOrFatal(t, `{"field":"subject.department","op":"eq","value":"finance"}`)
	p := engine.Policy{
		ID:                 "p-cond",
		TenantID:           testTenant,
		Effect:             "allow",
		Action:             "SELECT",
		AllowedColumns:     []string{"id"},
		CompiledConditions: fn,
	}
	sc := baseSession()
	sc.Attributes.Department = "finance"
	d := engine.Decide(testTenant, []engine.Policy{p}, engine.EvalRequest{
		SessionContext: sc,
		Action:         "SELECT",
		ResourceURI:    "pg://ds1/public/users",
	})
	if d.Effect != engine.EffectPermit {
		t.Fatalf("expected PERMIT for matching condition, got %s", d.Effect)
	}
}

func TestDecide_ConditionNoMatch(t *testing.T) {
	fn := compileOrFatal(t, `{"field":"subject.department","op":"eq","value":"finance"}`)
	p := engine.Policy{
		ID:                 "p-cond",
		TenantID:           testTenant,
		Effect:             "allow",
		Action:             "SELECT",
		AllowedColumns:     []string{"id"},
		CompiledConditions: fn,
	}
	sc := baseSession()
	sc.Attributes.Department = "hr" // does NOT match
	d := engine.Decide(testTenant, []engine.Policy{p}, engine.EvalRequest{
		SessionContext: sc,
		Action:         "SELECT",
		ResourceURI:    "pg://ds1/public/users",
	})
	if d.Effect != engine.EffectDeny {
		t.Fatalf("expected DENY for non-matching condition, got %s", d.Effect)
	}
}

func TestDecide_WildcardAction(t *testing.T) {
	p := engine.Policy{ID: "p-star", TenantID: testTenant, Effect: "allow", Action: "*", AllowedColumns: []string{"id"}}
	d := engine.Decide(testTenant, []engine.Policy{p}, engine.EvalRequest{
		SessionContext: baseSession(),
		Action:         "DELETE",
		ResourceURI:    "pg://ds1/public/users",
	})
	if d.Effect != engine.EffectPermit {
		t.Fatalf("wildcard action should permit DELETE, got %s", d.Effect)
	}
}

func TestDecide_ResourceHierarchy(t *testing.T) {
	// Policy on table level should match column-level resource URI.
	p := engine.Policy{
		ID:             "p-table",
		TenantID:       testTenant,
		Effect:         "allow",
		Action:         "SELECT",
		ResourcePrefix: "pg://ds1/public/users",
		AllowedColumns: []string{"*"},
	}
	d := engine.Decide(testTenant, []engine.Policy{p}, engine.EvalRequest{
		SessionContext: baseSession(),
		Action:         "SELECT",
		ResourceURI:    "pg://ds1/public/users/email",
	})
	if d.Effect != engine.EffectPermit {
		t.Fatalf("table-level policy should match column URI, got %s", d.Effect)
	}
}

func TestDecide_ObligationMFAStale(t *testing.T) {
	staleTime := time.Now().Add(-10 * time.Minute)
	ob := engine.Obligation{
		Type:   "require_mfa_within",
		Params: map[string]string{"window": "5m"},
	}
	p := engine.Policy{
		ID:             "p-mfa",
		TenantID:       testTenant,
		Effect:         "allow",
		Action:         "SELECT",
		AllowedColumns: []string{"id"},
		Obligations:    []engine.Obligation{ob},
	}
	sc := baseSession()
	sc.MFAAt = &staleTime
	d := engine.Decide(testTenant, []engine.Policy{p}, engine.EvalRequest{
		SessionContext: sc,
		Action:         "SELECT",
		ResourceURI:    "pg://ds1/public/users",
	})
	if d.Effect != engine.EffectDeny {
		t.Fatalf("stale MFA should DENY, got %s: %s", d.Effect, d.Reason)
	}
}

func TestDecide_ColumnMaskMerge(t *testing.T) {
	p := engine.Policy{
		ID:             "p-mask",
		TenantID:       testTenant,
		Effect:         "allow",
		Action:         "SELECT",
		AllowedColumns: []string{"email"},
		ColumnMasks:    map[string]string{"email": "REDACTED"},
	}
	d := engine.Decide(testTenant, []engine.Policy{p}, engine.EvalRequest{
		SessionContext: baseSession(),
		Action:         "SELECT",
		ResourceURI:    "pg://ds1/public/users",
	})
	if d.Effect != engine.EffectPermit {
		t.Fatalf("expected PERMIT, got %s", d.Effect)
	}
	if d.ColumnMasks["email"] != "REDACTED" {
		t.Fatalf("expected mask for email, got %q", d.ColumnMasks["email"])
	}
}

func TestDecide_ExplainMode(t *testing.T) {
	rawCond := `{"field":"subject.department","op":"eq","value":"finance"}`
	condJSON := json.RawMessage(rawCond)
	_ = condJSON
	fn := compileOrFatal(t, rawCond)
	p := engine.Policy{
		ID:                 "p-explain",
		TenantID:           testTenant,
		Effect:             "allow",
		Action:             "SELECT",
		AllowedColumns:     []string{"id"},
		CompiledConditions: fn,
	}
	d := engine.Decide(testTenant, []engine.Policy{p}, engine.EvalRequest{
		SessionContext: baseSession(),
		Action:         "SELECT",
		ResourceURI:    "pg://ds1/public/users",
		ExplainMode:    true,
	})
	if d.Effect != engine.EffectPermit {
		t.Fatalf("expected PERMIT, got %s", d.Effect)
	}
	if len(d.EvaluationTrace) == 0 {
		t.Fatal("expected evaluation trace in explain mode")
	}
}
