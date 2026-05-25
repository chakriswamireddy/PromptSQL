# Oracle Connector

## Minimum Supported Version

Oracle Database 19c (19.3+).  
Oracle Data Redaction requires Oracle Advanced Security option.  
DBMS_RLS (Virtual Private Database) is available in Enterprise Edition and above.

## Required Service Account Permissions

### Crawl user

```sql
-- Read access to ALL_TAB_COLUMNS, ALL_TABLES, ALL_TAB_COMMENTS, ALL_COL_COMMENTS.
GRANT SELECT ON ALL_TAB_COLUMNS TO gov_crawl;
GRANT SELECT ON ALL_TABLES TO gov_crawl;
GRANT SELECT ON ALL_TAB_COMMENTS TO gov_crawl;
GRANT SELECT ON ALL_COL_COMMENTS TO gov_crawl;
```

### Enforcement user (VPD + Data Redaction)

```sql
-- VPD policy management.
GRANT EXECUTE ON DBMS_RLS TO gov_enforce;
-- Data Redaction policy management.
GRANT EXECUTE ON DBMS_REDACT TO gov_enforce;
-- Session management.
GRANT EXECUTE ON DBMS_SESSION TO gov_enforce;
GRANT EXECUTE ON DBMS_APPLICATION_INFO TO gov_enforce;
-- Cryptographic functions for masking.
GRANT EXECUTE ON DBMS_CRYPTO TO gov_enforce;
GRANT CREATE ANY PROCEDURE TO gov_enforce;
GRANT CREATE PROCEDURE TO gov_enforce;
-- Required to create VPD policy functions in GOVERNANCE schema.
GRANT CREATE SESSION TO gov_enforce;
CREATE USER governance IDENTIFIED BY <vault-managed>;
GRANT RESOURCE TO governance;
GRANT EXECUTE ON DBMS_RLS TO governance;
GRANT EXECUTE ON DBMS_REDACT TO governance;
GRANT EXECUTE ON DBMS_CRYPTO TO governance;
```

## SessionContext Propagation

The connector uses a single PL/SQL anonymous block to set the Oracle client identifier
and application info before any query:

```sql
BEGIN
  DBMS_SESSION.SET_IDENTIFIER('tenant_id:user_id:session_id');
  DBMS_APPLICATION_INFO.SET_CLIENT_INFO('role1,role2');
  DBMS_APPLICATION_INFO.SET_MODULE(module_name => 'governance-platform', action_name => 'tenant_id');
END;
```

VPD policy functions read `SYS_CONTEXT('USERENV','CLIENT_IDENTIFIER')` to extract
the tenant identifier from the composite string.

## Native Enforcement Capabilities

| Capability | Supported | Method |
|---|---|---|
| Row filtering | Yes | Oracle VPD (DBMS_RLS) — transparent WHERE clause injection |
| Column masking | Yes | Oracle Data Redaction (DBMS_REDACT) |
| Native RLS | Yes | VPD policies are enforced at the SQL engine level |
| DDM | Yes | Oracle Data Redaction (`FULL`, `PARTIAL`, `NULLIFY`, `REGEXP`) |
| Transactions | Yes | Oracle full ACID with multi-version concurrency |

## Known Limitations

1. **VPD policy function must exist in GOVERNANCE schema**: The enforcement user must
   have CREATE PROCEDURE on the GOVERNANCE schema.
2. **Data Redaction is Enterprise-only**: Community/Standard Edition does not include
   Oracle Advanced Security.  Masking falls back to views in Standard Edition.
3. **DBMS_CRYPTO requires explicit grant**: The `EXECUTE` privilege on DBMS_CRYPTO
   is not granted by default to non-SYS users.
4. **Oracle identifier length**: Object names are limited to 30 characters in
   Oracle 12.1 and below (128 in Oracle 12.2+); the connector truncates to 30.
5. **go-ora driver**: The `github.com/sijms/go-ora/v2` driver does not support
   all Oracle-specific data types (e.g. XMLType, CLOB > 32767 bytes).
