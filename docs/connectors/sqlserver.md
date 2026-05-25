# SQL Server Connector

## Minimum Supported Version

SQL Server 2019 (15.x) or Azure SQL Database (all tiers).  
Dynamic Data Masking requires SQL Server 2016+.  
Row-Level Security via Security Policies requires SQL Server 2016+.

## Required Service Account Permissions

### Crawl user

```sql
-- Grant VIEW DEFINITION to read INFORMATION_SCHEMA and sys.columns.
GRANT VIEW DEFINITION ON SCHEMA::dbo TO gov_crawl;
GRANT VIEW DATABASE STATE TO gov_crawl;
```

### Enforcement user

```sql
-- Schema-level permissions for governance objects.
CREATE SCHEMA governance;
GRANT ALTER ON SCHEMA::governance TO gov_enforce;
GRANT CREATE FUNCTION TO gov_enforce;
GRANT CREATE PROCEDURE TO gov_enforce;
GRANT CREATE SECURITY POLICY TO gov_enforce;
-- DDM: ALTER TABLE permission on target tables.
GRANT ALTER ON SCHEMA::dbo TO gov_enforce;
-- Required for sp_set_session_context calls:
-- No special permission needed — any user can call sp_set_session_context.
```

## SessionContext Propagation

The connector calls `sp_set_session_context` to store tenant_id, user_id, session_id,
and roles in the connection's session dictionary:

```sql
EXEC sp_set_session_context @key = N'tenant_id', @value = @p1, @read_only = 0;
EXEC sp_set_session_context @key = N'user_id',   @value = @p2, @read_only = 0;
```

Security predicates (Row-Level Security filter functions) read these values via
`SESSION_CONTEXT(N'tenant_id')`.

## Native Enforcement Capabilities

| Capability | Supported | Method |
|---|---|---|
| Row filtering | Yes | `CREATE SECURITY POLICY` with `ADD FILTER PREDICATE` |
| Column masking | Yes | `ALTER TABLE ... ALTER COLUMN ... MASKED WITH (FUNCTION = ...)` |
| Native RLS | Yes | SQL Server Row-Level Security (Security Policies) |
| DDM | Yes | Dynamic Data Masking — `default()`, `email()`, `partial()` |
| Transactions | Yes | Full DDL + DML transactional support |

## Known Limitations

1. **DDM requires unmask permission**: Users with `UNMASK` permission bypass DDM.
   The platform enforces that application service accounts do not hold `UNMASK`.
2. **Security Policy is not `CREATE OR ALTER`**: The connector drops and re-creates
   the SECURITY POLICY on each sync (non-atomic for a brief window).
3. **`CREATE OR ALTER FUNCTION`** requires SQL Server 2016+.
4. **Azure SQL Database**: The `EXECUTE AS` clause in security predicates requires
   the connecting user to have `IMPERSONATE` on the DEFINER user.
5. **Column DDM is additive**: Running `ALTER COLUMN ... ADD MASKED` on an already-
   masked column raises an error; the connector logs a warning and continues.
