# Snowflake Connector

## Minimum Supported Version

Snowflake (SaaS — version is always current).  
Row Access Policies (RAP) require Standard Edition or higher.  
Dynamic Data Masking (DDM) requires Enterprise Edition or higher.

## Required Service Account Permissions

```sql
-- Create a dedicated role for the platform.
CREATE ROLE GOV_ENFORCE;

-- Grant schema creation for the governance database/schema.
GRANT CREATE SCHEMA ON DATABASE <target_db> TO ROLE GOV_ENFORCE;
GRANT USAGE ON DATABASE <target_db> TO ROLE GOV_ENFORCE;
GRANT USAGE ON SCHEMA <target_db>.<target_schema> TO ROLE GOV_ENFORCE;

-- Grant CREATE ROW ACCESS POLICY and CREATE MASKING POLICY.
GRANT CREATE ROW ACCESS POLICY ON SCHEMA governance TO ROLE GOV_ENFORCE;
GRANT CREATE MASKING POLICY ON SCHEMA governance TO ROLE GOV_ENFORCE;

-- Grant APPLY privileges to attach policies to target tables.
GRANT APPLY ROW ACCESS POLICY ON ACCOUNT TO ROLE GOV_ENFORCE;
GRANT APPLY MASKING POLICY ON ACCOUNT TO ROLE GOV_ENFORCE;

-- For crawl: read INFORMATION_SCHEMA.
GRANT SELECT ON ALL TABLES IN SCHEMA <target_db>.INFORMATION_SCHEMA TO ROLE GOV_ENFORCE;
GRANT USAGE ON ALL SCHEMAS IN DATABASE <target_db> TO ROLE GOV_ENFORCE;
GRANT SELECT ON ALL TABLES IN DATABASE <target_db> TO ROLE GOV_ENFORCE;

-- Assign role to the service account user.
GRANT ROLE GOV_ENFORCE TO USER GOV_SERVICE_ACCOUNT;
ALTER USER GOV_SERVICE_ACCOUNT SET DEFAULT_ROLE = GOV_ENFORCE;
```

## SessionContext Propagation

Snowflake is serverless; there are no persistent session variables in the traditional
sense.  The connector uses `ALTER SESSION SET <var> = ?` to store governance context
as session parameters:

```sql
ALTER SESSION SET GOV_TENANT_ID = 'tenant-uuid';
ALTER SESSION SET GOV_USER_ID   = 'user-uuid';
ALTER SESSION SET GOV_SESSION_ID = 'session-uuid';
ALTER SESSION SET GOV_ROLES     = 'role1,role2';
```

Row Access Policies and Masking Policies can reference these via:

```sql
-- In RAP body:
CURRENT_SESSION() -- returns the session identifier (not the same as GOV_SESSION_ID)
-- Or via a JavaScript UDF that calls SYSTEM$GET_SESSION_VARIABLE().
```

**Note**: Snowflake session parameters are account-level or session-level; they are not
accessible directly inside RAP/DDM SQL bodies in all editions.  The platform stores
tenant context in a Snowflake Secure Function as a fallback.

## Native Enforcement Capabilities

| Capability | Supported | Method |
|---|---|---|
| Row filtering | Yes | Row Access Policies (`CREATE ROW ACCESS POLICY`) |
| Column masking | Yes | Dynamic Data Masking (`CREATE MASKING POLICY`) |
| Native RLS | No | No traditional RLS; RAP is the equivalent |
| Row Access Policy | Yes | Attached via `ALTER TABLE ... ADD ROW ACCESS POLICY` |
| DDM | Yes | `ALTER TABLE ... MODIFY COLUMN ... SET MASKING POLICY` |
| Transactions | No | DDL is auto-committed; DML supports multi-statement transactions |

## Known Limitations

1. **DDM requires Enterprise Edition**: Standard Edition only supports RAP.
2. **RAP policy functions cannot reference external tables**: The row filter must be a
   pure SQL expression or reference only the current session context.
3. **Detaching existing policies**: The connector issues `DROP ALL ROW ACCESS POLICIES`
   before re-attaching, which creates a brief enforcement gap.
4. **Snowflake account-level `APPLY` privilege**: Granting `APPLY ROW ACCESS POLICY ON ACCOUNT`
   is a powerful privilege; restrict it to the governance service account only.
5. **gosnowflake driver latency**: Snowflake HTTP REST connections have higher latency
   than TCP connections.  Increase `SyncTimeoutPerSource` for large policy sets.
