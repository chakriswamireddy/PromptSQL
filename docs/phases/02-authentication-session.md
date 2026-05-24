# Phase 2 — Authentication & SessionContext Plumbing

> **Duration:** 3–5 weeks &nbsp; · &nbsp; **Owner:** Backend (Auth specialist) &nbsp; · &nbsp; **Dependencies:** Phases 0, 1
> **Companion:** [`../implementation-plan.md` §Phase 2](../implementation-plan.md)

---

## 1. Phase Objective & Business Purpose

Every request to every service must carry a **verified, typed `SessionContext`** containing identity, tenant, roles, attributes, trace IDs, and risk readiness — never trusted from headers, always re-derived server-side. Authentication is delegated to an OIDC provider; authorization claims (roles, attributes) are *resolved freshly* per request from the control plane, never cached in the JWT.

**Business rationale:** stale role-in-JWT is the single most common authorization vulnerability in B2B SaaS. By severing identity (cryptographic, short-lived) from authorization (DB-resolved, freshly read), revocation is instant and stale-token attacks are impossible. This also positions the platform to pass SOC 2 access-management controls cleanly in Phase 16.

---

## 2. Scope Boundaries & Ownership

**In scope**
- OIDC integration (Better Auth or Keycloak) with password + magic-link + TOTP MFA.
- Ed25519-signed JWT access tokens (identity-only) and opaque rotating refresh tokens.
- `SessionContext` shared schema (Zod → TS, codegen → Go).
- API gateway middleware that verifies JWT and constructs `SessionContext` per request.
- Service-to-service propagation (V1: HMAC-signed payloads; V2 mTLS in Phase 15).
- DB session helper enforcing `SET LOCAL` discipline.
- Logout, session revocation, "logout everywhere" semantics.
- MFA claim plumbing (`amr`, `mfa_at`) for Phase 14 step-up readiness.

**Out of scope**
- SAML federation (defer to Phase 11+ via Keycloak if customer demand).
- Step-up MFA flow (Phase 14).
- Mid-flight risk-based termination (Phase 14).
- Service mesh mTLS (Phase 15).

**Ownership**
- **Drives:** Backend Auth Lead.
- **Reviews:** Security (token design, MFA), Frontend lead (login UX).
- **Hand-off:** Phase 3 consumes `SessionContext` from gateway.

---

## 3. Hard Dependencies & Sequencing

- Phase 0: Vault, mTLS dev CA, OIDC mock container.
- Phase 1: `users`, `roles`, `user_roles`, `tenants` schema.
- Phase 0 OTel context propagation already in service templates.

Internal sequence: pick IdP → JWT spec → SessionContext schema → gateway middleware → per-service consumer → DB session helper → refresh token flow → logout → tests.

---

## 4. Detailed Sub-Phases & Implementation Tasks

### 2.1 — Choose AuthN

**V1 recommendation:** **Better Auth** if TS-first and SAML can wait; **Keycloak** if SAML/LDAP demand is near-term. Both expose OIDC + OAuth2; the platform contract is OIDC, so the IdP is replaceable.

Configure on Day 1:
- Password (Argon2id, configurable params).
- Email magic link.
- TOTP (RFC 6238) with backup codes.
- Account lockout: 5 failed attempts → 15-min lockout, exponential after.
- Session settings: 10-min access token, 30-day refresh, rotate on use.

### 2.2 — JWT Structure

**Identity claims only** — *never* roles, permissions, attributes:

```json
{
  "iss": "https://auth.platform.io",
  "aud": "platform-api",
  "sub": "user-uuid-v7",
  "tenant": "tenant-uuid-v7",
  "session_id": "session-uuid-v7",
  "amr": ["pwd", "totp"],
  "mfa_at": 1735660000,
  "iat": 1735660000,
  "exp": 1735660600,
  "jti": "jwt-uuid-v7"
}
```

- **Signature:** Ed25519 (`EdDSA`). Faster verify than RS256, smaller token, no parameter ambiguity.
- **Key rotation:** monthly; JWKS endpoint exposes current + previous (≤ 1 month overlap).
- **Algorithm allowlist:** verifiers reject any `alg` not in `["EdDSA"]`. Defends against `alg=none` and key-confusion attacks.
- **`iss` and `aud`** strictly checked. Mismatch → 401, no leakage.
- **Replay protection:** `jti` recorded in a Redis bloom filter for 11 minutes (access TTL + clock skew).

### 2.3 — SessionContext: Canonical Shape

`packages/shared-types/session.ts`:

```ts
export const SessionContext = z.object({
  userId: z.string().uuid(),
  tenantId: z.string().uuid(),
  sessionId: z.string().uuid(),
  roles: z.array(z.string()),                       // resolved server-side per request (60s cache)
  attributes: z.object({
    department: z.string().optional(),
    campusId: z.string().uuid().optional(),
    region: z.string().optional(),
    clearanceLevel: z.number().int().min(0).max(10).optional(),
    mfaSince: z.date().optional(),
    deviceTrust: z.enum(['managed','byod','unknown']).default('unknown'),
    networkTrust: z.enum(['corp','vpn','public']).default('public'),
  }),
  requestId: z.string().uuid(),
  traceId: z.string(),
  parentSpanId: z.string().optional(),
  isBreakGlass: z.boolean().default(false),
  riskScore: z.number().int().min(0).max(100).optional(),       // populated from Phase 13
  issuedAt: z.date(),
  expiresAt: z.date(),
});
```

Codegen Go struct + JSON tags via `quicktype` or a small custom generator pinned in CI.

### 2.4 — API Gateway Middleware

Per-request pipeline in `apps/api-gateway` (Go + Gin):

1. **Parse bearer token**, reject malformed.
2. **Verify signature** against JWKS (cached 5 min, refresh on `kid` miss).
3. **Validate `iss`, `aud`, `exp`, `iat`, `jti`** (replay check).
4. **Resolve roles/attributes** from PG (`user_roles` JOIN `roles`) — Redis cache `user:{userId}:roles` TTL 60s. Cache key versioned by `users.session_invalidated_at` to make revocations instant.
5. **Check `users.session_invalidated_at > token.iat`** → 401 if invalidated.
6. **Generate `requestId`**, propagate W3C `traceparent`.
7. **Build `SessionContext`**, sign internal HMAC header for downstream, attach to context.
8. Forward via gRPC/HTTP. Failure modes: 401 (auth), 403 (tenant suspended), 503 (PG unavailable but allow `app_read` services to degrade per policy).

### 2.5 — Service-to-Service Trust

**V1 — HMAC-signed SessionContext**
- Gateway computes `HMAC-SHA256(secret, base64(SessionContextJSON))`.
- Downstream services verify with shared secret (rotated weekly via Vault).
- Header: `X-Session-Context`, `X-Session-Context-Sig`, `X-Session-Context-KeyId`.
- 60-second freshness window prevents replay.

**Path to V2 (Phase 15) — mTLS + short-lived service tokens**
- Each internal service has a Vault-issued cert with SAN encoding service name.
- Gateway mints a downstream-bound JWT (`aud=pdp`) per call.
- Service mesh sidecar enforces mTLS automatically.

V1 ships HMAC inside a private network; V2 lands when K8s + Linkerd land.

### 2.6 — DB Session Discipline

`pkg/db/withSession.go`:

```go
func WithSession(ctx context.Context, sc SessionContext, fn func(tx pgx.Tx) error) error {
  return pool.BeginTxFunc(ctx, txOpts, func(tx pgx.Tx) error {
    if _, err := tx.Exec(ctx, "SET LOCAL ROLE app_read"); err != nil { return err }
    if _, err := tx.Exec(ctx,
        "SELECT set_config('app.user_id', $1, true)," +
        "       set_config('app.tenant_id', $2, true)," +
        "       set_config('app.session_id', $3, true)," +
        "       set_config('app.break_glass', $4, true)",
        sc.UserID, sc.TenantID, sc.SessionID, strconv.FormatBool(sc.IsBreakGlass),
    ); err != nil { return err }
    return fn(tx)
  })
}
```

- Lint rule (`semgrep`) rejects direct `pool.Query` / `pool.Exec` outside this helper.
- `set_config(..., true)` is equivalent to `SET LOCAL`; safe with PgBouncer transaction pooling.
- Helper records `app.request_id`, `app.trace_id` for observability via `pg_stat_activity`.

### 2.7 — Refresh Tokens & Session Lifecycle

- Access token: 10 min, JWT, stateless.
- Refresh token: 30 days, **opaque**, stored in Redis as `refresh:{sha256(token)} → {userId, sessionId, prevTokenId}`.
- Rotation: every refresh issues new RT, invalidates the old one. **Reuse of an old RT triggers session invalidation** (token theft signal) — set `users.session_invalidated_at = now()` and emit `auth.token.reuse_detected` audit event.
- Logout: deletes RT from Redis; AT continues until expiry (accepted tradeoff).
- "Logout everywhere": sets `users.session_invalidated_at = now()`; gateway revalidates every request against this column (cached 60s).
- IdP-initiated SLO: `back-channel logout` consumed if available.

### 2.8 — MFA & Step-Up Readiness

- JWT carries `amr` and `mfa_at`.
- Phase 14 will introduce obligations of the form `require_mfa_within=5m`; Phase 2 only ensures the claims and SessionContext attribute exist.
- Backup codes stored hashed (Argon2id); single-use enforced via DB unique index on the hash.

### 2.9 — Login UX (in-scope minimum)

- Next.js login page (Phase 4 wires the full admin console; Phase 2 only ships a minimal page).
- Password + TOTP + magic-link.
- CAPTCHA via `Turnstile` after 3 failed attempts.
- Rate-limit by IP + email; lockout per the IdP policy.
- "Forgot password" flow with time-limited tokens + invalidation on use.

### 2.10 — Tests

- **Unit:** JWT parser fuzz; `alg=none`, missing claims, wrong aud, signature tampered, expired, future-dated, oversized.
- **Integration:** end-to-end login → call internal service → DB query → RLS asserts tenant_id from `SET LOCAL` matches token.
- **Property:** removing `SET LOCAL app.tenant_id` always yields 0 rows on tenant-scoped tables.
- **Security:** JWT replay rejected; old refresh token reuse invalidates session; logout everywhere makes all in-flight tokens stale within 60s.

---

## 5. Architectural Gaps & Missing Requirements

1. **Service account / machine identity.** What's a non-human caller's token? Recommendation: a `service_accounts` table + Vault-issued mTLS cert; introduce in Phase 2.5 even if minimal.
2. **API key surface for customers.** Customers will demand keys for CLI/SDK. Reserve `api_keys` schema (hashed only), scope = user + tenant, with per-key TTL and rate limit.
3. **OIDC back-channel logout** support — implement now or document deferral.
4. **Device binding.** `deviceTrust` exists but no enrollment flow. Phase 14 builds it; Phase 2 ships the attribute as `unknown`.
5. **JWKS caching semantics under outage.** If JWKS endpoint unreachable, do we fail closed or accept previously seen keys? Decision: serve stale up to 30 min, then 503.
6. **Cross-tenant user identity.** A consultant who serves multiple tenants — one user record per tenant or single user with tenant-scoped sessions? Recommendation: one user per tenant; users with identical emails across tenants are *not* the same user.
7. **Time skew tolerance.** Default ±60s; document and configurable.
8. **Trace propagation across IdP boundary.** Use OIDC `state` to carry a correlation ID through the IdP redirect.

---

## 6. Edge Cases & Failure Modes

| Scenario                                                          | Mitigation                                                                                       |
| ----------------------------------------------------------------- | ------------------------------------------------------------------------------------------------ |
| JWKS endpoint slow or down                                        | Cache 5 min, stale-while-revalidate up to 30 min, then 503.                                      |
| Token issued just before key rotation                             | Verifier accepts current + previous keys; overlap ≥ 1 month.                                    |
| Replay of access token within 10 min                              | `jti` bloom filter in Redis (false-positive rate ≤ 0.01% acceptable; on hit, compare full `jti`).|
| Refresh token reuse (theft)                                       | Invalidate session, audit event, force re-login.                                                 |
| User deactivated mid-session                                      | `session_invalidated_at` updated; next gateway check (≤ 60s) rejects.                            |
| Role change mid-session                                           | Role cache key versioned by `user_roles.updated_at`; 60s tail latency acceptable; force-invalidate via pub/sub for sensitive grants. |
| Multiple devices logged in; one logs out                          | Per-session refresh tokens; logout affects only that session.                                    |
| PG unreachable during gateway role resolution                     | Serve from Redis cache up to TTL; emit `auth.degraded` metric; deny new sessions.                |
| Redis unreachable                                                  | Gateway falls back to PG with circuit breaker on PG load; new logins slower but functional.      |
| Clock drift on a service                                          | NTP enforced; PrometheusAlert if drift > 5s.                                                     |
| Magic-link replay                                                  | Single-use; nonces stored in Redis with TTL = link expiry.                                       |
| User exists in IdP but not in `users`                             | JIT provisioning with default `analyst` role + tenant assignment from IdP claim; audited.       |

---

## 7. Non-Functional Concerns

### 7.1 Scalability
- JWT verification is CPU-bound: Ed25519 ≈ 100k verify/s per core; gateway sized for 5× peak.
- Redis cache TTL 60s for roles → at 10k RPS, peak DB load is ~166 role lookups/sec — trivial.
- JWKS fetch is rare (~once per pod per hour); never on hot path.

### 7.2 Security
- HSTS on all auth domains; secure, http-only, SameSite=strict cookies for refresh tokens (if cookie flow).
- CSRF protection: double-submit cookie + `SameSite=strict`; explicit `Origin` allowlist for cross-origin.
- Constant-time comparisons for password and TOTP checks.
- Refresh-token reuse detection is mandatory.
- Logging: tokens NEVER logged, even hashed; structured logger has a `secret` redactor.
- WebAuthn (passkeys) on roadmap (Phase 14); architecture ready via `amr` claim.

### 7.3 Multi-Tenant Isolation
- `tenant` claim is the bedrock; every downstream service uses it via `SET LOCAL app.tenant_id`.
- Cross-tenant identity attacks: gateway rejects requests where the URL path tenant slug ≠ JWT tenant.
- Tenant suspension: `tenants.status='suspended'` short-circuits gateway with 403.

### 7.4 Concurrency
- Refresh-token rotation uses Redis `WATCH/MULTI/EXEC` (or Lua) to atomically swap; concurrent refresh of same token: one wins, the other triggers reuse-detection.
- Role cache stampede prevented by single-flight per `(tenantId, userId)` key.

### 7.5 Performance
- Gateway request-decoration p99 budget: < 5 ms (JWT verify + Redis hit).
- Cold path (cache miss) p99: < 25 ms.
- Login flow p99: < 500 ms (IdP-bound).

---

## 8. Recommended Improvements

### Architecture
- Treat the gateway middleware as a **library** in `packages/auth-middleware` so internal services can re-validate independently (defense in depth).
- Introduce **`subject` abstraction** so users, service accounts, and API keys all surface as `SessionContext` with `subjectKind ∈ {user, service, apikey}`.

### DX
- A `make login` script issues a dev JWT for any seeded user + tenant, with optional MFA simulation.
- TS + Go client SDKs auto-attach bearer + tenant + trace headers.
- A `make impersonate user@tenant` issues a tagged token (audit-flagged) for support workflows.

### UX
- Login page: clear MFA setup; recovery codes downloadable as text & PDF.
- "Where am I signed in?" page lists active sessions (device fingerprint hash, IP geolocation, last activity); per-session logout button.
- Magic-link emails include sender authentication (SPF/DKIM/DMARC) — coordinate with deliverability.

### Reliability
- Circuit breakers around IdP + PG + Redis with budgets and per-tier fallback.
- Health: `/healthz` (process up), `/readyz` (deps healthy), `/livez` (event loop responsive).

### Observability
- Per-request OTel span includes `enduser.id` (hashed), `enduser.tenant`, `auth.method`, `auth.mfa`, `auth.cache_hit`.
- Metrics: `auth_jwt_verify_duration`, `auth_role_resolve_duration`, `auth_failed_total{reason}`, `auth_replay_total`, `auth_reuse_detected_total`.
- Audit: emit `auth.login.success`, `auth.login.failure`, `auth.mfa.enroll`, `auth.token.reuse_detected`, `auth.logout.everywhere`.

### Maintainability
- Token spec lives in `docs/auth/jwt-spec.md`, versioned; CI checks codegen matches.
- ADRs for: choice of IdP, Ed25519 over RS256, identity-only JWT decision.

---

## 9. Technical Considerations

### 9.1 DB Design
- `users.session_invalidated_at` indexed (used per-request).
- `api_keys` (reserved for Phase 2.5): id, hashed secret, owner_user_id, scopes[], expires_at, last_used_at, revoked_at.
- `service_accounts` (reserved): id, tenant_id, name, default_role, attached_cert_fingerprint.

### 9.2 API Contracts
- `POST /v1/auth/login` (OIDC redirect-initiated).
- `POST /v1/auth/refresh`.
- `POST /v1/auth/logout`.
- `POST /v1/auth/logout-everywhere`.
- `GET  /v1/auth/sessions` (active sessions list).
- `DELETE /v1/auth/sessions/{id}`.
- `POST /v1/auth/mfa/enroll`, `verify`, `disable`.
- All bodies validated via Zod/OpenAPI; 4xx returns RFC 7807 problem+json.

### 9.3 RBAC
- Application RBAC begins here: roles loaded from PG, attached to `SessionContext`.
- Role hierarchy via `roles.parent_role_id`; resolution flattened at load time and cached.
- Tenant-scoped roles only; cross-tenant grants impossible by schema.

### 9.4 Validation Flows
- Zod schemas in `packages/shared-types/auth` are the single source of truth for inputs/outputs.
- Server re-validates everything received from clients.

### 9.5 Caching
- `user:{userId}:roles` Redis TTL 60s; key versioned by `users.updated_at`.
- JWKS cache 5 min in-process.
- Tenant status cache 30s.

### 9.6 Queues & Background Jobs
- Token cleanup: delete expired refresh tokens nightly (Redis TTLs handle most; sweeper covers stragglers).
- Failed-login analytics → ClickHouse (via Phase 5 pipeline).

### 9.7 Audit Logs
- Login success/failure, MFA enroll/disable, password reset, refresh token reuse, logout, session terminate → `policy_audit` and Phase 5 pipeline.

### 9.8 Retry & Idempotency
- Login/refresh idempotency keys to prevent double-grant under network flakiness.
- gRPC interceptor adds retry-with-backoff only on idempotent endpoints.

### 9.9 Monitoring
Alerts:
- `auth_failed_total` rate > 10× baseline / 5 min → page security.
- `auth_reuse_detected_total > 0` → page security.
- IdP error rate > 1% / 5 min → page on-call.
- Replay attempt detected → security event (not page; weekly review).

### 9.10 CI/CD
- Token spec contract tests run on every PR.
- Auth integration tests run against an ephemeral OIDC mock; secondary suite runs against a real Keycloak in `dev`.
- Secret rotation drill scripted; CI exercises monthly in `staging`.

---

## 10. Risks, Rollback & Future Extensibility

### Risks
| Risk                                                                              | Likelihood | Impact   | Mitigation                                                                                  |
| --------------------------------------------------------------------------------- | ---------- | -------- | ------------------------------------------------------------------------------------------- |
| Roles cached in JWT (pressure from PMs for "performance")                        | High       | Critical | ADR documenting why this is forbidden; code review veto; SOC 2 control mapped.              |
| Refresh-token theft without rotation detection                                    | Med        | Critical | Reuse-detection is mandatory; security drill quarterly.                                     |
| JWKS endpoint outage                                                              | Low        | High     | Stale-while-revalidate + 30-min grace.                                                      |
| Clock skew breaks token validation                                                | Low        | Med      | NTP + 60s skew tolerance + alert.                                                            |
| IdP misconfiguration grants over-broad default roles                              | Med        | High     | JIT provisioning audited; default role = `analyst`; super-admin grants only via UI.         |
| Lateral movement via leaked HMAC service secret                                   | Med        | High     | Rotate weekly; per-service secret; mTLS migration in Phase 15 ends this risk.               |
| Session-invalidation cache (60s) leaves window for revoked user                   | Med        | Med      | Pub/sub fanout cuts to < 1s for sensitive grants (admin role, super-admin).                 |

### Rollback
- Feature-flag every middleware variant; rollback to pre-`SessionContext` mode is a flag flip.
- Backwards-compatible token schema for ≥ 2 releases when changing claims.
- Refresh-token rotation reversible by per-tenant flag during incident.

### Future Extensibility
- WebAuthn / passkeys plug into `amr` and `mfa_at`.
- SAML/LDAP plug via Keycloak federation.
- `subjectKind` extension makes ML agents and service accounts first-class.
- Device-binding adds an attribute, no schema change.

---

## 11. Deliverables & Acceptance Criteria

### Deliverables
- [ ] OIDC IdP configured in `dev`/`staging`/`prod` namespaces.
- [ ] Ed25519 JWT signing + JWKS endpoint live.
- [ ] `SessionContext` Zod schema + Go codegen.
- [ ] Gateway middleware deployed and exercised by ≥ 2 downstream services.
- [ ] DB session helper enforcing `SET LOCAL` discipline; lint live.
- [ ] Refresh-token rotation + reuse detection.
- [ ] MFA (TOTP) enroll + verify.
- [ ] Logout + logout-everywhere.
- [ ] OTel spans showing identity propagation across ≥ 3 services.

### Acceptance Criteria
- [ ] Tampered/expired JWT → 401, no leakage.
- [ ] Removing `SET LOCAL app.tenant_id` from any tenant query yields 0 rows.
- [ ] Refresh-token reuse triggers session invalidation within 60s globally.
- [ ] Role change reflected in PDP/services within 60s; sensitive grants within 1s via pub/sub.
- [ ] Service-to-service HMAC verified end-to-end.
- [ ] Load test: 10k RPS sustained at < 5 ms p99 gateway overhead.

---

## 12. Production Readiness Checklist

- [ ] JWKS rotation runbook + monthly cron tested.
- [ ] HMAC service-secret rotation runbook + weekly cron tested.
- [ ] Alerts wired and tested via synthetic incidents.
- [ ] Pen-test scenario list: token tamper, key confusion, replay, reuse, JIT abuse.
- [ ] Login UI passes accessibility audit (WCAG AA).
- [ ] Disaster scenarios runbook: IdP outage, PG outage, Redis outage, JWKS outage.

---

## 13. Remaining Risks Carried Forward

- **HMAC service trust (V1)** is a single shared-secret blast radius. Phase 15 hardens with mTLS.
- **No risk score yet** — `SessionContext.riskScore` is unused until Phase 13.
- **No step-up flow** — `mfa_at` claim exists, the obligation handler arrives in Phase 14.
- **No device binding** — `deviceTrust` stays `unknown` until enrollment lands.
- **API keys + service accounts** unimplemented; document as Phase 2.5 fast-follow if integration partners demand.
