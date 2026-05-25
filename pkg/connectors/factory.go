package connectors

import (
	"context"
	"fmt"

	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel/trace"
)

// NewConnector returns a Connector implementation appropriate for ds.Engine.
// The connector is not yet connected; callers must invoke Connect after this.
//
// logger and tracer are injected so each implementation can emit structured
// logs and OTel spans without reaching into global state.
func NewConnector(
	_ context.Context,
	ds *DataSource,
	logger zerolog.Logger,
	tracer trace.Tracer,
) (Connector, error) {
	if _, ok := ValidEngines[ds.Engine]; !ok {
		return nil, fmt.Errorf("connectors: unknown engine %q", ds.Engine)
	}

	log := logger.With().
		Str("engine", string(ds.Engine)).
		Str("data_source_id", ds.ID).
		Logger()

	switch ds.Engine {
	case EnginePostgres:
		return newPostgresConnector(log, tracer), nil
	case EngineMySQL:
		return newMySQLConnector(log, tracer), nil
	case EngineSQLServer:
		return newSQLServerConnector(log, tracer), nil
	case EngineOracle:
		return newOracleConnector(log, tracer), nil
	case EngineSnowflake:
		return newSnowflakeConnector(log, tracer), nil
	case EngineBigQuery:
		return newBigQueryConnector(log, tracer), nil
	case EngineDatabricks:
		return newDatabricksConnector(log, tracer), nil
	case EngineMongoDB:
		return newMongoDBConnector(log, tracer), nil
	default:
		// Should be unreachable due to ValidEngines check above.
		return nil, fmt.Errorf("connectors: unhandled engine %q", ds.Engine)
	}
}
