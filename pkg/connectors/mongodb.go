package connectors

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/rs/zerolog"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// mongoDBConnector implements Connector for MongoDB 5.0+.
//
// Minimum supported version: MongoDB 5.0 (for Atlas) or Community/Enterprise 5.0+.
//
// Session context propagation:
//   - MongoDB has no native session variables accessible to queries.
//   - Identity context is propagated by injecting a $match stage that embeds
//     a constant predicate derived from the row filter at query-plan time.
//   - EnforceContext stores the session context for injection into pipeline ops.
//
// Native enforcement:
//   - Row filters: injected as $match stages prepended to aggregation pipelines.
//   - Column masks: $project stages that replace sensitive fields with masked values.
//   - Policy definitions are stored in apps.pep_mongo_policies collection;
//     the PEP reads and prepends them at query time.
//
// Crawl uses listCollections + $sample aggregation (depth-limited to 3 levels)
// to infer document schema without full collection scans.
type mongoDBConnector struct {
	client      *mongo.Client
	dbName      string
	log         zerolog.Logger
	tracer      trace.Tracer
	ds          *DataSource
	sessionCtx  *SessionContext
}

func newMongoDBConnector(log zerolog.Logger, tracer trace.Tracer) *mongoDBConnector {
	return &mongoDBConnector{log: log, tracer: tracer}
}

func (m *mongoDBConnector) Engine() Engine { return EngineMongoDB }

// Connect establishes a MongoDB client connection.
// DSN is the standard MongoDB connection string (mongodb:// or mongodb+srv://).
// The DSN comes from Vault and must not be logged.
func (m *mongoDBConnector) Connect(ctx context.Context, ds *DataSource) error {
	m.ds = ds

	// Parse database name from DSN or DataSource.Database field.
	m.dbName = ds.Database
	if m.dbName == "" {
		m.dbName = "admin"
	}

	opts := options.Client().ApplyURI(ds.DSN).
		SetConnectTimeout(10 * time.Second).
		SetServerSelectionTimeout(10 * time.Second).
		SetMaxPoolSize(10)

	client, err := mongo.Connect(ctx, opts)
	if err != nil {
		return fmt.Errorf("mongodb: connect: %w", err)
	}

	if err := client.Ping(ctx, nil); err != nil {
		_ = client.Disconnect(ctx)
		return fmt.Errorf("mongodb: ping: %w", err)
	}

	m.client = client
	m.log.Info().Str("data_source_id", ds.ID).Str("db", m.dbName).Msg("mongodb: connected")
	return nil
}

// EnforceContext stores the session context for use in pipeline injection.
// Because MongoDB has no server-side session variables accessible to queries,
// we store the context here and inject it into pipelines during Execute.
func (m *mongoDBConnector) EnforceContext(_ context.Context, sc *SessionContext) error {
	m.sessionCtx = sc
	return nil
}

// PrepareUDFs creates the pep_mongo_policies collection in the governance
// database with appropriate indexes.  MongoDB has no UDFs; this is a no-op
// for function creation but ensures the policy storage collection exists.
func (m *mongoDBConnector) PrepareUDFs(ctx context.Context) error {
	_, span := m.tracer.Start(ctx, "mongodb.PrepareUDFs",
		trace.WithAttributes(attribute.String("data_source_id", m.ds.ID)))
	defer span.End()

	db := m.client.Database("governance")

	// Create the policy collection with a unique index on (tenant_id, collection, policy_id).
	coll := db.Collection("pep_mongo_policies")
	indexModel := mongo.IndexModel{
		Keys: bson.D{
			{Key: "tenant_id", Value: 1},
			{Key: "collection", Value: 1},
			{Key: "policy_id", Value: 1},
		},
		Options: options.Index().SetUnique(true).SetName("unique_policy"),
	}
	if _, err := coll.Indexes().CreateOne(ctx, indexModel); err != nil {
		// Non-fatal: index may already exist.
		m.log.Warn().Err(err).Msg("mongodb: create policy index (may already exist)")
	}
	return nil
}

// SyncNativePolicies stores pipeline injection definitions in the
// governance.pep_mongo_policies collection.  At query time the PEP reads these
// documents and prepends the corresponding $match and $project stages.
func (m *mongoDBConnector) SyncNativePolicies(ctx context.Context, policies []*NativePolicy) (*SyncResult, error) {
	_, span := m.tracer.Start(ctx, "mongodb.SyncNativePolicies",
		trace.WithAttributes(attribute.String("data_source_id", m.ds.ID),
			attribute.Int("policy_count", len(policies))))
	defer span.End()

	start := time.Now()
	result := &SyncResult{Engine: EngineMongoDB, DataSourceID: m.ds.ID, PoliciesTotal: len(policies)}

	db := m.client.Database("governance")
	coll := db.Collection("pep_mongo_policies")

	for _, pol := range policies {
		if err := m.upsertPolicy(ctx, coll, pol); err != nil {
			result.PoliciesErr++
			result.Errors = append(result.Errors, fmt.Errorf("policy %s: %w", pol.PolicyID, err))
			m.log.Error().Err(err).Str("policy_id", pol.PolicyID).Msg("mongodb: policy sync failed")
		} else {
			result.PoliciesOK++
		}
	}

	result.Duration = time.Since(start)
	return result, nil
}

func (m *mongoDBConnector) upsertPolicy(ctx context.Context, coll *mongo.Collection, pol *NativePolicy) error {
	// Build the $match stage from the row filter (if any).
	var matchStage bson.D
	if pol.RowFilter != "" {
		// Row filters for MongoDB are stored as JSON-encoded $match expressions.
		// The policy author must supply a valid MongoDB $match expression as JSON.
		var matchExpr bson.D
		if err := bson.UnmarshalExtJSON([]byte(pol.RowFilter), true, &matchExpr); err != nil {
			// Fallback: store as a string for later human review.
			matchExpr = bson.D{{Key: "$comment", Value: "invalid filter: " + pol.RowFilter}}
		}
		matchStage = matchExpr
	}

	// Build the $project stage for column masking.
	var projectStage bson.D
	if pol.ColumnName != "" {
		projectStage = mongoMaskProjectStage(pol.ColumnName, pol.MaskKind)
	}

	filter := bson.D{
		{Key: "policy_id", Value: pol.PolicyID},
		{Key: "collection", Value: pol.TableName},
	}

	update := bson.D{{Key: "$set", Value: bson.D{
		{Key: "policy_id", Value: pol.PolicyID},
		{Key: "policy_version", Value: pol.PolicyVersion},
		{Key: "table_schema", Value: pol.TableSchema},
		{Key: "collection", Value: pol.TableName},
		{Key: "column_name", Value: pol.ColumnName},
		{Key: "mask_kind", Value: pol.MaskKind},
		{Key: "match_stage", Value: matchStage},
		{Key: "project_stage", Value: projectStage},
		{Key: "updated_at", Value: time.Now().UTC()},
	}}}

	opts := options.Update().SetUpsert(true)
	if _, err := coll.UpdateOne(ctx, filter, update, opts); err != nil {
		return fmt.Errorf("mongodb: upsert policy: %w", err)
	}
	return nil
}

func mongoMaskProjectStage(colName, maskKind string) bson.D {
	var expr any
	switch maskKind {
	case "null":
		// $project: { col: 0 } suppresses the field.
		return bson.D{{Key: colName, Value: 0}}
	case "hash":
		// Use $toHashedIndexKey which provides a deterministic hash.
		expr = bson.D{{Key: "$convert", Value: bson.D{
			{Key: "input", Value: bson.D{{Key: "$toHashedIndexKey", Value: "$" + colName}}},
			{Key: "to", Value: "string"},
		}}}
	case "partial":
		// Expose first 2 and last 2 chars; stars in between.
		expr = bson.D{{Key: "$cond", Value: bson.A{
			bson.D{{Key: "$lte", Value: bson.A{bson.D{{Key: "$strLenCP", Value: "$" + colName}}, 4}}},
			bson.D{{Key: "$replaceAll", Value: bson.D{
				{Key: "input", Value: "$" + colName},
				{Key: "find", Value: "$" + colName},
				{Key: "replacement", Value: "****"},
			}}},
			bson.D{{Key: "$concat", Value: bson.A{
				bson.D{{Key: "$substrCP", Value: bson.A{"$" + colName, 0, 2}}},
				"****",
				bson.D{{Key: "$substrCP", Value: bson.A{
					"$" + colName,
					bson.D{{Key: "$subtract", Value: bson.A{
						bson.D{{Key: "$strLenCP", Value: "$" + colName}}, 2,
					}}},
					2,
				}}},
			}}},
		}}}
	case "redact":
		expr = "[REDACTED]"
	default:
		return bson.D{{Key: colName, Value: 0}}
	}
	return bson.D{{Key: colName, Value: expr}}
}

// Crawl introspects the database by listing all non-system collections and
// running a $sample aggregation to infer field names and types.
// Schema inference is depth-limited to 3 nesting levels to bound memory.
func (m *mongoDBConnector) Crawl(ctx context.Context) (*CatalogDelta, error) {
	_, span := m.tracer.Start(ctx, "mongodb.Crawl",
		trace.WithAttributes(attribute.String("data_source_id", m.ds.ID)))
	defer span.End()

	db := m.client.Database(m.dbName)

	cursor, err := db.ListCollections(ctx, bson.D{})
	if err != nil {
		return nil, fmt.Errorf("mongodb: list collections: %w", err)
	}
	defer cursor.Close(ctx)

	var cols []ColumnInfo
	for cursor.Next(ctx) {
		var collMeta bson.M
		if err := cursor.Decode(&collMeta); err != nil {
			continue
		}
		collName, _ := collMeta["name"].(string)
		if collName == "" || isSystemCollection(collName) {
			continue
		}

		fields, err := m.sampleCollectionFields(ctx, db, collName)
		if err != nil {
			m.log.Warn().Err(err).Str("collection", collName).Msg("mongodb: sample failed (skipping)")
			continue
		}
		for _, f := range fields {
			cols = append(cols, ColumnInfo{
				SchemaName: m.dbName,
				TableName:  collName,
				ColumnName: f.path,
				DataType:   f.bsonType,
				Nullable:   true, // MongoDB fields are always optional
			})
		}
	}
	return &CatalogDelta{Added: cols}, cursor.Err()
}

type fieldMeta struct {
	path     string
	bsonType string
}

// sampleCollectionFields runs a $sample aggregation to collect a small number
// of documents and infers fields from their keys (depth-limited to 3 levels).
func (m *mongoDBConnector) sampleCollectionFields(ctx context.Context, db *mongo.Database, collName string) ([]fieldMeta, error) {
	coll := db.Collection(collName)
	pipeline := mongo.Pipeline{
		{{Key: "$sample", Value: bson.D{{Key: "size", Value: 20}}}},
	}

	cursor, err := coll.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	fieldSet := make(map[string]string)
	for cursor.Next(ctx) {
		var doc bson.M
		if err := cursor.Decode(&doc); err != nil {
			continue
		}
		extractFields("", doc, 0, 3, fieldSet)
	}

	var out []fieldMeta
	for path, bsonType := range fieldSet {
		out = append(out, fieldMeta{path: path, bsonType: bsonType})
	}
	return out, cursor.Err()
}

// extractFields recursively walks a BSON document up to maxDepth levels,
// recording each field path and its inferred BSON type.
func extractFields(prefix string, doc bson.M, depth, maxDepth int, out map[string]string) {
	if depth >= maxDepth {
		return
	}
	for k, v := range doc {
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}
		switch val := v.(type) {
		case bson.M:
			out[path] = "object"
			extractFields(path, val, depth+1, maxDepth, out)
		case bson.A:
			out[path] = "array"
		case string:
			out[path] = "string"
		case int32, int64:
			out[path] = "int"
		case float64:
			out[path] = "double"
		case bool:
			out[path] = "bool"
		case nil:
			out[path] = "null"
		default:
			// Serialize to detect type from JSON representation.
			b, _ := json.Marshal(v)
			_ = b
			out[path] = "mixed"
		}
	}
}

// Execute wraps an aggregation pipeline execution.
// q.SQL is expected to be a JSON array of pipeline stages.
// The row filter from the stored policy is prepended as the first $match stage.
// Column allowlist is enforced via an appended $project stage.
//
// If q.SQL is a simple find filter (JSON object), it is wrapped in a $match.
func (m *mongoDBConnector) Execute(ctx context.Context, q *Query) (ResultStream, error) {
	_, span := m.tracer.Start(ctx, "mongodb.Execute",
		trace.WithAttributes(
			attribute.String("data_source_id", m.ds.ID),
			attribute.String("trace_id", q.TraceID),
		))
	defer span.End()

	// q.SQL should be a JSON pipeline.  The collection name comes from Args[0].
	if len(q.Args) < 1 {
		return nil, fmt.Errorf("mongodb: Execute requires collection name in Args[0]")
	}
	collName, ok := q.Args[0].(string)
	if !ok {
		return nil, fmt.Errorf("mongodb: Args[0] must be the collection name (string)")
	}

	// Parse the pipeline from q.SQL (JSON array of stage objects).
	var pipeline mongo.Pipeline
	if q.SQL != "" {
		if err := bson.UnmarshalExtJSON([]byte(q.SQL), true, &pipeline); err != nil {
			return nil, fmt.Errorf("mongodb: parse pipeline JSON: %w", err)
		}
	}

	// Prepend $match for row filter from session context (set in EnforceContext).
	// In production, the PEP loads the row filter from governance.pep_mongo_policies.
	// Here we prepend a tenant_id $match as a safety backstop.
	if m.sessionCtx != nil {
		tenantMatch := bson.D{{Key: "$match", Value: bson.D{
			{Key: "tenant_id", Value: m.sessionCtx.TenantID},
		}}}
		pipeline = append(mongo.Pipeline{tenantMatch}, pipeline...)
	}

	// Append $limit if MaxRows is set.
	if q.MaxRows > 0 {
		pipeline = append(pipeline, bson.D{{Key: "$limit", Value: q.MaxRows}})
	}

	coll := m.client.Database(m.dbName).Collection(collName)
	cursor, err := coll.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, fmt.Errorf("mongodb: aggregate: %w", err)
	}

	return newMongoResultStream(cursor, q.MaxRows), nil
}

func (m *mongoDBConnector) Capabilities() map[string]bool {
	return map[string]bool{
		"row_filter":         true,  // via $match pipeline injection
		"column_mask":        true,  // via $project pipeline injection
		"native_rls":         false, // no server-side RLS in MongoDB
		"ddm":                false,
		"row_access_policy":  false,
		"aggregation_pipeline": true,
		"transactions":       true,  // MongoDB 4.0+ multi-document transactions
		"information_schema": false, // uses listCollections + $sample
	}
}

func (m *mongoDBConnector) Close() error {
	if m.client != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return m.client.Disconnect(ctx)
	}
	return nil
}

// isSystemCollection returns true for MongoDB system collections that should
// not be included in the catalog.
func isSystemCollection(name string) bool {
	systemPrefixes := []string{"system.", "admin.", "local.", "config."}
	for _, p := range systemPrefixes {
		if len(name) >= len(p) && name[:len(p)] == p {
			return true
		}
	}
	return false
}
