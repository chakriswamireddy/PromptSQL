# MongoDB Connector

## Minimum Supported Version

MongoDB 5.0+ (Community, Enterprise, or Atlas).  
Multi-document transactions require MongoDB 4.0+ with replica set or sharded cluster.  
Atlas Search / Vector Search features are not used by this connector.

## Required Service Account Permissions

Create a MongoDB user with read permissions on target databases and
read/write permissions on the `governance` database:

```javascript
// In the admin database:
db.createUser({
  user: "gov_enforce",
  pwd: "<vault-managed>",
  roles: [
    // Read all databases to crawl:
    { role: "readAnyDatabase", db: "admin" },
    // Read/write governance database for policy storage:
    { role: "readWrite", db: "governance" },
    // Required for listCollections:
    { role: "listCollections", db: "admin" }
  ]
});
```

For Atlas, use Atlas Database Users with built-in roles or custom roles scoped
to the project.

## SessionContext Propagation

MongoDB has no server-side session variables accessible to queries.  Identity
context is propagated via two mechanisms:

1. **In-process context**: The connector stores the `SessionContext` in the
   `mongoDBConnector` struct during `EnforceContext`.  This context is injected
   into every aggregation pipeline as a `$match` stage prepended to the user's pipeline.

2. **Tenant isolation backstop**: Every `Execute` call prepends:
   ```json
   { "$match": { "tenant_id": "<tenant-uuid>" } }
   ```
   This is a safety backstop; the actual row filter from `governance.pep_mongo_policies`
   is loaded and prepended by the PEP service layer.

## Native Enforcement Capabilities

| Capability | Supported | Method |
|---|---|---|
| Row filtering | Yes | `$match` stage prepended to aggregation pipeline |
| Column masking | Yes | `$project` stage with mask expressions (null, $toHashedIndexKey, etc.) |
| Native RLS | No | MongoDB has no server-side RLS |
| Aggregation pipeline | Yes | Full MongoDB aggregation pipeline supported |
| Transactions | Yes | Multi-document ACID transactions (replica set required) |
| DDM | No | No native Dynamic Data Masking |

## Policy Storage

Row filter and column mask definitions are stored in the `governance.pep_mongo_policies`
collection with the following schema:

```json
{
  "policy_id":      "uuid",
  "policy_version": "v42",
  "table_schema":   "mydb",
  "collection":     "orders",
  "column_name":    "ssn",
  "mask_kind":      "hash",
  "match_stage":    { "tenant_id": "..." },
  "project_stage":  { "ssn": { "$toHashedIndexKey": "$ssn" } },
  "updated_at":     ISODate("...")
}
```

## Known Limitations

1. **No server-side RLS**: All enforcement is application-side (pipeline injection).
   A malicious actor with direct `mongosh` access bypasses enforcement.  Use network
   policies and MongoDB Atlas IP access lists as defence-in-depth.
2. **Row filter syntax is MongoDB BSON**: Unlike SQL engines, row filters for MongoDB
   must be written as MongoDB `$match` expression JSON, not ANSI SQL predicates.
   The policy authoring UI must provide a MongoDB-specific filter editor.
3. **Schema inference via $sample**: The `Crawl` implementation uses `$sample` to
   infer schema from a random subset of documents.  Collections with highly variable
   document shapes may produce incomplete schema metadata.
4. **Depth limit of 3**: Nested document fields beyond 3 levels are not crawled.
   Adjust `sampleCollectionFields` `maxDepth` parameter if deeper crawling is needed.
5. **`listCollections` requires read on admin**: In some deployments, the user requires
   the `listDatabases` privilege on the `admin` database to enumerate all databases.
6. **mongoResultStream serializes to JSON**: The `ResultStream` interface returns
   MongoDB documents as JSON strings in the `_doc` column.  Callers must deserialize
   this JSON for structured access.
