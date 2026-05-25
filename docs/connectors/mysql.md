# MySQL Connector

## Minimum Supported Version

MySQL 8.0.19+ (Community or Enterprise).  
Tested with Aurora MySQL 3.x (MySQL 8.0 compatible).

## Required Service Account Permissions

The platform creates a read-only crawl user and a write user for UDF/view management.

### Crawl user (read-only)

```sql
GRANT SELECT ON information_schema.* TO 'gov_crawl'@'%';
GRANT SELECT ON performance_schema.* TO 'gov_crawl'@'%';
-- Grant SELECT on each target database to crawl:
GRANT SELECT ON `mydb`.* TO 'gov_crawl'@'%';
```

### Enforcement user (for SyncNativePolicies)

```sql
-- Must be able to create views in the governance schema and call UDFs.
GRANT CREATE VIEW, SHOW VIEW ON governance.* TO 'gov_enforce'@'%';
GRANT EXECUTE ON governance.* TO 'gov_enforce'@'%';
GRANT CREATE ROUTINE, ALTER ROUTINE ON governance.* TO 'gov_enforce'@'%';
GRANT SELECT ON `mydb`.* TO 'gov_enforce'@'%';
```

## SessionContext Propagation

MySQL does not support per-connection named session variables accessible to DDL objects.
The connector sets user-defined variables (`@app_tenant`, `@app_user`, `@app_session`,
`@app_roles`) via parameterized `SET @var = ?` statements before each query batch.

Views and stored procedures created by `SyncNativePolicies` reference these variables
in their WHERE clauses for row filtering.

**Important**: Because MySQL user-defined variables are per-connection and persist for
the session, the connection pool MUST NOT reuse connections without resetting these
variables.  The connector uses a dedicated connection per request for enforcement paths.

## Native Enforcement Capabilities

| Capability | Supported | Method |
|---|---|---|
| Row filtering | Yes | `CREATE OR REPLACE VIEW governance.gov_<name> AS SELECT ... WHERE @app_tenant = ...` |
| Column masking | Yes | View uses `governance.mask_*()` scalar functions |
| Native RLS | No | MySQL has no built-in RLS; views enforce access |
| DDM | No | No native Dynamic Data Masking in MySQL |
| Transactions | Yes | InnoDB supports full ACID transactions |

## Known Limitations

1. **No native RLS**: All row-level enforcement is view-based.  Applications must be
   directed to query `governance.*` views instead of base tables.
2. **User-defined variables are connection-scoped**: The connection pool must reset
   session variables on return, or use dedicated connections per request.
3. **`CREATE OR REPLACE FUNCTION` requires MySQL 8.0.19+**: Earlier versions require
   `DROP FUNCTION IF EXISTS` + `CREATE FUNCTION` (not atomic).
4. **No schema-level CREATE FUNCTION privilege**: The enforcement user requires
   `CREATE ROUTINE` on the `governance` schema, not the target schema.
5. **DEFINER clause**: Masking functions use `SQL SECURITY DEFINER`; the creating
   user must have `SUPER` or `SET_USER_ID` privilege in MySQL 8.0.
