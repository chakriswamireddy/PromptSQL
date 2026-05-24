// Package featureflags wraps the Unleash Go SDK and provides a thin interface
// used by all platform services. Every feature must be checked here — never
// call the SDK directly from service code.
package featureflags

import (
	"context"
	"fmt"

	unleash "github.com/Unleash/unleash-client-go/v4"
)

// Client is the platform feature-flag client.
type Client struct {
	appName string
}

// Config is loaded from environment variables.
type Config struct {
	UnleashURL   string // e.g. "http://unleash:4242/api"
	APIToken     string
	AppName      string // service name, used as context appName
	Environment  string // "local" | "dev" | "staging" | "prod"
}

// New initialises the Unleash client and synchronously polls once.
func New(ctx context.Context, cfg Config) (*Client, error) {
	err := unleash.Initialize(
		unleash.WithUrl(cfg.UnleashURL),
		unleash.WithAppName(cfg.AppName),
		unleash.WithCustomHeaders(map[string][]string{
			"Authorization": {cfg.APIToken},
		}),
		unleash.WithEnvironment(cfg.Environment),
		unleash.WithSynchronousFetch(true),
	)
	if err != nil {
		return nil, fmt.Errorf("init unleash: %w", err)
	}
	return &Client{appName: cfg.AppName}, nil
}

// IsEnabled returns true if the named flag is active for the given context.
// ctx must contain a tenant.id value if the flag uses a tenant strategy.
func (c *Client) IsEnabled(flag string, ctx ...unleash.FeatureOption) bool {
	return unleash.IsEnabled(flag, ctx...)
}

// Variant returns the variant for the flag, or a disabled variant if off.
func (c *Client) Variant(flag string, ctx ...unleash.FeatureOption) unleash.Variant {
	return unleash.GetVariant(flag, ctx...)
}

// Close shuts down the background polling goroutine.
func (c *Client) Close() {
	unleash.Close()
}

// WithTenantContext returns an Unleash FeatureOption that injects the tenant
// identifier so tenant-scoped strategies resolve correctly.
func WithTenantContext(tenantID string) unleash.FeatureOption {
	return unleash.WithContext(unleash.Context{
		Properties: map[string]string{"tenantId": tenantID},
	})
}
