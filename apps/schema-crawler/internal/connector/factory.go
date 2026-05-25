// Package connector bridges the schema-crawler service to the pkg/connectors
// abstraction.  This factory wraps pkg/connectors.NewConnector so the crawler
// can work with any supported engine, not just PostgreSQL.
package connector

import (
	"context"
	"fmt"

	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel"

	"github.com/governance-platform/pkg/connectors"
)

// Engine is re-exported from pkg/connectors for use in schema-crawler config.
type Engine = connectors.Engine

// NewConnectorForEngine creates a pkg/connectors.Connector for the given
// engine type and connects it using the provided DSN.
//
// The DSN must come from Vault via resolveVaultDSN in main.go — never from
// an untrusted source.
func NewConnectorForEngine(
	ctx context.Context,
	engine Engine,
	dataSourceID string,
	dsn string,
	database string,
	schema string,
	secretRef string,
	log zerolog.Logger,
) (connectors.Connector, error) {
	ds := &connectors.DataSource{
		ID:        dataSourceID,
		Engine:    engine,
		DSN:       dsn,
		Database:  database,
		Schema:    schema,
		SecretRef: secretRef,
	}

	tracer := otel.Tracer("schema-crawler")

	conn, err := connectors.NewConnector(ctx, ds, log, tracer)
	if err != nil {
		return nil, fmt.Errorf("connector factory: %w", err)
	}

	if err := conn.Connect(ctx, ds); err != nil {
		return nil, fmt.Errorf("connector connect [%s]: %w", engine, err)
	}

	return conn, nil
}
