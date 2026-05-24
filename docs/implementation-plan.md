# Implementation Plan
## AI-Native Authorization & Retrieval Governance Platform

> *Companion to:* `architecture-v2.md`
> *Stack reference:* the production stack document
> *Goal:* a buildable, dependency-ordered sequence from empty repo to multi-region production. Each phase produces working, demoable software. No phase depends on a later phase.

---

## How To Use This Document

Each phase is **shippable** — at the end of every phase you have something that works end-to-end, even if narrow. We never spend a quarter on plumbing with nothing to show.

Every phase declares:

- **Goal** — what "done" looks like
- **Dependencies** — what must be complete first
- **Sub-phases** — concrete sequenced work
- **Exit criteria** — measurable definition of done
- **Risks** — what typically goes wrong
- **Owner profile** — what skills the phase needs

---

## Phase Map (At a Glance)

| #  | Phase                                                | Weeks | Demoable result                                                         |
| -- | ---------------------------------------------------- | ----- | ----------------------------------------------------------------------- |
| 0  | Foundation: repo, infra, environments, CI            | 1–2   | `make up` brings the whole stack up locally                             |
| 1  | Control-plane database & migrations                  | 2–3   | Schema deployed, RLS active, seeded                                     |
| 2  | Authentication & SessionContext                      | 3–5   | Login works; every request has a verified SessionContext                |
| 3  | PDP (Policy Decision Point) v1                       | 5–7   | Manual policies enforce permit/deny via API                             |
| 4  | Admin Console v1 + Policy Simulator                  | 7–9   | Admins author JSON policies, test in simulator, approve                 |
| 5  | Audit pipeline & tamper-evident logging              | 9–10  | Every policy + access decision flows to WORM + queryable store          |
| 6  | PEP v1: PostgreSQL transparent proxy + Calcite       | 10–14 | Real queries to PG get rewritten with WHERE filters & column masks      |
| 7  | Schema catalog & metadata crawler                    | 14–16 | All connected DBs crawled; columns classified; embeddings stored        |
| 8  | Permission-aware retrieval                           | 16–17 | Allowed Schema Snapshots; RAG honours per-chunk ACLs                    |
| 9  | AI Orchestrator: PAP graph (policy authoring by NL)  | 17–20 | Admins type English → drafted policy → simulator → approve              |
| 10 | AI Orchestrator: PEP graph (NL → safe SQL)           | 20–23 | End user asks a question → safe SQL → permitted results                 |
| 11 | Multi-database expansion                             | 23–28 | MySQL, SQL Server, Oracle, Snowflake, BigQuery, MongoDB                 |
| 12 | Real-time event stream & live access feed            | 28–30 | Kafka stream live; admin console shows live access                      |
| 13 | Anomaly detection & risk-aware ABAC                  | 30–34 | UEBA model in production; risk score becomes policy variable            |
| 14 | Auto-response, step-up auth, break-glass             | 34–36 | Risk thresholds trigger MFA, masking, termination automatically         |
| 15 | Scale-out: K8s, HA, multi-region                     | 36–42 | Active-active control plane; regional proxy fleets                      |
| 16 | Compliance, hardening, GA launch                     | 42–48 | SOC 2 Type II evidence collected; pentest passed; GA live               |

Total: ~48 weeks (≈11 months) to GA. Each phase is shippable; you can sell to design partners from Phase 6 onwards.

---

# Phase 0 — Foundation: Repository, Infrastructure, Environments, CI

**Goal:** any new engineer can `git clone` → `make up` → working stack in under 15 minutes.

**Dependencies:** none.

**Owner profile:** Platform / DevOps engineer.

## 0.1 — Repository structure

Monorepo with workspaces. Single repo, clear boundaries.

```
governance-platform/
├── apps/
│   ├── admin-console/         # Next.js
│   ├── api-gateway/           # Go + Gin
│   ├── ai-orchestrator/       # Node.js + TS + Fastify + LangGraph
│   ├── pdp/                   # Go service (Policy Decision Point)
│   ├── proxy/                 # Go service (Policy Enforcement Point)
│   └── schema-crawler/        # Go service
├── packages/
│   ├── shared-types/          # Zod schemas, shared TS types
│   ├── policy-dsl/            # Condition AST, validators
│   ├── audit-client/          # OTel-instrumented audit emitter
│   └── ui/                    # shadcn/ui shared components
├── infra/
│   ├── docker-compose.yml
│   ├── docker-compose.override.yml.example
│   └── migrations/            # SQL migrations (single source of truth)
├── scripts/
│   ├── bootstrap.sh
│   ├── seed.ts
│   └── verify-env.sh
├── docs/
│   ├── architecture-v2.md
│   ├── implementation-plan.md
│   └── runbooks/
├── .github/workflows/
├── Makefile
├── pnpm-workspace.yaml
└── go.work
```

**Why monorepo:** shared Zod types between TS frontend, TS AI orchestrator, and (via codegen) Go services. One repo, one PR.

## 0.2 — Local dev environment

Docker Compose is your dev environment. Everything below runs in containers:

```yaml
services:
  postgres:    # control plane DB
  redis:       # cache + pub/sub
  kafka:       # event stream (single-broker for dev)
  qdrant:      # vector store (or pgvector inside postgres)
  clickhouse:  # audit logs queryable
  minio:       # S3-compatible WORM bucket
  vault:       # secrets (dev mode)
  jaeger:      # tracing UI for local dev
  prometheus:  # metrics
  grafana:     # dashboards
```

Plus mocks for IdPs in dev (a simple `oidc-mock` container).

Engineers run the actual apps natively (faster reload) but everything stateful runs in Docker.

## 0.3 — Three environments minimum

| Env       | Purpose                                  | Data            |
| --------- | ---------------------------------------- | --------------- |
| `local`   | Engineer laptop                          | Seeded fixtures |
| `dev`     | Shared cloud env, auto-deploy from main  | Synthetic       |
| `staging` | Pre-prod, mirrors prod config            | Anonymized      |
| `prod`    | GA                                       | Real            |

Every environment has its own:
- Vault namespace
- IdP tenant
- Cloud account or sub-account (recommended for blast-radius isolation)
- Domain (e.g. `*.dev.platform.io`, `*.staging.platform.io`)

## 0.4 — CI/CD baseline

GitHub Actions workflows from day one:

- `ci.yml` — runs on every PR: lint, type-check, unit tests, build all services, scan dependencies (Snyk/Trivy)
- `deploy-dev.yml` — runs on merge to `main`, deploys to `dev`
- `deploy-staging.yml` — runs on tag, deploys to `staging`
- `deploy-prod.yml` — manual approval gate, blue/green deploy

**Pin everything:** Docker image SHAs, npm packages with `pnpm-lock.yaml`, Go with `go.sum`. No floating tags in production manifests.

## 0.5 — Observability scaffold (before any code)

OpenTelemetry SDK initialization in every service template. Even a hello-world service emits traces and metrics on day one. If you defer this, retrofitting OTel across services is painful — start with it.

## 0.6 — Engineering discipline rules to set NOW

These are cheap on day one, expensive in month six:

- **Conventional commits** + commitlint
- **Trunk-based development**: feature branches max 3 days
- **PR templates** that ask "what testing was done?"
- **No direct prod access** — everything via Terraform / scripts under audit
- **All SQL migrations forward-only** (Phase 1 detail)
- **Feature flags from day one** (use [Unleash](https://www.getunleash.io/) or LaunchDarkly)
- **No `TODO` without a ticket number**

## Exit criteria

- [ ] New engineer runs `make bootstrap && make up` and gets a working stack in < 15 min
- [ ] A PR opens with a hello-world Go service; CI passes; deploys to `dev`; visible in Grafana
- [ ] Vault stores at least one secret retrieved by an app
- [ ] OTel traces show end-to-end in Jaeger
- [ ] Pre-commit hooks reject lint failures locally

## Risks

- **Over-engineering the platform before there's product.** Resist building a service mesh, multi-cluster K8s, etc. now. Docker Compose is enough until Phase 15.
- **Shared monorepo build hell.** Use Nx, Turbo, or pnpm workspaces from day one or you'll regret it by month 3.

---

# Phase 1 — Control-Plane Database & Migrations

**Goal:** the data model is correct, migrations are versioned, RLS is active on the control plane itself.

**Dependencies:** Phase 0.

**Owner profile:** Backend engineer comfortable with PostgreSQL.

## 1.1 — Choose migration tooling

Use a tool that supports forward-only, hash-checked migrations. Recommendation: **[golang-migrate](https://github.com/golang-migrate/migrate)** for Go services, or **[Atlas](https://atlasgo.io/)** if you want declarative migrations. Avoid Prisma migrate at this layer — it doesn't handle RLS well.

```
infra/migrations/
├── 0001_tenants.sql
├── 0001_tenants.down.sql
├── 0002_users.sql
├── 0002_users.down.sql
├── 0003_roles.sql
├── ...
└── README.md  # migration rules
```

**Rules** (codified in CI):
- Forward-only in production. Down migrations exist only for dev / staging rollback.
- Never edit a merged migration. Add a new one instead.
- Every migration is idempotent (`CREATE IF NOT EXISTS`, `INSERT ... ON CONFLICT DO NOTHING`).
- Every migration is wrapped in `BEGIN; ... COMMIT;`.

## 1.2 — Apply the schema from architecture-v2 §5

Order matters. Apply in this sequence:

1. `tenants`
2. `users`
3. `roles` + `user_roles`
4. `data_sources`
5. `data_classifications`
6. `policies`
7. `policy_audit` (with hash trigger)
8. `access_audit` (partitioned table)
9. `schema_metadata` (with pgvector extension)
10. `doc_chunks`

## 1.3 — Enable RLS on the control plane itself

This is the step everyone skips. Don't skip it.

```sql
-- For every tenant-scoped table:
ALTER TABLE users ENABLE ROW LEVEL SECURITY;
ALTER TABLE users FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_iso ON users
  USING (tenant_id = current_setting('app.tenant_id')::uuid);
```

Repeat for `roles`, `user_roles`, `data_sources`, `data_classifications`, `policies`, `policy_audit`, `access_audit`, `schema_metadata`, `doc_chunks`.

A bug in any service that forgets to set `app.tenant_id` will fail closed (returns zero rows) rather than leaking cross-tenant data.

## 1.4 — Configure scoped database roles

```sql
CREATE ROLE app_read         NOINHERIT;
CREATE ROLE app_write        NOINHERIT;
CREATE ROLE app_admin        NOINHERIT;
CREATE ROLE app_migrator     NOINHERIT BYPASSRLS;
CREATE ROLE app_break_glass  NOINHERIT BYPASSRLS;
CREATE ROLE app_login_user   NOINHERIT LOGIN;

-- The login user can SET ROLE to any of the above:
GRANT app_read, app_write, app_admin, app_break_glass TO app_login_user;

-- Migrations connect as a separate user that can BECOME app_migrator only:
CREATE ROLE app_migration_login LOGIN;
GRANT app_migrator TO app_migration_login;
```

Now every service connects as `app_login_user` and `SET LOCAL ROLE app_read` per request. Migrations connect as `app_migration_login`.

## 1.5 — Audit triggers (hash chain)

Create the trigger that hash-chains `policy_audit` rows on insert:

```sql
CREATE OR REPLACE FUNCTION policy_audit_hash_chain()
RETURNS TRIGGER AS $$
DECLARE
  prev BYTEA;
BEGIN
  SELECT row_hash INTO prev FROM policy_audit
    WHERE tenant_id = NEW.tenant_id
    ORDER BY id DESC LIMIT 1;
  NEW.prev_hash := COALESCE(prev, '\x00'::bytea);
  NEW.row_hash := digest(
    NEW.prev_hash || convert_to(row_to_json(NEW)::text, 'UTF8'),
    'sha256'
  );
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER policy_audit_hash_trigger
BEFORE INSERT ON policy_audit
FOR EACH ROW EXECUTE FUNCTION policy_audit_hash_chain();
```

## 1.6 — Partitioning `access_audit`

Daily partitions, retained per tenant compliance setting. Use `pg_partman` or write a small cron that creates next-month partitions automatically.

## 1.7 — Seed data

A `scripts/seed.ts` that creates:
- 1 tenant `acme`
- 2 users: `admin@acme.test`, `analyst@acme.test`
- 2 roles: `admin`, `analyst`
- 1 demo data_source pointing at a fake `orders_db` PostgreSQL
- 5 demo policies

Engineers use this to test without setting up real IdPs.

## Exit criteria

- [ ] `migrate up` from empty database produces full schema
- [ ] All tenant tables have RLS enabled AND forced
- [ ] Inserting a `policy_audit` row produces a non-null `row_hash`
- [ ] Modifying any historical row breaks the hash chain (verified by a test)
- [ ] `scripts/seed.ts` runs idempotently
- [ ] Migration rollback works in `dev` (forward-only enforced in `prod` via CI check)

## Risks

- **Forgetting `FORCE ROW LEVEL SECURITY`.** Without it, table owners (migrations) bypass RLS, and a developer running maintenance as the wrong user exfiltrates data. Make it part of the migration checklist.
- **PgBouncer in transaction-pooling mode + plain `SET` (not `SET LOCAL`)** leaks GUCs across requests. The codebase must use `SET LOCAL` inside explicit transactions everywhere. Write a linter check that grep's for bare `SET app.` and fails the build.

---

# Phase 2 — Authentication & SessionContext Plumbing

**Goal:** every request to every service carries a verified, typed `SessionContext`.

**Dependencies:** Phase 0, Phase 1.

**Owner profile:** Backend engineer with auth experience.

## 2.1 — Pick AuthN

For V1 use **Better Auth** if you're TS-heavy, or **Keycloak** if you anticipate SAML in months not years. The architecture below assumes either via OIDC.

Configure:
- Password + magic-link for V1
- TOTP MFA
- Account lockout policies
- Strong session settings (short access tokens, rotating refresh)

## 2.2 — JWT structure

The token carries only **identity claims**, never policy state.

```json
{
  "sub": "user-uuid",
  "tenant": "tenant-uuid",
  "iss": "https://auth.platform.io",
  "aud": "platform-api",
  "exp": 1234567890,
  "iat": 1234567000,
  "amr": ["pwd", "totp"],
  "mfa_at": 1234566900,
  "session_id": "session-uuid"
}
```

Roles, permissions, attributes are **not** in the JWT. They resolve server-side per request from the database.

Sign with **Ed25519** (smaller signatures than RSA, faster to verify, no parameter ambiguity).

## 2.3 — SessionContext: the canonical shape

Defined in `packages/shared-types/session.ts` as a Zod schema, codegen'd to Go.

```ts
export const SessionContext = z.object({
  userId: z.string().uuid(),
  tenantId: z.string().uuid(),
  roles: z.array(z.string()),            // resolved server-side
  attributes: z.object({
    department: z.string().optional(),
    campusId: z.string().uuid().optional(),
    region: z.string().optional(),
    clearanceLevel: z.number().int().optional(),
    mfaSince: z.date().optional(),
    deviceTrust: z.enum(['managed','byod','unknown']).optional(),
  }),
  requestId: z.string().uuid(),
  traceId: z.string(),
  parentSpanId: z.string().optional(),
  isBreakGlass: z.boolean().default(false),
  riskScore: z.number().int().min(0).max(100).optional(),
  issuedAt: z.date(),
  expiresAt: z.date(),
});
export type SessionContextT = z.infer<typeof SessionContext>;
```

## 2.4 — Gateway middleware

Every public request flows through `apps/api-gateway` middleware that:

1. Parses & verifies the JWT (Ed25519 public key from JWKS endpoint).
2. Pulls `userId`, `tenantId` from the JWT.
3. Loads roles + attributes from PostgreSQL (cached in Redis 60s).
4. Builds the SessionContext.
5. Generates a fresh `requestId` and OTel `traceId`.
6. Attaches the SessionContext to the request and forwards via gRPC or HTTP to internal services.

Internal services authenticate via **mTLS** with service certificates issued by your internal CA (use cert-manager + a private CA in K8s; for Docker Compose, generate a dev CA).

## 2.5 — Service-to-service: never trust headers

The SessionContext can be tampered with if sent as plain HTTP headers. Two options, in increasing order of strictness:

1. **Signed propagation** — gateway signs the SessionContext with an internal HMAC key; services verify before trusting.
2. **mTLS + short-lived service tokens** — only authenticated services can talk to each other; SessionContext is a signed JWT minted by the gateway with audience = downstream service.

For V1, option 1 is acceptable inside a private network. Move to option 2 by Phase 15.

## 2.6 — `SET LOCAL` discipline in DB connections

Every service that talks to the control plane DB or to managed databases must, at the start of every transaction:

```sql
BEGIN;
SET LOCAL ROLE app_read;
SET LOCAL app.user_id   = '...';
SET LOCAL app.tenant_id = '...';
SET LOCAL app.campus_id = '...';
SET LOCAL app.break_glass = 'false';
-- queries
COMMIT;
```

Wrap this in a helper: `db.withSession(ctx, async (tx) => { ... })`. Forbid direct connection use in code review.

## 2.7 — Refresh tokens & session lifecycle

- Access tokens: 10 min, stateless (JWT).
- Refresh tokens: 30 days, **opaque**, stored in Redis as a hash. One-time use; rotated on every refresh. Reuse triggers session invalidation.
- Logout: deletes the refresh token; the access token continues to be valid until expiry (10 min) — acceptable tradeoff for stateless verification.
- "Logout everywhere": flag the user; gateway checks `users.session_invalidated_at > token.iat`. This requires one DB lookup per request but is cached aggressively.

## 2.8 — MFA & step-up readiness

Don't build step-up auth yet — but design for it. The JWT carries `amr` (auth methods) and `mfa_at` (last MFA timestamp). Phase 14 builds the step-up flow on top of these claims.

## Exit criteria

- [ ] Login flow works end to end
- [ ] Every API call to every internal service has a verified SessionContext
- [ ] An expired or tampered JWT returns 401 with no leakage
- [ ] A test confirms: removing `SET LOCAL app.tenant_id` from a query returns 0 rows (RLS still enforces)
- [ ] Service-to-service calls use mTLS in `dev` and `staging`
- [ ] OTel traces show identity propagation across at least 3 services

## Risks

- **Trusting the JWT for authorization claims.** Resist all pressure to "just put the roles in the JWT for performance." Stale roles = security incident.
- **Refresh-token theft** without rotation detection. The reuse-detection rule is mandatory.

---

# Phase 3 — Policy Decision Point (PDP) v1

**Goal:** given (subject, action, resource), the PDP returns a deterministic decision in < 5ms p99.

**Dependencies:** Phases 0–2.

**Owner profile:** Backend engineer (Go), comfortable with authorization concepts.

## 3.1 — Service shape

A separate Go service `apps/pdp` with:

- gRPC `Decide` RPC (primary)
- HTTP `/decide` endpoint (for debugging and admin tools)
- gRPC `BulkDecide` for the proxy (one call, many decisions)
- Health/readiness endpoints
- Prometheus `/metrics`

## 3.2 — Conditions DSL: parser & compiler

Define the condition DSL grammar (see architecture-v2 §6.3). Implement:

1. **Parser** — JSONB → typed AST in Go
2. **Validator** — checks operator allowlist, no infinite recursion, depth ≤ 5
3. **Compiler** — AST → closure (a function `func(ctx SessionContext, resource Attrs) bool`)
4. **SQL emitter** — AST → dialect-neutral SQL predicate AST (RexNode-like) for later proxy rewrite

Pre-compile every active policy's conditions to closures **at policy load time**. Keep them in process memory keyed by `policy_id`. Recompile on the pub/sub invalidation event from Phase 4.

## 3.3 — Decision algorithm (deny-overrides)

Implement exactly as specified in architecture-v2 §6.2. Critical points:

- **Default deny.** No matching allow policy = deny.
- **Deny-overrides.** Any matching deny rule = deny regardless of allows.
- **Compute final `allowedColumns = ⋃ allow.allowedColumns \ ⋃ deny.deniedColumns`.**
- **Combine row filters as AND** across matching allow policies.
- **Collect obligations**; if any unsatisfiable, demote to deny.

Write the algorithm as a pure function. Side-effect-free. Easy to unit test.

## 3.4 — Caching: two-tier

| Cache | Storage  | TTL    | Invalidation                |
| ----- | -------- | ------ | --------------------------- |
| L1    | sync.Map in-process LRU 10k entries | 60s | pub/sub event |
| L2    | Redis cluster | 60s   | pub/sub event               |

Cache key: `pdp:{tenantId}:{userId}:{action}:{resource}:{policyVersion}`.

**Critical:** the cache key includes `policyVersion` — a fingerprint of the currently-active policy set for that tenant, bumped on every policy change. Stale entries become unreachable rather than served stale.

Use single-flight to prevent stampede.

## 3.5 — Pub/sub invalidation

Phase 4's admin console writes a new policy → emits `policy.invalidate.{tenantId}` on Redis pub/sub. Every PDP node subscribes and:

1. Bumps the `policyVersion` for that tenant.
2. Recompiles any in-process closures for affected policies.
3. Drops the L1 cache entries.

## 3.6 — gRPC contract (proto)

```protobuf
service PDP {
  rpc Decide (DecideRequest) returns (Decision);
  rpc BulkDecide (BulkDecideRequest) returns (BulkDecideResponse);
}

message DecideRequest {
  bytes subject_session_context = 1;   // serialized SessionContext
  string action = 2;
  string resource = 3;
  string data_source_id = 4;
  map<string, string> context = 5;
}

message Decision {
  Effect effect = 1;
  repeated Obligation obligations = 2;
  RowFilter row_filter = 3;
  repeated string allowed_columns = 4;
  repeated string denied_columns = 5;
  map<string, string> column_masks = 6;
  string reason = 7;
  int32 ttl_seconds = 8;
}
```

## 3.7 — Tests are not optional

The PDP is the most safety-critical service in the system. Required tests:

- **Unit tests** of the decision algorithm with hand-crafted policy sets
- **Property tests** (using `gopter` or `rapid`):
  - Monotonicity: removing a policy never grants more access
  - Determinism: same input → same output, 100×
  - Tenant containment: cross-tenant always deny
- **Fuzz tests** of the conditions parser (malformed JSON, deeply nested, huge strings)
- **Benchmark suite** — assert p99 < 5ms with 10k cached + 90k uncached requests

## Exit criteria

- [ ] PDP deployed to `dev`; admin console (Phase 4) successfully calls it
- [ ] Benchmark shows p99 < 5ms at 10k QPS per node
- [ ] All property tests pass
- [ ] Pub/sub invalidation propagates to all PDP nodes in < 100ms
- [ ] Removing a user from a role takes effect in PDP within 1s (cache invalidates correctly)

## Risks

- **Over-clever condition DSL.** Resist Turing-completeness. The DSL must be decidable. CEL or your own bounded DSL only.
- **Cache invalidation bugs.** Test with chaos: kill a node mid-invalidation, verify it picks up state on restart from the version fingerprint.

---

# Phase 4 — Admin Console v1 + Policy Simulator

**Goal:** an admin can author a policy in JSON (no AI yet), simulate it against test subjects, and approve it.

**Dependencies:** Phases 0–3.

**Owner profile:** Full-stack engineer (Next.js + Go backend).

## 4.1 — App skeleton (Next.js + TanStack)

- Next.js 14+ with App Router
- TanStack Query for server state
- TanStack Router or Next.js routing (pick one — Next.js routing is fine for an admin app)
- shadcn/ui for components
- React Hook Form + Zod for forms
- Monaco Editor for the JSON policy editor

## 4.2 — Core admin pages (V1)

1. **Login** (delegates to Better Auth/Keycloak)
2. **Tenants list** (super-admin only)
3. **Users list & details** — view, deactivate, assign roles
4. **Roles list & details** — create, edit, parent role for hierarchy
5. **Policies list** — filter by resource, role, status
6. **Policy editor** — JSON Monaco editor with Zod schema validation
7. **Simulator** — pick subject + action + resource → see decision
8. **Audit trail** — recent policy changes
9. **Data sources** — register a managed DB (used in Phase 6+)

## 4.3 — Policy editor UX

The Monaco editor is configured with:
- JSON schema for `Policy` (from shared Zod schemas)
- Autocomplete for column names (from `data_classifications` table)
- Inline validation errors
- "Validate" button that calls the PDP's validation endpoint (does the policy make sense?)
- "Simulate" button that opens the simulator with this draft

## 4.4 — Simulator: the killer feature

Two modes:

**Mode A — Spot check**

Pick a subject (real user or synthetic persona), pick an action + resource. See:
- Decision: permit / deny
- Reason: which policy id matched
- Allowed columns
- Denied columns
- Row filter applied
- Obligations triggered

**Mode B — Diff mode**

For policy edits, compare current-active vs draft. Show:
- "+3 columns now visible: `email_domain`, `phone_country`, `signup_source`"
- "-200 row conditions now block: rows where `campus_id != 'hyd'`"
- "Affected users (estimated): 47"

The diff is computed by running the simulator over a sample of subjects from each role and aggregating.

## 4.5 — Approval workflow

- Every policy enters as `status = 'draft'`.
- Submit for review → drafts are immutable.
- A second admin approves (configurable: same-admin allowed for low-risk tenants — but require a setting flip).
- On approval: atomic transaction writes `status = 'active'`, records `approved_by`, writes `policy_audit` row, publishes Redis pub/sub event.
- Older versions remain queryable. Rollback = create a new policy version with prior content. No UPDATE-in-place.

## 4.6 — Audit trail page

Two tabs:
1. **Policy audit** — who changed what authorization
2. **Access audit** — who accessed what data (populated from Phase 5)

Filterable by user, resource, time, decision. Links from a user's profile and a resource's detail page.

## Exit criteria

- [ ] Admin can create a JSON policy, simulate it, approve it, and see it enforced via the PDP
- [ ] Approval requires a second admin (config-gated)
- [ ] Policy version history is fully queryable
- [ ] Simulator's diff view works for at least 20 sample subjects per role
- [ ] All actions write `policy_audit` rows with valid hash chains

## Risks

- **Skipping the simulator to "save time."** The simulator is what separates a toy from a tool. Every customer demo lives on it.
- **Letting admins edit policies in place.** Always version. Always atomic activation. Never in-place updates.

---

# Phase 5 — Audit Pipeline & Tamper-Evident Logging

**Goal:** every policy change and every access decision flows to a WORM bucket and a queryable store, end-to-end in < 60s.

**Dependencies:** Phases 0–4.

**Owner profile:** Data platform engineer.

## 5.1 — Producer side

Every service emits audit events via a shared package `packages/audit-client`:

```ts
audit.policyEvent({
  action: 'policy.create',
  policyId: '...',
  beforeState: null,
  afterState: policy,
  metadata: { requestId, ip, userAgent }
});

audit.accessEvent({
  userId, tenantId, dataSourceId,
  resource, action,
  decision: 'permit',
  reason: 'policy:abc',
  rowCount: 47,
  queryHash: sha256(normalizedSql),
  durationMs: 23
});
```

The client batches events and ships them to Kafka (single broker in V1). On Kafka failure, falls back to local bounded ring buffer; if that fills, drops with a Prometheus counter increment.

## 5.2 — Kafka topics

- `audit.policy.{tenantId}` — partitioned by tenantId
- `audit.access.{tenantId}` — high-volume
- `audit.system` — system-level events (deployments, role grants)

Retention: 7 days on Kafka (it's a transport, not a store).

## 5.3 — Sinks

Three consumers running in parallel:

**Sink 1 — WORM bucket (compliance)**

A Go service consumes `audit.policy` and writes to MinIO/S3 with Object Lock in Compliance mode. One object per hour per tenant. The object contains JSONL events plus the last `row_hash` from the database trigger (Phase 1.5).

**Sink 2 — ClickHouse (queryable)**

Both topics flow into ClickHouse for the admin console's audit trail page and for the anomaly detector (Phase 13). Use a daily partitioning scheme.

**Sink 3 — Hash chain verifier**

A cron job (hourly) re-computes the hash chain over the last hour of `policy_audit` rows and verifies it matches the latest WORM object. Mismatch → page security.

## 5.4 — Admin console: audit views

Update the audit pages to read from ClickHouse (fast) rather than PostgreSQL (slow at scale). Keep the PostgreSQL `policy_audit` as the source of truth for the hash chain; ClickHouse is a derived read model.

## 5.5 — Retention & deletion (GDPR)

Build the two-phase erasure flow:

1. **Tombstone** the user (`status = 'deprovisioned'`, scrub attributes).
2. **Audit log:** PII columns in audit rows are passed through deterministic tokenization (HMAC with a tenant-scoped key) so the user can be referenced but not re-identified by the audit reader.

For the WORM bucket, GDPR right-to-erasure is satisfied by destroying the tenant-scoped HMAC key — the data becomes pseudonymous-unlinkable rather than deleted (Object Lock prevents physical delete). Document this in your DPA.

## Exit criteria

- [ ] An admin policy change shows up in ClickHouse within 60s and in the WORM bucket within 1h
- [ ] Modifying a row in `policy_audit` after the fact is detected by the hourly verifier
- [ ] The audit trail UI in the admin console reads from ClickHouse with sub-second latency
- [ ] GDPR erasure flow tested end-to-end with a synthetic user

## Risks

- **Audit pipeline as single point of failure.** If audit dies, queries should NOT block. Audit is best-effort with strong durability guarantees, not a critical path for query latency.
- **WORM cost.** Compliance-mode Object Lock can't be deleted before TTL. If a buggy producer floods you with terabytes, you pay for years. Add daily volume alarms in Phase 5, not later.

---

# Phase 6 — PEP v1: PostgreSQL Transparent Proxy + Calcite

**Goal:** real BI tools and ORMs point at the proxy; queries to a managed PostgreSQL get rewritten with WHERE filters and column masks.

**Dependencies:** Phases 0–5.

**Owner profile:** Backend engineer with database internals knowledge (Go + Java for Calcite, or all-Java if going Calcite-native).

This is the **single hardest phase** in the project. Budget appropriately.

## 6.1 — Choose the proxy implementation strategy

Three viable paths:

| Path                            | Pros                              | Cons                          |
| ------------------------------- | --------------------------------- | ----------------------------- |
| **Build on ProxySQL 3.x**       | Mature wire protocol, low effort  | C++; harder to extend         |
| **Build in Go + pgwire library**| Go-native; easy to extend         | Re-implement protocol bits    |
| **Calcite host in Java**        | Native Calcite integration        | JVM, ops overhead             |

**Recommendation:** Go service that speaks PG wire protocol using `jackc/pgproto3`, calls out to a sidecar Java service running Calcite for parsing/rewriting via gRPC.

This split keeps the high-throughput hot path in Go while letting Calcite do what only Calcite does well.

## 6.2 — Wire protocol handler

Implement the minimum PostgreSQL wire protocol surface:

- Startup message
- Authentication (SCRAM-SHA-256)
- Simple Query
- Extended Query (Parse, Bind, Execute, Sync)
- ReadyForQuery
- ErrorResponse
- RowDescription, DataRow, CommandComplete
- Terminate

Use **`jackc/pgproto3`** which already implements the wire format.

The proxy presents itself to clients as PostgreSQL. Clients see no difference.

## 6.3 — Calcite rewrite service

A Java service exposing gRPC:

```protobuf
service CalciteRewriter {
  rpc Rewrite (RewriteRequest) returns (RewriteResponse);
}

message RewriteRequest {
  string raw_sql = 1;
  string source_dialect = 2;     // 'postgres' in V1
  string target_dialect = 3;     // 'postgres' in V1
  Decision decision = 4;          // from PDP
  map<string,string> bindings = 5;
}

message RewriteResponse {
  string rewritten_sql = 1;
  repeated string referenced_tables = 2;
  repeated string referenced_columns = 3;
  string ast_hash = 4;
  RewriteError error = 5;
}
```

Inside, the service:
1. Parses with `SqlParser` using a PostgreSQL `SqlParser.Config`.
2. Validates against a catalog (populated from the managed DB's `information_schema`).
3. Converts to `RelNode`.
4. Applies rewrite rules:
   - Inject filters from `decision.rowFilter` (one filter per referenced table)
   - Replace masked column refs with `MASK_FN(col)` calls
   - Strip denied columns from projections
   - Enforce LIMIT
5. Converts back with `RelToSqlConverter` using `PostgresqlSqlDialect`.

## 6.4 — End-to-end flow per query

```
client → PG wire → proxy
  ├─ proxy parses startup, authenticates
  ├─ proxy looks up SessionContext from a gateway-issued token in the startup options
  ├─ on Query:
  │   1. extract raw SQL
  │   2. quick deny checks (statement type, banned keywords)
  │   3. PDP.BulkDecide for all tables (estimated by a fast regex)
  │   4. CalciteRewriter.Rewrite(...)
  │   5. Audit event emitted
  │   6. Get backend connection (PgBouncer-backed pool, per data_source)
  │   7. BEGIN; SET LOCAL ROLE app_read; SET LOCAL app.user_id=...; etc.
  │   8. Execute rewritten SQL
  │   9. Stream rows back to client unchanged
  │   10. COMMIT
  │   11. Audit completion (row_count, duration_ms)
  └─ on connection close: cleanup
```

## 6.5 — Authentication: how does the proxy know who the user is?

Two options:

**Option A — Connection-time token (preferred)**

The user's BI tool connects with username = real user, password = a short-lived token issued by the API gateway via `POST /db-token`. The proxy validates the token, resolves the SessionContext from it.

**Option B — Mutual auth via cert**

The user holds a client certificate issued by your CA. The proxy validates it against the user's identity. Better for ML notebooks and CLI tools.

V1 ships option A. Option B comes in Phase 14 alongside step-up auth.

## 6.6 — Connection pooling

Per `(tenant_id, data_source_id)` pool, backed by PgBouncer. The proxy uses a pool of long-lived connections to PgBouncer; PgBouncer pools to the backend.

Per-pool sizing from tenant plan:
- Starter: 25 connections
- Pro: 100 connections
- Enterprise: 500+ connections, dedicated PgBouncer

## 6.7 — Query cost gate

Before executing the rewritten SQL, optionally:

```sql
EXPLAIN (FORMAT JSON) <rewritten_sql>
```

Parse the JSON. If `Total Cost > role.maxCost` or `Plan Rows > role.maxRows`, reject with a clear error and audit. The DB-side `statement_timeout` and `idle_in_transaction_session_timeout` are backstops.

## 6.8 — Side-channel hardening

- Generic error messages to clients: "query not permitted" — no leakage of which column or row was denied.
- Specifics go to audit and the admin console only.
- Block `information_schema`, `pg_catalog`, `pg_*` system tables from user queries.

## 6.9 — Native RLS as last-line backstop

Even though the proxy rewrites the WHERE clause, also configure native PostgreSQL RLS policies on the managed DB tables that mirror the proxy's rules. **Defense in depth.** If the proxy is bypassed (someone direct-connects with leaked credentials), RLS still applies.

This requires the proxy to also be able to provision RLS policies on the managed DB. Build a small "policy syncer" cron that ensures the managed DB's RLS matches the active policies. Phase 15 makes this real-time via CDC; V1 is hourly cron.

## Exit criteria

- [ ] A psql client connects to the proxy and successfully runs `SELECT * FROM orders` against a managed PG
- [ ] Without a matching policy → query fails with permission error
- [ ] With a policy → query is rewritten, masks applied, results streamed
- [ ] Latency overhead p99 < 15ms vs direct PG connection
- [ ] Audit event emitted for every query with the rewritten SQL hash
- [ ] Direct connection to managed DB (bypassing proxy) still enforces via native RLS

## Risks

- **Calcite is a beast.** Budget 2–3 weeks just for the Calcite integration to be production-ready. Hire someone who has shipped Calcite before, or accept the learning curve.
- **PG protocol edge cases.** Prepared statements, COPY, LISTEN/NOTIFY, large objects — all need handling. Start by supporting only Simple Query + Extended Query; reject the rest with clear errors and add later.
- **Connection-pool meltdowns.** Test under load with a connection-leaking client. The proxy must reap orphaned backend connections aggressively.

---

# Phase 7 — Schema Catalog & Metadata Crawler

**Goal:** every connected database is crawled; every column is classified; embeddings are stored for retrieval.

**Dependencies:** Phases 0–6 (specifically Phase 6 for the multi-DB connection layer).

**Owner profile:** Backend engineer.

## 7.1 — Crawler service

A Go service `apps/schema-crawler` that, per `data_source`:

1. Connects (read-only) via the same drivers the proxy uses.
2. Queries `information_schema` (or DB-specific equivalents) for tables, columns, types, constraints, FK relationships.
3. Samples up to 10 distinct values per column **for non-classified or low-classification columns only**.
4. Writes to `schema_metadata` with `quarantine = true` for new entries.
5. Diffs against last crawl; emits `schema.drift` events for added/changed/removed columns.

Schedule: every 6 hours per data source, plus on-demand via admin console "refresh."

## 7.2 — Classification UI

A page in the admin console where a steward sees:
- All unclassified columns (queued for triage)
- Suggested classification based on column name patterns (`%ssn%` → `restricted+pii`, `%email%` → `confidential+contact`, etc.)
- Bulk-apply classifications by pattern
- Manual override with notes

## 7.3 — Embedding generation

For each column, generate an embedding from:

```
"{table_name}.{column_name} ({data_type}): {description}"
```

Use `text-embedding-3-large` (3072 dims) or `text-embedding-3-small` (1536 dims, much cheaper). Store in `schema_metadata.embedding`.

Re-embed on description change. Batch in groups of 100 to control costs.

## 7.4 — Quarantine enforcement

The retrieval layer (Phase 8) filters out `quarantine = true` rows. New columns are invisible to AI until a steward classifies them.

Send a Slack alert (via Phase 12's webhook system) to data stewards when columns enter quarantine.

## 7.5 — Drift handling

When the crawler detects:
- **New column** → quarantine + alert
- **Column renamed** → flag policies referencing the old name; require steward confirmation before remapping
- **Column dropped** → flag affected policies as "broken"; queue for steward review
- **Type change** → flag; many drift scenarios are app deploys, not threats

## Exit criteria

- [ ] All connected PG instances are crawled every 6h
- [ ] Adding a new column to a managed DB produces a quarantine entry within 6h + a Slack alert
- [ ] Steward UI can classify columns in bulk
- [ ] Embeddings are present for every classified column
- [ ] Vector similarity search returns sensible results for a sample of natural-language queries

## Risks

- **Classification fatigue.** If a tenant has 50k columns, no steward will hand-classify them all. The bulk-pattern matcher and good defaults make or break adoption.
- **Sample-value leakage.** Make absolutely sure sample values are only stored for `public` and `internal` classifications. A bug here puts PII in your control plane.

---

# Phase 8 — Permission-Aware Retrieval

**Goal:** when the AI needs schema or documents, it sees only what the requesting user can access.

**Dependencies:** Phases 0–7.

**Owner profile:** Backend engineer.

## 8.1 — Allowed Schema Snapshot service

A library / function `buildAllowedSnapshot(ctx SessionContext, dataSourceId)`:

1. Calls PDP for all resources in the data source.
2. For each `read`-permitted resource, looks up tables in `schema_metadata`.
3. Subtracts `denied_columns`; intersects `allowed_columns` if explicit.
4. Attaches column metadata: type, description, sample values (only if classification ≤ internal).
5. Inlines FK relationships only between tables both in the snapshot.
6. Returns a structured object + a hash.
7. Cached by `(userId, dataSourceId, schemaVersion)` for 5 min.

The output looks like:

```json
{
  "version": "snapshot-hash-abc",
  "tables": [
    {
      "name": "orders",
      "schema": "public",
      "columns": [
        { "name": "id", "type": "uuid" },
        { "name": "amount", "type": "numeric" },
        { "name": "customer_email", "type": "text", "masked": "mask_email_domain" }
      ],
      "foreign_keys": [
        { "column": "customer_id", "ref_table": "customers", "ref_column": "id" }
      ],
      "description": "Order records, one per purchase"
    }
  ]
}
```

## 8.2 — Document RAG with per-chunk ACLs

For document corpora (manuals, policies, emails):

1. Ingestion pipeline writes to `doc_chunks` with `acl_roles`, `acl_users`, `acl_attrs`, `classification`.
2. Retrieval query filters by ACL + RLS on the chunks table.
3. The retrieval function returns only chunks the user can see.
4. The classification of retrieved chunks bounds which LLM provider can be used (private cloud for `restricted`, public for `public`).

## 8.3 — Prompt injection defense at retrieval

- Wrap every chunk in delimiters with a system-prompt instruction that text inside delimiters is untrusted.
- Strip control phrases ("ignore previous instructions", role markers) via a regex preprocessor.
- Quarantine new chunks for review before retrieval is allowed to surface them.

## 8.4 — LLM provider routing

Based on the highest-classification content in the prompt:

| Content classification | Allowed providers                             |
| ---------------------- | --------------------------------------------- |
| public                 | Any (cost-optimized)                          |
| internal               | Any                                           |
| confidential           | Provider with DPA + zero-retention contracts  |
| restricted             | Private-cloud / on-prem (Bedrock private, vLLM on-prem) |

Implement via **LiteLLM** with per-tenant routing rules.

## Exit criteria

- [ ] Calling `buildAllowedSnapshot` for two users with different roles produces different snapshots
- [ ] Document retrieval returns 0 results when the user lacks ACL even if vector similarity is high
- [ ] Prompts containing `restricted` content are routed to on-prem models only
- [ ] Snapshot cache hit rate > 90% in load tests

## Risks

- **Leakage through embeddings.** Embeddings of sensitive text can sometimes be inverted. Restricted-classification content should not be embedded in the shared vector store at all — keep a per-tenant private store for those.

---

# Phase 9 — AI Orchestrator: PAP Graph (Policy Authoring by NL)

**Goal:** an admin types "Give finance managers read-only access to payments for their own campus, hide bank account numbers" → drafted JSON policy → simulator preview → approval.

**Dependencies:** Phases 0–8.

**Owner profile:** AI / LangGraph engineer + backend.

## 9.1 — Service shape

`apps/ai-orchestrator` in TypeScript + Fastify + LangGraph. Each agent node is a typed function. Streaming output to the admin console via Server-Sent Events.

## 9.2 — The PAP graph nodes

```
[Input Sanitizer]
  → strip control sequences; bound length; check rate limit
[Intent Parser]
  → classify: role.create | policy.update | grant | revoke
  → small model (Haiku, GPT-4o-mini); structured output
[Schema Resolver]
  → map fuzzy terms ("payments", "campus") to canonical names
  → uses ONLY metadata the admin can manage
  → RAG over filtered schema_metadata
[Policy Drafter]
  → frontier model (Sonnet, GPT-4o)
  → constrained decoding with Zod schema = Policy
  → output is typed JSON
[Policy Validator]
  → deterministic checks: tables exist, no privilege escalation,
    no cross-tenant, no conflict with existing deny rules
[Simulator]
  → runs draft against synthetic and real subjects
  → produces impact summary
[Audit Explainer]
  → generates plain English explanation of what this policy does
[Human Approval]
  → ALWAYS in V1/V2; no auto-apply
  → emits approval UI state via SSE
[Compiler]
  → writes draft to policies table
  → emits invalidation event
```

## 9.3 — Graph bounds & safeguards

- Max 6 node iterations per request.
- Wall-clock budget: 30s.
- Token budget per tenant per minute; throttle at 80%, hard-stop at 100%.
- Each node typed I/O; LLM emits malformed JSON → node fails (no auto-repair).
- Idempotency keys on the Compiler — restart safety.
- On failure, the user sees a clean error with a "report to support" button; the system does NOT silently retry the LLM call multiple times (cost explosion risk).

## 9.4 — Constrained decoding

For the Policy Drafter, use provider-native structured output:
- OpenAI: `response_format: { type: "json_schema", schema: ... }`
- Anthropic: tool-use mode with a single tool whose input schema is the Policy
- Anthropic via Bedrock: same as direct

Backstop with Zod validation at the node boundary.

## 9.5 — Show the JSON

In the admin console, the result of an AI session is **both**:
1. The natural-language prompt + English explanation
2. The generated JSON policy (Monaco editor, editable)
3. The simulator preview

The admin approves the JSON, not the English. This is a critical UX commitment.

## 9.6 — Adversarial Reviewer (V2 enhancement, optional in this phase)

After the Drafter produces output, a second LLM is prompted to attack it:

> "You are a red team. Find ways this policy could leak data, be misused, or allow privilege escalation. List specific attack scenarios."

The simulator runs each scenario. Successful attacks block approval and feed back into the policy for refinement.

## 9.7 — Cost & latency telemetry

OTel attributes on every LLM span:
- `llm.provider`, `llm.model`
- `llm.input_tokens`, `llm.output_tokens`
- `llm.cost_usd`
- `llm.latency_ms`

Surface a per-tenant LLM cost dashboard. Alert at 200% of 7-day average.

## Exit criteria

- [ ] Admin types a natural-language request → drafted policy appears in editor in < 10s
- [ ] Drafted policy passes Zod validation 99%+ of the time
- [ ] Simulator preview accompanies every draft
- [ ] Approving the draft writes to `policies` and invalidates caches
- [ ] Prompt-injection attempts in admin input are detected and refused
- [ ] LLM cost per authoring session < $0.10 on average

## Risks

- **The Drafter hallucinates column names.** The Validator must catch this — every column reference must resolve in `schema_metadata` or the policy is rejected. Don't trust the model.
- **LLM cost runaway.** Tight per-tenant budgets and aggressive caching of identical prompts are mandatory.

---

# Phase 10 — AI Orchestrator: PEP Graph (NL → Safe SQL)

**Goal:** an end user asks "show me overdue invoices this quarter" → safe SQL is generated, validated, executed via the proxy, results returned.

**Dependencies:** Phases 0–9.

**Owner profile:** Same as Phase 9.

## 10.1 — The PEP graph nodes

```
[Input Sanitizer]
[Permission Resolver]
  → PDP call; builds Allowed Schema Snapshot
[Retriever]
  → vector search over filtered schema_metadata
[SQL Drafter]
  → frontier model, constrained decoding
  → output is a SELECT-only AST (Calcite-compatible)
[AST Validator]
  → allowlist tables/columns
  → reject DDL/DML, subqueries (V1), forbidden functions
[Cost Estimator]
  → EXPLAIN; reject if cost > budget
[Proxy Executor]
  → submits to proxy (Phase 6) with user's SessionContext
[Result Formatter]
  → streams to client; applies any output-side masks
```

## 10.2 — Constraint: emit AST, not strings

The SQL Drafter should emit a structured representation (an AST in JSON, compatible with Calcite's `SqlNode`) — not raw SQL strings. Why:

- A grammar-constrained AST is much harder to inject into
- The validator works on the AST directly (cheap, fast)
- The same AST can be rendered to any dialect via Calcite's `SqlDialect`

If your LLM provider can constrain to a complex JSON schema, this works. If not, fall back to raw SQL + immediate Calcite parse + validation.

## 10.3 — Validator rules

Reject if any of:
- Statement type ≠ SELECT
- Any referenced table not in the Allowed Schema Snapshot
- Any referenced column not in the snapshot for its table
- Any function not in the allowlist (`sum`, `count`, `avg`, `min`, `max`, `coalesce`, `date_trunc`, etc.)
- Subqueries (V1); enable later
- Multiple statements (semicolons in body)
- Comments (strip and re-parse)
- Cartesian products
- LIMIT missing or > role cap
- JOINs not declared in the schema's FK graph

## 10.4 — Cost gate

Run `EXPLAIN (FORMAT JSON)` as the executor's first action. Parse the cost. Reject expensive queries with a friendly message and a hint ("try narrowing the date range").

## 10.5 — Result streaming

Long result sets stream row-by-row from the proxy back through the orchestrator to the client. Each row passes through any output-side mask functions (e.g., `mask_email_domain`).

The client (chat UI) renders rows progressively, not all-at-once. Crucial for queries returning 10k+ rows.

## 10.6 — End-user UX (chat interface)

A "Chat with your data" view in the admin console (or a separate user app):
- User asks a question
- See: the resolved schema (collapsed), the generated SQL (expandable), the result table
- Save query as a "named question" for repeated use
- Feedback thumbs up/down (used for fine-tuning later)

## Exit criteria

- [ ] End-to-end: question in chat → SQL → results table in < 5s p95
- [ ] Validator rejects > 99% of off-allowlist generated SQL
- [ ] AST-based generation produces dialect-correct SQL via Calcite
- [ ] Cost gate prevents queries that would scan > 1M rows
- [ ] Result streaming works for 100k-row results without OOM

## Risks

- **Hallucinated joins.** Constrain joins to FK graph. Reject otherwise.
- **Validator drift.** If SQL Drafter rejection rate exceeds 10%, the Drafter has drifted; investigate before adding more LLM rules.

---

# Phase 11 — Multi-Database Expansion

**Goal:** the platform enforces policies on MySQL, SQL Server, Oracle, Snowflake, BigQuery, Databricks, and MongoDB — not just PostgreSQL.

**Dependencies:** Phases 0–10.

**Owner profile:** Backend engineers with each target DB's experience.

## 11.1 — Tackle in two waves

**Wave A — wire-protocol compatible (5–6 weeks)**

Databases that speak SQL over a network protocol you can proxy:
1. MySQL / MariaDB (ProxySQL handles wire; Calcite handles dialect)
2. SQL Server (TDS protocol — harder, consider a JDBC bridge for V1)
3. Oracle (TNS — same as SQL Server; JDBC bridge V1)

**Wave B — HTTPS / REST-based (3–4 weeks)**

Databases where you proxy at the API layer, not wire:
4. Snowflake (REST + JDBC, both supported)
5. BigQuery (REST API)
6. Databricks Unity Catalog (JDBC)
7. MongoDB (MongoDB wire protocol — different model entirely)

## 11.2 — Per-DB enforcement strategy

| DB         | Proxy wire    | Native last-line                |
| ---------- | ------------- | ------------------------------- |
| MySQL      | yes (ProxySQL)| Views + role grants             |
| SQL Server | JDBC bridge   | Row Level Security + DDM        |
| Oracle     | JDBC bridge   | VPD (Virtual Private Database)  |
| Snowflake  | REST proxy    | Row access policies + DDM       |
| BigQuery   | REST proxy    | Row-level access + policy tags  |
| Databricks | JDBC bridge   | Unity Catalog policies          |
| MongoDB    | wire proxy    | Aggregation pipeline injection ($match) |

## 11.3 — Session context propagation per DB

The proxy must set context the target DB can use in its native policies:

- **MySQL:** `SET @app_user := '...'; SET @app_tenant := '...'`
- **SQL Server:** `EXEC sp_set_session_context @key='user', @value='...'`
- **Oracle:** `DBMS_SESSION.SET_IDENTIFIER('user_uuid')`; access via `SYS_CONTEXT('USERENV', 'CLIENT_IDENTIFIER')`
- **Snowflake:** session variables `SET user_id = '...'`
- **BigQuery:** request labels + IAM (context flows through service-account impersonation)
- **MongoDB:** `$set` operator in transactions; harder

## 11.4 — Calcite dialect coverage

Calcite already ships `PostgresqlSqlDialect`, `MysqlSqlDialect`, `MssqlSqlDialect`, `OracleSqlDialect`, `SnowflakeSqlDialect`, `BigQuerySqlDialect`, `SparkSqlDialect`. Use them. Only build custom dialects if you find specific function rewrites your target DB needs.

## 11.5 — Same policy, different output

The architecture-v2 doc gave an example. Make this real and test it. One policy authored once should:

```sql
-- For PG:
WHERE campus_id = 'hyd'
  AND tenant_id = current_setting('app.subject.tenant_id')::uuid

-- For Snowflake:
WHERE campus_id = 'hyd'
  AND tenant_id = $tenant_id

-- For Oracle:
WHERE campus_id = 'hyd'
  AND tenant_id = SYS_CONTEXT('USERENV', 'CLIENT_IDENTIFIER')
```

A test suite runs each policy through each target dialect and asserts the rewrites are semantically equivalent (use `sqlglot` for cross-dialect normalization).

## Exit criteria

- [ ] All 7+ database types in the matrix are reachable through the proxy
- [ ] A single policy authored once enforces correctly on at least 4 different DB types
- [ ] Cross-dialect equivalence tests pass for a corpus of 50+ test policies
- [ ] Latency overhead per DB type meets the < 15ms p99 target

## Risks

- **Each DB has quirks.** Oracle's NULL handling, Snowflake's variant types, MongoDB's whole-different-model. Budget time per DB.
- **TDS and Oracle TNS are not pleasant.** A JDBC bridge is a fine V1 — it's a hop, but it works. Replace with native wire later if performance demands.

---

# Phase 12 — Real-Time Event Stream & Live Access Feed

**Goal:** Kafka stream of every access event is live; admin console shows access happening in real time.

**Dependencies:** Phases 0–11.

**Owner profile:** Backend / streaming.

## 12.1 — Topic design

- `audit.access.{tenantId}` — partitioned by `tenant_id`, keyed by `user_id` for ordering per user
- `audit.policy.{tenantId}` — already from Phase 5
- `audit.system` — deployments, role grants, etc.

V1 single broker; V2 (Phase 15) a 3-broker cluster.

## 12.2 — Producer reliability

Already wired in Phase 5. Add:
- `acks=all`
- Idempotent producer
- Local disk buffer on Kafka unavailability (bounded ring; alert when filling)

## 12.3 — Consumers

| Consumer                | Purpose                                |
| ----------------------- | -------------------------------------- |
| `clickhouse-sink`       | Phase 5 already                        |
| `worm-sink`             | Phase 5 already                        |
| `live-feed-broadcaster` | NEW — broadcasts to admin UI via WS    |
| `anomaly-detector`      | NEW (Phase 13)                         |
| `webhook-fanout`        | NEW — calls customer webhooks          |

## 12.4 — Live Access Feed in the admin console

A new admin page: **Live Activity**. Connected over WebSocket to `live-feed-broadcaster`. Shows in real time:
- User, resource, action, decision, latency, masked-or-not, risk score (Phase 13)
- Filter by user / resource / decision
- Pause/resume
- Click-through to query details (with the rewritten SQL hash and the policy that fired)

## 12.5 — Webhook subscriptions

In admin console, customers configure webhook URLs for event types:
- `policy.changed`
- `access.denied`
- `risk.spike` (Phase 13)
- `schema.drift`
- `breakglass.activated`

The `webhook-fanout` consumer:
- POSTs the event JSON
- HMAC-signs with a per-tenant secret
- Retries with exponential backoff (max 5 retries, 24h)
- Dead-letters to a queue customer can inspect

## Exit criteria

- [ ] An access event is visible in the Live Activity feed < 2s after the query
- [ ] A webhook subscription receives events within 5s
- [ ] Webhook signature is verifiable with a tenant secret
- [ ] Kafka outage doesn't drop events for up to 1h (local ring + replay)

## Risks

- **Webhook abuse against customer endpoints.** Rate-limit per subscription. Circuit-break if their endpoint 5xx's for > 5 min.

---

# Phase 13 — Anomaly Detection & Risk-Aware ABAC

**Goal:** every user has a continuously updated risk score; the score is a first-class ABAC variable that policies can reference.

**Dependencies:** Phases 0–12.

**Owner profile:** ML engineer + backend.

## 13.1 — Choose the detection approach

Start simple, evolve:

**V1 — Statistical (3 weeks)**

Rolling 90-day baseline per user:
- Typical access times (hour-of-day distribution)
- Typical resources touched (set)
- Typical row volume per query
- Typical IP / device

Z-score outliers contribute to risk score. Easy to ship, easy to explain to customers, low false-positive rate when tuned.

**V2 — ML (later quarter)**

Transformer + GNN ensemble:
- Sequence model on user's recent access sequence
- GNN on the (user, resource) bipartite graph for "who else accesses this together"
- Risk score is the weighted ensemble

This V2 is what UEBA vendors charge $250K/year for. Build V1 first; V2 is differentiator.

## 13.2 — Streaming pipeline

Apache Flink consumes `audit.access.{tenantId}`:

```
[Kafka source]
  → [user keyed state]
  → [feature window: last 1h, 24h, 7d, 90d]
  → [z-score / model inference]
  → [risk score per user, per resource access]
  → [Kafka sink: risk.scored]
```

Output a `risk.scored` event per access with the score. Also write a per-user current score to Redis with 60s TTL so the PDP can read it.

## 13.3 — Risk score as ABAC variable

The PDP loads `riskScore` from Redis into `SessionContext` before evaluating policies. Policies can now reference it:

```json
{
  "all": [
    { "field": "subject.riskScore", "op": "lt", "value": 70 },
    { "field": "resource.classification", "op": "in", "value": ["public", "internal"] }
  ]
}
```

A user with elevated risk score is automatically denied access to sensitive data.

## 13.4 — Risk score visibility

In admin console:
- User detail page shows current risk score and history
- Live Activity feed colors entries by score
- Alerts on score spikes

Customers can pull risk scores via API for their own SIEMs.

## 13.5 — Calibration

False positives are the killer of UEBA products. Counter-measures:

- **Warm-up period.** Risk score is "unknown" for first 30 days per user.
- **Cooldown.** A single anomalous event doesn't spike permanently — exponential decay.
- **Allowlist patterns.** Known maintenance windows, batch jobs, etc.
- **Per-tenant tuning UI** — show the score distribution, let admins adjust thresholds.

## Exit criteria

- [ ] Risk score is computed for every access event within 1s
- [ ] PDP successfully uses `riskScore` in policy decisions
- [ ] False positive rate < 5% on a 30-day calibration run with a real tenant
- [ ] Score visible in admin console and Live Activity feed

## Risks

- **Tenant data variance.** A small tenant has too little data for good baselines. Use per-role or per-team baselines as fallback. Cohort-based baselining is the answer.

---

# Phase 14 — Auto-Response, Step-Up Auth, Break-Glass

**Goal:** elevated risk triggers automatic response — MFA step-up, additional masking, or session termination.

**Dependencies:** Phases 0–13.

**Owner profile:** Backend + security.

## 14.1 — Auto-response playbooks

Define tenant-configurable playbooks tied to risk score tiers:

| Score range | Default action                                                |
| ----------- | ------------------------------------------------------------- |
| 0–40        | Normal                                                        |
| 41–70       | Allow + tag for review                                        |
| 71–85       | Require step-up MFA before next query                         |
| 86–95       | Auto-mask additional columns (heightened protection)          |
| 96–100      | Block + force re-auth + page security contact                 |

Tunable per tenant. Defaults shipped, overrides exposed in admin.

## 14.2 — Step-up auth flow

When a policy decision returns an obligation like `require_mfa_within=5min`:

1. PEP (proxy or AI orchestrator) returns `401 step_up_required` with an obligation token.
2. Client redirects user to IdP step-up flow.
3. After MFA, IdP issues a fresh JWT with updated `mfa_at` claim.
4. Client retries with new token.
5. PDP now sees fresh `mfaSince`; obligation satisfied.

Build a small `obligation-tracker` library used by both the proxy and the AI orchestrator.

## 14.3 — Mid-flight masking

When risk score escalates during a streaming query:

- Proxy intercepts the stream
- Increases masking on remaining rows
- Optionally calls `pg_cancel_backend` to kill the in-flight query
- Sends a "query terminated due to risk score change" event

This is a flashy demo feature and a real safety feature. Don't skip.

## 14.4 — Break-glass workflow

- Time-boxed access bypassing specific policies
- Maximum 1 hour, configurable down per tenant
- Requires two human approvers (always)
- Sets `app.break_glass = true` in DB sessions
- Specific RLS policies opt-in to honor it: `USING (... OR current_setting('app.break_glass', true) = 'true')`
- Every action logged with `metadata.break_glass = true` and a mandatory reason
- Auto-revoked at TTL; summary report to designated security contact

## 14.5 — Admin console additions

- "Active break-glass sessions" page (super-admin)
- Auto-response playbook editor
- Per-user risk score history page

## Exit criteria

- [ ] Step-up MFA correctly triggers on `require_mfa` obligation
- [ ] Risk score crossing 71 triggers MFA requirement
- [ ] Break-glass requires two approvers and auto-expires
- [ ] Mid-flight masking works for streaming queries

## Risks

- **Step-up flow timeout.** Users on flaky networks hit the MFA timeout, fail, retry. Build generous retry windows.
- **Break-glass abuse.** Audit every use of it. Quarterly review of break-glass patterns; outliers get investigated.

---

# Phase 15 — Scale-Out: Kubernetes, HA, Multi-Region

**Goal:** the system survives a region outage with < 30 min RTO and < 5 min RPO. Active-active control plane across regions.

**Dependencies:** Phases 0–14.

**Owner profile:** SRE + platform.

## 15.1 — Move from Docker Compose to Kubernetes

By now you have ~10 services. Compose is at its limit. Switch to K8s.

- Managed K8s per cloud (EKS, GKE, AKS — pick one per cloud you support)
- Helm charts per service
- ArgoCD for GitOps deployment
- Cluster autoscaler + Karpenter for node-level scaling
- Pod Disruption Budgets on stateful services

## 15.2 — Service mesh

Linkerd or Istio. mTLS between all internal services automatically. Reuses the Phase 2 mTLS plumbing.

## 15.3 — Stateful services in K8s

- **PostgreSQL:** managed (RDS, Cloud SQL, AlloyDB) — not self-hosted in K8s. The opex isn't worth it.
- **Redis:** managed (ElastiCache, Memorystore) or operator-managed (e.g., Redis Enterprise on K8s).
- **Kafka:** managed (Confluent Cloud, MSK) — Phase 15 is when you upgrade from single-broker dev to a real cluster.
- **ClickHouse:** ClickHouse Cloud or self-hosted on K8s with the operator.
- **MinIO/S3:** S3 in production; MinIO only for dev/air-gapped.

## 15.4 — Multi-region control plane

Two regions to start (e.g., `us-east`, `eu-west`):

- Active-active reads
- Single writer with failover (don't try multi-master writes for the control plane; conflict resolution for policy data is too dangerous)
- Logical replication of PostgreSQL between regions
- DNS-based failover via Route53 / Cloudflare with health checks
- Kafka MirrorMaker 2 for audit topics

## 15.5 — Regional proxy fleets

Each region runs its own proxy fleet, sized to local tenant traffic. Tenant data residency setting routes the proxy → DB connection within-region only.

## 15.6 — Backup & disaster recovery

- Continuous WAL archiving (PostgreSQL)
- Daily base backups, retained 30 days
- Audit WORM bucket: cross-region replicated, retained per tenant compliance config
- **Quarterly DR drill** — actually fail over to the secondary region, run a full test suite, fail back

## 15.7 — Performance load testing

Run the k6 / Locust suite at 5× expected peak. Profile:
- Proxy CPU / memory
- PDP CPU / Redis IOPS
- Kafka throughput
- ClickHouse ingest

Address the bottlenecks. Set capacity-planning headroom at 3× steady-state.

## 15.8 — Cost controls

- Per-tenant budget alerts
- LLM cost optimization (cache identical prompts, route to smaller models where possible)
- Spot/preemptible nodes for stateless workloads
- Idle environment shutdown for `dev` outside business hours

## Exit criteria

- [ ] System handles 5× expected peak with < 15ms p99 proxy overhead
- [ ] Failover from primary region to secondary completes in < 30 min
- [ ] DR drill executed successfully twice in staging
- [ ] All stateful services run on managed offerings, not self-host
- [ ] mTLS between all internal services

## Risks

- **DR-on-paper.** Drills not run = DR doesn't work. Schedule and execute. No exceptions.

---

# Phase 16 — Compliance, Hardening, GA Launch

**Goal:** SOC 2 Type II evidence collected, pentest passed, GA launched.

**Dependencies:** Phases 0–15.

**Owner profile:** Security engineer + compliance lead.

## 16.1 — SOC 2 Type II preparation

The Type II requires evidence over a 6-month period. Start collecting evidence from Phase 0 (your `policy_audit` log is most of it). Specifically:

- Access reviews (quarterly) — collected automatically from `policy_audit`
- Change management — git history + ArgoCD deploys
- Incident response — runbook + actual incident records
- Vendor management — SBOM + sub-processor list
- Security training — track in HR system
- Engage a SOC 2 auditor 3 months before target date

## 16.2 — ISO 27001 (parallel track)

Many controls overlap with SOC 2. Most expensive control to satisfy: information security risk assessment. Use a tool like Vanta or Drata to automate evidence collection.

## 16.3 — HIPAA (if pursuing healthcare)

- Business Associate Agreement (BAA) template
- PHI handling runbook
- Encryption at rest with tenant-scoped keys (KMS integration)
- Per-tenant data residency

## 16.4 — External penetration test

Hire a reputable firm. Provide them:
- Source code (or representative samples)
- A dedicated test environment with realistic data
- A list of suspected weak points (be honest)

Fix critical and high findings before GA. Medium and low findings get tickets.

## 16.5 — Bug bounty program

Soft-launch with HackerOne or Intigriti private program first. Public program after 6 months of GA stability. Pay realistic bounties; cheaping out costs more than paying.

## 16.6 — Documentation completeness

For GA, every customer-facing surface needs docs:
- Getting Started guide
- Concept docs (RBAC, ABAC, policies, retrieval)
- API reference (auto-generated from OpenAPI)
- SDK references
- Runbooks for customer-side ops (rotating keys, etc.)

## 16.7 — Pricing & packaging finalization

- Starter, Pro, Enterprise tiers
- Usage-based billing for AI tokens (mark up over LLM cost; otherwise you're a loss leader)
- Annual contracts for Enterprise
- Free tier for design partners (V1 only)

## 16.8 — GA launch checklist

- [ ] SOC 2 Type II evidence complete
- [ ] Pentest critical/high findings closed
- [ ] All four day-one dashboards in production
- [ ] All paging alerts wired and tested
- [ ] On-call rotation established
- [ ] Customer support tooling (Zendesk / Intercom) integrated
- [ ] Status page (statuspage.io or similar) live
- [ ] Disaster recovery drilled in production-replica
- [ ] Customer onboarding flow tested by 3 design partners
- [ ] Public marketing site live with pricing
- [ ] Documentation site live and complete

## Exit criteria

GA launch. Real money. Real customers. Real on-call.

---

# Critical Cross-Cutting Concerns

These don't fit neatly in one phase but show up throughout. Build them into engineering discipline from day one.

## A. Feature flags everywhere

Every phase ships behind a flag. Defaults differ per environment:
- `dev`: all flags on
- `staging`: matches `prod` (tested config)
- `prod`: flags rolled out per tenant in cohorts

Use **Unleash** (open source) or **LaunchDarkly** (commercial).

## B. Per-tenant config

Tenants have meaningfully different needs:
- Authentication methods (some need SAML, some OIDC)
- Compliance modes (HIPAA flags additional logging)
- Approval workflows (some require dual approval, some don't)
- Risk score thresholds
- Data residency

Build a per-tenant config service early. Don't hard-code tenant policies in service code.

## C. Migration discipline

In a system this complex, schema migrations happen monthly. Rules:

- Forward-only in production
- Backwards-compatible for at least one release (so old code can talk to new schema)
- Big changes use the **expand-contract** pattern (add new, dual-write, migrate readers, remove old)
- Every migration is tested in `staging` for 7 days before production

## D. Documentation as code

Engineers write docs in the same PR as the code. PR template asks "have you updated docs?" CI checks for stale references.

## E. Pager fatigue

Every alert that pages must be actionable. If it isn't, it's a notification, not a page. Review weekly. The fastest way to break a team is to page them at 2 AM on flapping alerts.

## F. Customer escape valves

Every "automatic" feature needs a manual override. Auto-response too aggressive? Tenant admin can pause it. Anomaly detector flagging a service account? Allowlist it. Customers hate locked-in automation that they can't override.

---

# Appendix: Recommended Team Composition

For the full 48-week plan:

| Phase span | Roles needed                                              | Headcount |
| ---------- | --------------------------------------------------------- | --------- |
| 0–2        | 1 staff backend, 1 platform/DevOps, 1 frontend            | 3         |
| 3–6        | + 1 backend (Calcite/proxy), + 1 backend (PDP)            | 5         |
| 7–10       | + 1 AI engineer, + 1 ML engineer (start)                  | 7         |
| 11–13      | + 1 backend (multi-DB), + 1 ML engineer (full)            | 9         |
| 14–16      | + 1 SRE, + 1 security engineer                            | 11        |

Plus throughout: 1 product manager, 1 designer (3 days/week from Phase 4 onwards), 1 fractional compliance lead (last 6 months).

---

*End of implementation plan. Build in order. Ship every phase. Don't skip the simulator. Don't compromise on defense in depth. Pace yourself — this is a year of work.*
