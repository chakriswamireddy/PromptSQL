# BigQuery Connector

## Minimum Supported Version

BigQuery Standard Edition (any — BigQuery is a managed SaaS with no versioning).  
Policy Tags require Data Catalog API to be enabled in the GCP project.

## Required Service Account Permissions

Create a GCP Service Account for the platform and grant it the following IAM roles:

```bash
# Minimum roles for crawl + enforcement.
gcloud projects add-iam-policy-binding PROJECT_ID \
  --member="serviceAccount:gov-enforce@PROJECT_ID.iam.gserviceaccount.com" \
  --role="roles/bigquery.dataEditor"        # read/write datasets and tables (for authorized views)

gcloud projects add-iam-policy-binding PROJECT_ID \
  --member="serviceAccount:gov-enforce@PROJECT_ID.iam.gserviceaccount.com" \
  --role="roles/bigquery.jobUser"           # submit query jobs

gcloud projects add-iam-policy-binding PROJECT_ID \
  --member="serviceAccount:gov-enforce@PROJECT_ID.iam.gserviceaccount.com" \
  --role="roles/bigquery.metadataViewer"    # read INFORMATION_SCHEMA

# For Policy Tags (column-level security):
gcloud projects add-iam-policy-binding PROJECT_ID \
  --member="serviceAccount:gov-enforce@PROJECT_ID.iam.gserviceaccount.com" \
  --role="roles/datacatalog.tagTemplateOwner"
```

The service account JSON key file path is supplied via the DSN credentials_file parameter
or the `GOOGLE_APPLICATION_CREDENTIALS` environment variable (preferred for Workload Identity).

## SessionContext Propagation

BigQuery is fully serverless; there are no connection sessions or server-side session
variables.  Identity context is propagated via:

1. **Query labels**: Each query job is labeled with `gov_tenant_id`, `gov_user_hash`
   (SHA-256 of user_id, truncated), and `gov_session_id`.  Labels appear in
   INFORMATION_SCHEMA.JOBS for audit correlation.
2. **Authorized views**: Row-level access is enforced by authorized views in the
   `governance` dataset that embed constant predicates.  The calling user's identity
   is reflected in BigQuery audit logs via the service account used for the query.

## Native Enforcement Capabilities

| Capability | Supported | Method |
|---|---|---|
| Row filtering | Yes | Authorized views with WHERE clauses |
| Column masking | Yes | Authorized views with SQL masking expressions |
| Native RLS | No | No traditional RLS; authorized views are the equivalent |
| Policy Tags | Stub | Column-level IAM via Data Catalog Policy Tags (partial — see limitations) |
| Authorized views | Yes | `CREATE VIEW governance.gov_<name> AS SELECT ...` |
| Transactions | No | BigQuery does not support DDL transactions |

## Known Limitations

1. **Policy Tags require Data Catalog IAM binding**: The full Policy Tag integration
   requires creating a taxonomy, tags, and binding them to columns via the
   Data Catalog API.  This is a stub in Phase 11 and tracked for Phase 15.
2. **Authorized views are dataset-scoped**: The `governance` dataset must be in the
   same GCP project as the target dataset, or cross-project dataset access must be
   explicitly configured.
3. **BigQuery billing**: Every `Execute` call submits a billed query job.  Ensure
   the service account is in a project with appropriate budget alerts.
4. **Result pre-fetching**: The BigQuery Go client uses a REST API; the connector
   eagerly fetches up to `MaxRows` rows.  Large result sets should set a reasonable
   `MaxRows` to avoid memory pressure.
5. **DSN format**: BigQuery uses a non-standard `bigquery://project-id?credentials_file=path`
   URI rather than a traditional DSN.  Vault must store this URI, not a JDBC string.
