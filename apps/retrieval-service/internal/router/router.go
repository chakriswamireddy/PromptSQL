// Package router determines the appropriate LLM provider for a given content classification.
package router

import (
	"errors"
	"fmt"

	"github.com/governance-platform/retrieval-service/internal/store"
)

// classificationRank maps classification level to a comparable integer.
// Higher = more sensitive.
var classificationRank = map[string]int{
	"public":       0,
	"internal":     1,
	"confidential": 2,
	"restricted":   3,
}

// ErrNoPrivateProvider is returned when restricted content must be routed but
// no private-cloud provider is configured.
var ErrNoPrivateProvider = errors.New("no private provider configured for restricted content")

// ErrRouteUnavailable is returned when the chosen provider is temporarily
// unavailable (used by health-aware failover).
var ErrRouteUnavailable = errors.New("provider route unavailable")

// Route is the resolved routing decision.
type Route struct {
	ProviderName    string
	Model           string
	ZeroRetention   bool
	PrivateOnly     bool
	ResidencyRegion string
	// ContentClassification is the max classification across all content.
	ContentClassification string
}

// Router resolves LLM provider routes.
type Router struct {
	// HealthFn optionally checks provider health; nil means always healthy.
	HealthFn func(providerName string) bool
}

func New() *Router { return &Router{} }

// MaxClassification returns the highest classification across a list of strings.
func MaxClassification(classifications []string) string {
	max := "public"
	maxRank := -1
	for _, c := range classifications {
		if r, ok := classificationRank[c]; ok && r > maxRank {
			maxRank = r
			max = c
		}
	}
	return max
}

// Decide picks the best available route for the given content classification,
// respecting per-tenant provider configs and health status.
func (r *Router) Decide(contentClassification string, routes []store.ProviderRoute) (Route, error) {
	if len(routes) == 0 {
		return r.fallback(contentClassification)
	}

	rank := classificationRank[contentClassification]

	for _, rt := range routes {
		// Restricted content MUST go to a private-only provider.
		if contentClassification == "restricted" && !rt.PrivateOnly {
			continue
		}
		// Confidential content requires zero-retention.
		if rank >= classificationRank["confidential"] && !rt.ZeroRetention && !rt.PrivateOnly {
			continue
		}
		// Health check.
		if r.HealthFn != nil && !r.HealthFn(rt.ProviderName) {
			continue
		}
		return Route{
			ProviderName:          rt.ProviderName,
			Model:                 rt.Model,
			ZeroRetention:         rt.ZeroRetention,
			PrivateOnly:           rt.PrivateOnly,
			ResidencyRegion:       rt.ResidencyRegion,
			ContentClassification: contentClassification,
		}, nil
	}

	// No configured route satisfied the constraints.
	if contentClassification == "restricted" {
		return Route{}, fmt.Errorf("%w: classification=%s", ErrNoPrivateProvider, contentClassification)
	}
	return r.fallback(contentClassification)
}

// fallback returns hard-coded defaults when per-tenant routes are not configured.
func (r *Router) fallback(classification string) (Route, error) {
	defaults := map[string]Route{
		"public": {
			ProviderName:          "anthropic",
			Model:                 "claude-haiku-4-5-20251001",
			ContentClassification: "public",
		},
		"internal": {
			ProviderName:          "anthropic",
			Model:                 "claude-sonnet-4-6",
			ContentClassification: "internal",
		},
		"confidential": {
			ProviderName:          "anthropic",
			Model:                 "claude-sonnet-4-6",
			ZeroRetention:         true,
			ContentClassification: "confidential",
		},
	}
	if rt, ok := defaults[classification]; ok {
		return rt, nil
	}
	// restricted without a configured private provider — hard refuse.
	return Route{}, fmt.Errorf("%w: no fallback for classification=%s", ErrNoPrivateProvider, classification)
}
