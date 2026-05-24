package engine_test

// Property-based tests verifying the four core invariants of the decision algorithm:
//   1. Monotonicity: removing an allow never grants more; removing a deny never restricts more.
//   2. Determinism: same inputs always produce same output.
//   3. Tenant containment: a policy from tenant B never affects tenant A decisions.
//   4. Ordering invariance: shuffling the policy list does not change the outcome.

import (
	"math/rand"
	"testing"

	"github.com/governance-platform/policy-engine/engine"
)

const propTenant = "prop-tenant"

func makePolicies(n int, effect string) []engine.Policy {
	policies := make([]engine.Policy, n)
	for i := range policies {
		policies[i] = engine.Policy{
			ID:             randomID(),
			TenantID:       propTenant,
			Effect:         effect,
			Action:         "*",
			AllowedColumns: []string{"id"},
		}
	}
	return policies
}

func randomID() string {
	const letters = "abcdefghijklmnopqrstuvwxyz"
	b := make([]byte, 8)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return string(b)
}

func baseReq() engine.EvalRequest {
	return engine.EvalRequest{
		SessionContext: baseSession(),
		Action:         "SELECT",
		ResourceURI:    "pg://ds1/public/t",
	}
}

// Property 1a: Adding a deny can only keep or turn PERMIT → DENY, never DENY → PERMIT.
func TestProperty_AddingDenyNeverPermits(t *testing.T) {
	for iter := 0; iter < 200; iter++ {
		nAllow := rand.Intn(5)
		policies := makePolicies(nAllow, "allow")
		req := baseReq()

		before := engine.Decide(propTenant, policies, req)

		denyPolicy := engine.Policy{ID: "new-deny", TenantID: propTenant, Effect: "deny", Action: "*"}
		after := engine.Decide(propTenant, append(policies, denyPolicy), req)

		if before.Effect == engine.EffectDeny && after.Effect == engine.EffectPermit {
			t.Errorf("iter %d: adding deny turned DENY into PERMIT", iter)
		}
	}
}

// Property 1b: Removing an allow never grants more (outcome can only stay or become more restrictive).
func TestProperty_RemovingAllowNeverGrants(t *testing.T) {
	for iter := 0; iter < 200; iter++ {
		nAllow := rand.Intn(5) + 1
		policies := makePolicies(nAllow, "allow")
		req := baseReq()

		before := engine.Decide(propTenant, policies, req)

		// Remove one allow.
		subset := policies[:len(policies)-1]
		after := engine.Decide(propTenant, subset, req)

		if before.Effect == engine.EffectDeny && after.Effect == engine.EffectPermit {
			t.Errorf("iter %d: removing allow turned DENY into PERMIT", iter)
		}
	}
}

// Property 2: Determinism — same inputs always produce same Effect.
func TestProperty_Determinism(t *testing.T) {
	policies := makePolicies(3, "allow")
	req := baseReq()
	ref := engine.Decide(propTenant, policies, req)
	for i := 0; i < 100; i++ {
		d := engine.Decide(propTenant, policies, req)
		if d.Effect != ref.Effect {
			t.Fatalf("iteration %d: non-deterministic — expected %s got %s", i, ref.Effect, d.Effect)
		}
	}
}

// Property 3: Tenant containment — other-tenant policies never affect decisions.
func TestProperty_TenantContainment(t *testing.T) {
	otherPolicies := makePolicies(5, "allow")
	for i := range otherPolicies {
		otherPolicies[i].TenantID = "other-tenant"
	}
	req := baseReq()

	baseline := engine.Decide(propTenant, nil, req) // no policies → DENY
	withOther := engine.Decide(propTenant, otherPolicies, req)

	if baseline.Effect != withOther.Effect {
		t.Fatalf("cross-tenant policies affected decision: baseline=%s withOther=%s", baseline.Effect, withOther.Effect)
	}
}

// Property 4: Ordering invariance — shuffling policies does not change outcome.
func TestProperty_OrderingInvariance(t *testing.T) {
	policies := append(makePolicies(3, "allow"), makePolicies(1, "deny")...)
	req := baseReq()
	ref := engine.Decide(propTenant, policies, req)

	for iter := 0; iter < 100; iter++ {
		shuffled := make([]engine.Policy, len(policies))
		copy(shuffled, policies)
		rand.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		d := engine.Decide(propTenant, shuffled, req)
		if d.Effect != ref.Effect {
			t.Fatalf("iter %d: ordering changed effect from %s to %s", iter, ref.Effect, d.Effect)
		}
	}
}
