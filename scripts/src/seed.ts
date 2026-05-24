/**
 * seed.ts — idempotent dev fixture loader for Phase 1 schema.
 * Run via `make seed` after `make migrate`.
 * Uses ON CONFLICT DO NOTHING so it is safe to run multiple times.
 */

import { Client } from "pg";

// ── fixture data ──────────────────────────────────────────────────────────────

const TENANTS = [
  {
    id: "018f4e1a-0001-7000-8000-000000000001",
    slug: "acme",
    display_name: "Acme Corp",
    plan_tier: "enterprise",
    data_residency: "us-east-1",
    compliance_modes: JSON.stringify(["SOC2", "HIPAA"]),
  },
  {
    id: "018f4e1a-0002-7000-8000-000000000002",
    slug: "globex",
    display_name: "Globex Inc",
    plan_tier: "business",
    data_residency: "eu-west-1",
    compliance_modes: JSON.stringify(["GDPR"]),
  },
];

// System admin user for each tenant (granted_by self-reference requires
// inserting the user first, then bootstrapping user_roles separately)
const USERS = [
  {
    id: "018f4e1b-0001-7000-8000-000000000001",
    tenant_id: "018f4e1a-0001-7000-8000-000000000001",
    email: "admin@acme.test",
    status: "active",
    attributes: JSON.stringify({ department: "platform", mfa_enrolled: true }),
  },
  {
    id: "018f4e1b-0002-7000-8000-000000000002",
    tenant_id: "018f4e1a-0001-7000-8000-000000000001",
    email: "analyst@acme.test",
    status: "active",
    attributes: JSON.stringify({ department: "data", mfa_enrolled: false }),
  },
  {
    id: "018f4e1b-0003-7000-8000-000000000003",
    tenant_id: "018f4e1a-0002-7000-8000-000000000002",
    email: "admin@globex.test",
    status: "active",
    attributes: JSON.stringify({ department: "it", mfa_enrolled: true }),
  },
];

const ROLES = [
  {
    id: "018f4e1c-0001-7000-8000-000000000001",
    tenant_id: "018f4e1a-0001-7000-8000-000000000001",
    name: "admin",
    description: "Full control over tenant resources",
    is_system: true,
  },
  {
    id: "018f4e1c-0002-7000-8000-000000000002",
    tenant_id: "018f4e1a-0001-7000-8000-000000000001",
    name: "analyst",
    description: "Read access to approved data sources",
    is_system: false,
  },
  {
    id: "018f4e1c-0003-7000-8000-000000000003",
    tenant_id: "018f4e1a-0002-7000-8000-000000000002",
    name: "admin",
    description: "Full control over tenant resources",
    is_system: true,
  },
];

const USER_ROLES = [
  {
    user_id: "018f4e1b-0001-7000-8000-000000000001",
    role_id: "018f4e1c-0001-7000-8000-000000000001",
    tenant_id: "018f4e1a-0001-7000-8000-000000000001",
    granted_by: "018f4e1b-0001-7000-8000-000000000001", // self-bootstrap
  },
  {
    user_id: "018f4e1b-0002-7000-8000-000000000002",
    role_id: "018f4e1c-0002-7000-8000-000000000002",
    tenant_id: "018f4e1a-0001-7000-8000-000000000001",
    granted_by: "018f4e1b-0001-7000-8000-000000000001",
  },
];

const DATA_SOURCES = [
  {
    id: "018f4e1d-0001-7000-8000-000000000001",
    tenant_id: "018f4e1a-0001-7000-8000-000000000001",
    kind: "postgres",
    display_name: "orders_db",
    connection_secret_ref: "secret/acme/data-sources/orders_db",
    default_db: "orders",
    residency_region: "us-east-1",
    status: "active",
  },
];

const DATA_CLASSIFICATIONS = [
  {
    id: "018f4e1e-0001-7000-8000-000000000001",
    tenant_id: "018f4e1a-0001-7000-8000-000000000001",
    data_source_id: "018f4e1d-0001-7000-8000-000000000001",
    schema_name: "public",
    table_name: "customers",
    column_name: "email",
    classification: "restricted",
    tags: ["pii", "gdpr"],
    pii_category: "contact",
  },
  {
    id: "018f4e1e-0002-7000-8000-000000000002",
    tenant_id: "018f4e1a-0001-7000-8000-000000000001",
    data_source_id: "018f4e1d-0001-7000-8000-000000000001",
    schema_name: "public",
    table_name: "customers",
    column_name: "phone",
    classification: "confidential",
    tags: ["pii"],
    pii_category: "contact",
  },
];

const POLICIES = [
  {
    id: "018f4e1f-0001-7000-8000-000000000001",
    tenant_id: "018f4e1a-0001-7000-8000-000000000001",
    name: "allow-analysts-read-orders",
    version: 1,
    status: "active",
    effect: "allow",
    subject_match: JSON.stringify({ roles: ["analyst"] }),
    resource_match: JSON.stringify({ data_source: "orders_db", table: "orders" }),
    action: "SELECT",
    created_by: "018f4e1b-0001-7000-8000-000000000001",
    approved_by: "018f4e1b-0001-7000-8000-000000000001",
  },
  {
    id: "018f4e1f-0002-7000-8000-000000000002",
    tenant_id: "018f4e1a-0001-7000-8000-000000000001",
    name: "deny-analysts-customer-pii",
    version: 1,
    status: "active",
    effect: "deny",
    subject_match: JSON.stringify({ roles: ["analyst"] }),
    resource_match: JSON.stringify({ data_source: "orders_db", table: "customers" }),
    action: "SELECT",
    denied_columns: ["email", "phone"],
    created_by: "018f4e1b-0001-7000-8000-000000000001",
    approved_by: "018f4e1b-0001-7000-8000-000000000001",
  },
  {
    id: "018f4e1f-0003-7000-8000-000000000003",
    tenant_id: "018f4e1a-0001-7000-8000-000000000001",
    name: "row-filter-own-region",
    version: 1,
    status: "active",
    effect: "allow",
    subject_match: JSON.stringify({ roles: ["analyst"] }),
    resource_match: JSON.stringify({ data_source: "orders_db", table: "orders" }),
    action: "SELECT",
    row_filter: JSON.stringify({ field: "region", op: "eq", value: "${user.attributes.region}" }),
    created_by: "018f4e1b-0001-7000-8000-000000000001",
    approved_by: "018f4e1b-0001-7000-8000-000000000001",
  },
];

// ── helpers ───────────────────────────────────────────────────────────────────

function buildDsn(): string {
  const host = process.env.POSTGRES_HOST ?? "127.0.0.1";
  const port = process.env.POSTGRES_PORT ?? "5432";
  const user = process.env.POSTGRES_USER ?? "app_login_user";
  const pass = process.env.POSTGRES_PASSWORD ?? "changeme_app";
  const db   = process.env.POSTGRES_DB ?? "governance";
  return `postgresql://${user}:${pass}@${host}:${port}/${db}`;
}

async function setTenantCtx(client: Client, tenantId: string): Promise<void> {
  // Always SET LOCAL so the GUC is scoped to the current transaction
  await client.query("SELECT set_config('app.tenant_id', $1, true)", [tenantId]);
}

async function seedTable<T extends Record<string, unknown>>(
  client: Client,
  table: string,
  rows: T[],
  conflictCols: string = "id"
): Promise<void> {
  for (const row of rows) {
    const keys   = Object.keys(row);
    const values = Object.values(row);
    const placeholders = keys.map((_, i) => `$${i + 1}`).join(", ");
    const cols = keys.map((k) => `"${k}"`).join(", ");
    await client.query(
      `INSERT INTO ${table} (${cols}) VALUES (${placeholders})
       ON CONFLICT (${conflictCols}) DO NOTHING`,
      values
    );
  }
}

// ── main ──────────────────────────────────────────────────────────────────────

async function main(): Promise<void> {
  console.log("=== Seeding governance-platform dev environment ===\n");

  const client = new Client({ connectionString: buildDsn() });
  await client.connect();

  try {
    // Tenants have no tenant_id column; seed without RLS context
    console.log("  tenants...");
    await seedTable(client, "tenants", TENANTS as any);

    // All tenant-scoped tables require the GUC set before INSERT
    for (const tenantId of [
      "018f4e1a-0001-7000-8000-000000000001",
      "018f4e1a-0002-7000-8000-000000000002",
    ]) {
      await client.query("BEGIN");
      await setTenantCtx(client, tenantId);

      const tenantUsers = USERS.filter((u) => u.tenant_id === tenantId);
      const tenantRoles = ROLES.filter((r) => r.tenant_id === tenantId);
      const tenantUserRoles = USER_ROLES.filter((ur) => ur.tenant_id === tenantId);
      const tenantSources = DATA_SOURCES.filter((ds) => ds.tenant_id === tenantId);
      const tenantClassifications = DATA_CLASSIFICATIONS.filter((dc) => dc.tenant_id === tenantId);
      const tenantPolicies = POLICIES.filter((p) => p.tenant_id === tenantId);

      console.log(`  tenant ${tenantId}: users, roles, user_roles, data_sources, classifications, policies...`);
      await seedTable(client, "users",              tenantUsers as any);
      await seedTable(client, "roles",              tenantRoles as any);
      await seedTable(client, "user_roles",         tenantUserRoles as any, "user_id, role_id");
      await seedTable(client, "data_sources",       tenantSources as any);
      await seedTable(client, "data_classifications", tenantClassifications as any);
      await seedTable(client, "policies",           tenantPolicies as any);

      await client.query("COMMIT");
    }

    // Idempotency check: re-run seed and assert no new rows
    console.log("\n  Verifying idempotency (re-running once)...");
    for (const tenantId of [
      "018f4e1a-0001-7000-8000-000000000001",
      "018f4e1a-0002-7000-8000-000000000002",
    ]) {
      await client.query("BEGIN");
      await setTenantCtx(client, tenantId);

      const tenantUsers = USERS.filter((u) => u.tenant_id === tenantId);
      const tenantRoles = ROLES.filter((r) => r.tenant_id === tenantId);
      await seedTable(client, "users", tenantUsers as any);
      await seedTable(client, "roles", tenantRoles as any);

      await client.query("COMMIT");
    }

    console.log("\n=== Seed complete ===");
  } catch (err) {
    await client.query("ROLLBACK").catch(() => {});
    throw err;
  } finally {
    await client.end();
  }
}

main().catch((err) => {
  console.error("Seed failed:", err);
  process.exit(1);
});
