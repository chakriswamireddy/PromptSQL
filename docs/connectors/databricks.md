# Databricks Connector

## Minimum Supported Version

Databricks Runtime 12.0 LTS or higher with Unity Catalog enabled.  
Row Filters and Column Masks (Unity Catalog) require:
- Databricks Runtime 12.2 LTS+ (12.0 for preview, 12.2 for GA).
- Unity Catalog metastore enabled on the workspace.
- Databricks SQL Serverless or a running SQL Warehouse.

## Required Service Account Permissions

Create a Databricks Service Principal and grant Unity Catalog privileges:

```sql
-- On the governance catalog/schema (Unity Catalog SQL):
GRANT CREATE FUNCTION ON SCHEMA governance TO `gov-service-principal`;
GRANT USE SCHEMA ON SCHEMA governance TO `gov-service-principal`;
GRANT USE CATALOG ON CATALOG <catalog> TO `gov-service-principal`;

-- On target schemas (to create/alter row filters and column masks):
GRANT USE SCHEMA ON SCHEMA <target_schema> TO `gov-service-principal`;
GRANT ALTER ON TABLE <target_schema>.<table> TO `gov-service-principal`;

-- For crawl (system.information_schema):
GRANT SELECT ON system.information_schema.columns TO `gov-service-principal`;
```

The DSN uses the Databricks SQL Go driver format:
```
token:<personal-access-token>@<workspace-host>:443/sql/1.0/warehouses/<warehouse-id>
```

The personal access token or OAuth M2M token must be stored in Vault.

## SessionContext Propagation

The connector sets Databricks SQL session variables before executing queries:

```sql
SET `gov.tenant_id`  = 'tenant-uuid';
SET `gov.user_id`    = 'user-uuid';
SET `gov.session_id` = 'session-uuid';
SET `gov.roles`      = 'role1,role2';
```

Unity Catalog Row Filters and Column Mask functions can read these via
`session_context('gov.tenant_id')` in Databricks Runtime 14.1+.

## Native Enforcement Capabilities

| Capability | Supported | Method |
|---|---|---|
| Row filtering | Yes | Unity Catalog Row Filters (`ALTER TABLE ... SET ROW FILTER`) |
| Column masking | Yes | Unity Catalog Column Masks (`ALTER TABLE ... ALTER COLUMN ... SET MASK`) |
| Native RLS | No | No traditional RLS; row filters are the equivalent |
| Unity Catalog | Yes | Row filters and column masks are Unity Catalog features |
| DDM | No | No Dynamic Data Masking outside Unity Catalog masks |
| Transactions | No | Databricks SQL does not support DDL transactions |

## Known Limitations

1. **Unity Catalog required**: Row Filters and Column Masks are Unity Catalog-only
   features.  Databricks workspaces without Unity Catalog must use view-based enforcement.
2. **Row filter functions cannot have parameters in older runtimes**: In Databricks
   Runtime < 14.1, row filter functions cannot accept arguments; the tenant filter
   must be a constant expression or use session variables.
3. **Column mask accepts a single column argument**: Each mask function receives only
   the column value.  Cross-column masking logic must be embedded in the function body.
4. **DROP MASK before SET MASK**: The connector drops the existing mask before setting
   a new one (not atomic).  A brief enforcement gap exists during sync.
5. **databricks-sql-go driver maturity**: The Go driver is newer than the Python and
   Java SDKs.  Some Databricks features (e.g., complex type column access) may not
   be fully supported.
