# Phase 0 — Foundation: Repository, Infrastructure, Environments, CI/CD

> **Duration:** 1–2 weeks &nbsp; · &nbsp; **Owner:** Platform / DevOps &nbsp; · &nbsp; **Dependencies:** None
> **Companion:** [`../implementation-plan.md` §Phase 0](../implementation-plan.md)

---

## 1. Phase Objective & Business Purpose

Establish the engineering substrate on which every subsequent phase depends. By the end of this phase, a new engineer can `git clone && make up` and have a working multi-service stack in under 15 minutes, with observability, CI, secret management, and feature flags wired before any business code is written.

**Business rationale:** every dollar spent retrofitting platform plumbing in month six costs roughly 10× the equivalent investment on day one. OpenTelemetry, feature flags, vault, environment parity, and trunk-based CI are cheap to start with and prohibitively expensive to bolt on later. Phase 0 also de-risks hiring — onboarding velocity is a leading indicator of engineering productivity.

---

## 2. Scope Boundaries & Ownership

**In scope**
- Monorepo skeleton with workspace tooling (`pnpm`, `go.work`).
- Local dev environment (Docker Compose) with all stateful services.
- Cloud landing zone for `dev`, `staging`, `prod` (separate accounts/projects).
- CI/CD baseline (PR checks, deploy-to-dev, manual prod gate).
- Observability scaffold (OpenTelemetry SDK in every service template).
- Secret management (HashiCorp Vault / cloud KMS).
- Feature-flag service (Unleash self-hosted or LaunchDarkly).
- Conventional commits, commitlint, pre-commit hooks, PR templates.

**Out of scope** *(deferred to Phase 1+)*
- Domain data model (Phase 1).
- AuthN / SessionContext (Phase 2).
- Any policy logic.
- Kubernetes / service mesh (Phase 15).

**Ownership**
- **Drives:** Platform Engineering Lead.
- **Reviews:** Security (secret handling), Eng leadership (monorepo strategy).
- **Hand-off:** to Backend lead for Phase 1.

---

## 3. Hard Dependencies & Sequencing

None upstream. Downstream lock-in: every subsequent phase depends on the choices made here. **Resist re-litigation after week 2.**

---

## 4. Detailed Sub-Phases & Implementation Tasks

### 0.1 — Monorepo Skeleton (Day 1–2)

```
governance-platform/
├── apps/{admin-console,api-gateway,ai-orchestrator,pdp,proxy,schema-crawler}
├── packages/{shared-types,policy-dsl,audit-client,ui}
├── infra/{docker-compose.yml,migrations,terraform}
├── scripts/{bootstrap.sh,seed.ts,verify-env.sh}
├── docs/{architecture-v2.md,implementation-plan.md,runbooks/}
├── .github/workflows/
├── Makefile  pnpm-workspace.yaml  go.work
```

- `pnpm` workspaces for TypeScript; `go.work` for Go modules; `turbo` for cached task graph.
- A single `Makefile` orchestrates `make bootstrap | up | down | test | seed | lint`.
- Editor config: `.editorconfig`, shared TS `tsconfig.base.json`, shared Go `golangci.yml`.
- License headers enforced via `addlicense` for Go and `eslint-plugin-header` for TS.

### 0.2 — Local Dev Environment (Day 3–5)

`infra/docker-compose.yml` services with pinned image SHAs:

| Service     | Purpose                       | Healthcheck                                  |
| ----------- | ----------------------------- | -------------------------------------------- |
| postgres    | Control-plane DB              | `pg_isready -U app`                          |
| redis       | Cache + pub/sub               | `redis-cli ping`                             |
| kafka       | Event stream (KRaft mode)     | `kafka-topics --bootstrap-server :9092 --list` |
| qdrant      | Vector store (alt: pgvector)  | `curl :6333/healthz`                         |
| clickhouse  | Audit query store             | `clickhouse-client -q "SELECT 1"`            |
| minio       | S3 + Object Lock              | `mc ready local`                             |
| vault       | Secret store (dev mode)       | `vault status`                               |
| jaeger      | Trace UI                      | `curl :16686/`                               |
| prometheus  | Metrics                       | `wget -qO- :9090/-/ready`                    |
| grafana     | Dashboards                    | `wget -qO- :3000/api/health`                 |
| oidc-mock   | Fake IdP                      | `curl :8080/.well-known/openid-configuration` |

- Stateful containers use named volumes; `make down` does **not** wipe data, `make nuke` does.
- Apps run natively (not in compose) for hot-reload, configured by `.env` referencing compose ports.
- `docker-compose.override.yml.example` documents per-engineer overrides (e.g., GPU mounts for local LLM).

### 0.3 — Cloud Landing Zone (Day 6–8)

- Separate accounts/projects per environment (`dev`, `staging`, `prod`) using AWS Organizations / GCP Projects.
- Infrastructure-as-Code (**Terraform** + remote state in S3/GCS with state-locking via DynamoDB/Firestore).
- A `core` module per env: VPC, subnets, KMS, IAM baseline, log buckets, Vault namespace.
- A `kms` module exporting per-env CMK ARNs (used for envelope encryption from Phase 1+).
- DNS zones: `dev.platform.io`, `staging.platform.io`, `platform.io`.
- Cert issuance via cert-manager + Let's Encrypt (or AWS ACM in cloud).

### 0.4 — CI/CD Baseline (Day 9–11)

GitHub Actions workflows:

| Workflow              | Trigger              | Stages                                                                                  |
| --------------------- | -------------------- | --------------------------------------------------------------------------------------- |
| `ci.yml`              | PR + push            | lint → typecheck → unit-tests → build-images → SAST (semgrep) → SCA (trivy + grype) → SBOM (syft) |
| `deploy-dev.yml`      | merge to `main`      | build → push images (SHA tag) → terraform plan/apply → smoke tests                      |
| `deploy-staging.yml`  | git tag `v*-rc*`     | image promotion → migrations dry-run → integration tests → DORA metric publish          |
| `deploy-prod.yml`     | manual + 2-approver  | blue/green deploy → progressive rollout → automated rollback on SLO breach              |
| `nightly-supplychain` | cron 04:00 UTC       | re-scan latest images for newly-disclosed CVEs                                          |

- **No floating tags in production manifests.** Image references are immutable SHA digests.
- Branch protection on `main`: require ≥1 review, signed commits, passing CI, linear history.
- Cosign signs all release artifacts; verification step gates prod deploys.

### 0.5 — Observability Scaffold (Day 12–13)

- A reusable `pkg/telemetry` (Go) and `packages/telemetry` (TS) initializes:
  - OTel tracer + meter + logger with W3C context propagation
  - Service-level resource attributes: `service.name`, `service.version`, `deployment.environment`, `tenant.id`
  - Default exporters: OTLP to local Jaeger/Prometheus in dev, OTLP gateway in cloud
- Every service template emits at minimum: traces for inbound/outbound calls, `request_duration_seconds` histogram, `request_in_flight` gauge.
- Grafana provisioning: dashboards-as-code in `infra/grafana/dashboards/*.json`, auto-loaded.
- Alertmanager rules-as-code in `infra/alerts/*.yml`. Phase 0 ships one alert: any service `up == 0` for 2 minutes.

### 0.6 — Secret Management & Feature Flags (Day 14)

- Vault auto-unseal in cloud via cloud KMS; dev mode in local Compose.
- Approles per service; static secrets injected at deploy time via Vault Agent / external-secrets-operator.
- No secrets in repo, ever. `git-secrets` + `gitleaks` pre-commit hook + CI scan.
- Unleash self-hosted (open source) with per-env instances. Strategies seeded: `default`, `userIDs`, `tenants`, `gradualRolloutSessionId`.
- TS SDK + Go SDK wrappers in `packages/feature-flags` + `pkg/featureflags`.

### 0.7 — Engineering Discipline Codified (Day 14)

- **Conventional Commits** + `commitlint` + `commitizen` for prompts.
- **Trunk-based development;** branch protection rejects branches older than 3 days via a check.
- **PR template** asks: scope, test plan, rollback plan, observability impact, security impact.
- **No direct prod access** — break-glass via Vault-issued time-boxed creds, fully audited.
- **No `TODO` without ticket.** A grep CI step fails on bare `TODO` / `FIXME`.
- **Pre-commit hooks**: `gofmt`, `eslint`, `prettier`, `markdownlint`, `gitleaks`, `commitlint`.

---

## 5. Architectural Gaps & Missing Requirements

The high-level plan is sound but leaves these unspecified — resolve in Phase 0 before code:

1. **Tenant-ID propagation contract.** Decide *now* whether tenant context flows via gRPC metadata, signed JWT claim, mTLS SAN, or all three. Pick one as authoritative, document it. Phase 2 enforces it.
2. **Service catalog & ownership registry.** Build a tiny `services.yaml` listing every service, its owner team, on-call rotation, SLO, and runbook URL. Used by alert routing.
3. **Versioning strategy.** Are services SemVer? Calendar-versioned? Tied to a monorepo version? Pin this; affects deployment automation forever.
4. **Image base policy.** Distroless? Alpine? Wolfi? Decide once. Implications for CVE noise, debugging, and supply-chain attestation.
5. **CI compute budget.** Self-hosted runners vs. GitHub-hosted? At scale, hosted minutes get expensive; plan now.
6. **SBOM retention & attestation.** SLSA level target? Sigstore? In-toto? GA needs L2+; start at L2 today.
7. **DR drill cadence and ownership** — even at Phase 0 the cadence belongs in the runbook calendar.
8. **`dev` environment data hygiene.** Synthetic data only or anonymized prod? Document the rule and enforce via CI on seed files.

---

## 6. Edge Cases & Failure Modes

| Scenario                                              | Mitigation                                                                                              |
| ----------------------------------------------------- | ------------------------------------------------------------------------------------------------------- |
| Engineer machine cannot run all containers (RAM)     | Profile `compose --profile minimal` toggles off ClickHouse + Kafka + Qdrant for narrow work.            |
| Pinned image SHA pulled from a removed upstream tag  | Mirror critical images to internal registry (ECR/GAR) with replication.                                  |
| CI minutes exhausted mid-incident                     | Self-hosted runner fallback pool + emergency contact for hosted plan upgrade.                            |
| `main` broken blocks all teams                        | Required `gh actions` quarantine ruleset; the first reviewer who notices reverts; no rebase merges.      |
| Vault dev mode used in cloud by accident              | Terraform pre-apply check rejects `dev` mode flags outside `local`.                                      |
| Secret accidentally committed                         | `gitleaks` pre-receive hook on GitHub Server / pre-commit + post-incident rotation runbook.              |
| OTel collector outage                                 | SDKs buffer in-memory bounded; on overflow, drop with `otelcol_dropped_spans_total` metric.              |
| Compose ports collide with engineer-local processes   | All ports configurable via `.env`; collisions reported by `make doctor`.                                 |

---

## 7. Non-Functional Concerns

### 7.1 Scalability
Phase 0 does not host load, but the *patterns* must scale. Specifically:
- Logger / tracer must be **sampling-aware** from day one; configurable head sampler in `pkg/telemetry`.
- Avoid synchronous logging to stdout from hot paths; use a bounded ring buffer + flusher goroutine.
- CI parallelism plan: matrix tests with shard keys; cache `go build`, `pnpm`, `next` artifacts.

### 7.2 Security
- Vault tokens scoped per service via AppRole, TTL ≤ 24h.
- Container images run as non-root, read-only rootfs, dropped capabilities. Enforced by a `dockerfile-lint` step.
- All `docker-compose.yml` services bind only to `127.0.0.1` in dev to prevent accidental LAN exposure.
- CodeQL or Semgrep ruleset enabled in CI for the languages used.
- Branch protection requires signed commits; `gpg` setup in the bootstrap script.

### 7.3 Multi-Tenant Readiness
Even before tenancy exists, all telemetry attributes reserve `tenant.id` as a first-class label. Avoid retro-fitting later.

### 7.4 Concurrency
- Makefile targets must be idempotent and safe to run concurrently. Mutex via flock for state-changing steps.
- Compose health-gated startup (`depends_on.condition: service_healthy`) so apps don't race the DB.

### 7.5 Performance Budgets
- `make bootstrap` ≤ 5 min cold; `make up` ≤ 60s warm.
- CI PR pipeline ≤ 10 min p95. If breached, sprint-block.
- Vault round-trip ≤ 50 ms inside cloud env.

---

## 8. Recommended Improvements

### Architecture
- Decide between **monorepo** vs split repos before week 1. Monorepo recommended; once chosen, invest in `turbo`/`nx` immediately.
- **`go.work` / `pnpm-workspace.yaml`** discipline: enforce module boundaries with `eslint-plugin-import` and `depguard` in Go.

### Developer Experience (DX)
- `make doctor` — diagnoses local env (Docker daemon, ports, RAM, disk, certs, SDK versions).
- `make seed` populates all services with realistic fixtures (a 5-tenant universe).
- Devcontainer (`.devcontainer/`) so GitHub Codespaces is a one-click on-ramp.
- Auto-generated typed clients for internal APIs to avoid stringly-typed payloads.

### User Experience (UX)
- Not yet applicable, but reserve a *design tokens* package (`packages/ui/tokens`) so admin console and customer apps share theming from day one.

### Reliability
- Single SLO defined at Phase 0: **CI success rate on `main` ≥ 99%.** It anchors engineering discipline before the product has its own SLOs.
- Chaos primitive in dev: `make chaos` randomly kills one compose service to verify reconnection logic.

### Observability
- Centralized log schema enforced by `packages/audit-client`/`pkg/logging` even for non-audit logs.
- Span name conventions enforced via lint: `<service>.<verb>.<resource>`.
- Trace exemplars enabled in Prometheus so dashboards link to representative traces.

### Maintainability
- Renovate or Dependabot configured for grouped weekly PRs; gated by full CI.
- Architectural Decision Records (ADRs) live in `docs/adr/` from PR #1.

---

## 9. Technical Considerations

### 9.1 DB Design
Not in this phase. **But:** reserve the `app_migration_login`, `app_login_user`, role-set creation **today** as a Postgres init script. Phase 1 then only adds schema, never roles.

### 9.2 API Contracts
- Internal services speak **gRPC** with `.proto` files in `packages/proto/`; codegen for both Go and TS lives in CI.
- Public API is **OpenAPI 3.1**, single source-of-truth in `apps/api-gateway/openapi.yaml`; clients regenerated on change.
- Schema breaking changes blocked by `buf breaking` and `oasdiff` checks in CI.

### 9.3 RBAC
N/A at infra layer, but **GitHub** RBAC is enforced now: only the platform team merges to `infra/`; only release captains create version tags.

### 9.4 Validation Flows
- Every config file (Terraform, Helm, JSON) validated in CI with `terraform validate`, `helm lint`, `ajv`.
- A `verify-env.sh` smoke-tests the dev env (DB reachable, Vault sealed/unsealed, OIDC mock returning JWKS).

### 9.5 Caching
N/A until Phase 3.

### 9.6 Queues & Background Jobs
Kafka is in dev compose but unused until Phase 5. Reserve topic naming convention now: `<env>.<domain>.<event>.<tenantId>`.

### 9.7 Audit Logs
N/A but **`packages/audit-client`** scaffolded with a no-op implementation so every service imports the same shim. Phase 5 implements behind the same interface.

### 9.8 Retry & Idempotency
gRPC interceptors in `pkg/grpc/interceptors` implement: deadline propagation, retry with jittered backoff (only on idempotent codes), idempotency-key header forwarding. These exist *before* there's anything to call.

### 9.9 Monitoring
Phase 0 alerts:
- `up == 0` for any critical container > 2 min → page on-call (PagerDuty/Opsgenie).
- CI failure rate > 5% on `main` over 24h → page platform team.
- Container restart loop > 3 in 10 min → page service owner.

### 9.10 CI/CD
- Required PR checks: lint, typecheck, unit, integration-light, SAST, SCA, SBOM-diff, breaking-change, license.
- DORA metric capture: every deploy emits an OTel event tagged with commit SHA, env, and outcome; published to a Grafana board.

---

## 10. Risks, Rollback & Future Extensibility

### Risks
| Risk                                                                          | Likelihood | Impact | Mitigation                                                                                  |
| ----------------------------------------------------------------------------- | ---------- | ------ | ------------------------------------------------------------------------------------------- |
| **Over-engineering infra before product exists**                              | High       | High   | Time-box Phase 0 to 2 weeks; defer service mesh / K8s to Phase 15.                          |
| **Pinned-version drift** (CVEs accumulate; renovate fatigue)                  | High       | Med    | Weekly grouped renovate PRs + nightly CVE re-scan.                                          |
| **Monorepo build hell** (10 min CI becomes 45 min)                            | Med        | High   | Turbo cache + remote cache from day one; PR-level affected-only tests.                      |
| **Inconsistent local environments** (works on my machine)                     | Med        | Med    | Devcontainer + `make doctor` + version-pinned tool chain via `mise`/`asdf`.                 |
| **Vault dev mode running in cloud**                                           | Low        | Critical | Terraform pre-apply guard; environment label on Vault namespace.                          |
| **CI cost overrun**                                                           | Med        | Med    | Self-hosted runner pool for non-secret workloads; budget alerts.                            |

### Rollback
Phase 0 is greenfield — rollback = revert PR. Practice it: every PR in this phase is reverted in `dev` as part of the test plan to prove revertibility.

### Future Extensibility Notes
- Reserve namespaces for K8s migration: don't bake compose-specific service discovery into app code; use env-var URLs.
- Reserve port ranges for at least 3× the services we have today.
- Reserve OTel resource attribute keys; documenting them in `docs/observability.md` now prevents naming drift.

---

## 11. Deliverables & Acceptance Criteria

### Deliverables
- [ ] Monorepo with documented bootstrap.
- [ ] `make up` brings full stack live in <15 min on a clean laptop.
- [ ] `dev`, `staging`, `prod` cloud accounts provisioned via Terraform.
- [ ] PR CI passes within 10 min p95 with lint, test, build, SAST, SCA, SBOM, license.
- [ ] OTel traces visible end-to-end in Jaeger for a hello-world request.
- [ ] Vault stores ≥ 1 secret consumed by an app.
- [ ] Feature-flag SDKs available and toggling a flag from Unleash flips behavior in a sample service.
- [ ] ADR repository established with ≥ 3 ADRs filed.

### Acceptance Criteria
- [ ] A new engineer with read access can self-serve a working dev env without help in < 2 hours.
- [ ] Every service template emits trace+metric+log out of the box.
- [ ] Every container runs as non-root with read-only rootfs; verified by CI.
- [ ] Every secret access produces a Vault audit log entry visible in Grafana.
- [ ] Branch protection enforced on `main`: signed commits, ≥1 review, CI green, linear history.

---

## 12. Production Readiness Checklist

- [ ] CI green on `main` for 5 consecutive days.
- [ ] `deploy-dev.yml` succeeded 10× consecutively.
- [ ] `deploy-staging.yml` rehearsed once with a synthetic release.
- [ ] Runbook stub exists for: CI outage, registry outage, Vault unseal failure, certificate renewal failure.
- [ ] On-call rotation defined for platform team even if there's no production traffic yet.
- [ ] Backup of CI/CD config and Terraform state verified restorable.

---

## 13. Remaining Risks Carried Forward

- **Tenancy semantics undefined** — locked in only as a propagation contract; the data model lands in Phase 1.
- **No real workload measured yet** — capacity assumptions are aspirational until Phase 6 puts traffic through the proxy.
- **No production cutover plan exists** — Phase 15/16 own that, but the chosen IaC patterns must not foreclose blue/green.
- **Compose vs K8s parity gap** — by Phase 15 there *will* be drift; document it eagerly in Phase 0's ADRs to make the migration cheap.
