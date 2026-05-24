package router_test

import (
	"errors"
	"testing"

	"github.com/governance-platform/retrieval-service/internal/router"
	"github.com/governance-platform/retrieval-service/internal/store"
)

func TestMaxClassification(t *testing.T) {
	cases := []struct {
		input []string
		want  string
	}{
		{[]string{"public", "internal"}, "internal"},
		{[]string{"public", "restricted", "confidential"}, "restricted"},
		{[]string{}, "public"},
		{[]string{"public"}, "public"},
		{[]string{"confidential"}, "confidential"},
	}
	for _, tc := range cases {
		got := router.MaxClassification(tc.input)
		if got != tc.want {
			t.Errorf("MaxClassification(%v) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestRouteRestrictedRefusedWithoutPrivate(t *testing.T) {
	rt := router.New()

	// No routes configured at all → must refuse for restricted.
	_, err := rt.Decide("restricted", nil)
	if err == nil {
		t.Fatal("expected error for restricted without private provider")
	}
	if !errors.Is(err, router.ErrNoPrivateProvider) {
		t.Errorf("expected ErrNoPrivateProvider, got %v", err)
	}
}

func TestRouteRestrictedForcesPrivateOnly(t *testing.T) {
	rt := router.New()

	// Non-private route should be skipped.
	routes := []store.ProviderRoute{
		{ProviderName: "openai", Model: "gpt-4o", PrivateOnly: false, ZeroRetention: false, Priority: 1},
		{ProviderName: "bedrock-private", Model: "claude-sonnet-4-6", PrivateOnly: true, ZeroRetention: true, Priority: 2},
	}
	route, err := rt.Decide("restricted", routes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !route.PrivateOnly {
		t.Errorf("expected private_only route, got provider=%q", route.ProviderName)
	}
	if route.ProviderName != "bedrock-private" {
		t.Errorf("expected bedrock-private, got %q", route.ProviderName)
	}
}

func TestRouteConfidentialRequiresZeroRetention(t *testing.T) {
	rt := router.New()

	routes := []store.ProviderRoute{
		{ProviderName: "openai", Model: "gpt-4o", PrivateOnly: false, ZeroRetention: false, Priority: 1},
		{ProviderName: "anthropic-zdr", Model: "claude-sonnet-4-6", PrivateOnly: false, ZeroRetention: true, Priority: 2},
	}
	route, err := rt.Decide("confidential", routes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !route.ZeroRetention && !route.PrivateOnly {
		t.Errorf("confidential content should go to zero-retention provider, got %q", route.ProviderName)
	}
}

func TestRouteFallbackPublic(t *testing.T) {
	rt := router.New()
	route, err := rt.Decide("public", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if route.ProviderName == "" {
		t.Error("expected fallback provider name")
	}
}

func TestRoutePriorityOrder(t *testing.T) {
	rt := router.New()

	routes := []store.ProviderRoute{
		{ProviderName: "gemini", Model: "flash", PrivateOnly: false, ZeroRetention: false, Priority: 2},
		{ProviderName: "anthropic", Model: "haiku", PrivateOnly: false, ZeroRetention: false, Priority: 1},
	}
	// Routes already ordered by priority ascending per DB query.
	route, err := rt.Decide("public", routes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if route.ProviderName != "anthropic" {
		t.Errorf("expected highest-priority provider 'anthropic', got %q", route.ProviderName)
	}
}

func TestHealthAwareFailover(t *testing.T) {
	rt := &router.Router{
		HealthFn: func(name string) bool {
			return name != "anthropic" // anthropic is "down"
		},
	}

	routes := []store.ProviderRoute{
		{ProviderName: "anthropic", Model: "haiku", Priority: 1},
		{ProviderName: "openai", Model: "gpt-4o-mini", Priority: 2},
	}
	route, err := rt.Decide("public", routes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if route.ProviderName != "openai" {
		t.Errorf("expected failover to openai, got %q", route.ProviderName)
	}
}
