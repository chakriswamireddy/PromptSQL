# ADR-002: Tenant-ID propagation via JWT claim (authoritative) + gRPC metadata (transport)

**Date:** 2026-05-21  
**Status:** Accepted  
**Deciders:** Platform Lead, Security Engineer, Backend Lead

## Context

Every service call must carry the authenticated tenant context so that:
1. PostgreSQL RLS can enforce row-level isolation via `set_config('app.tenant_id', ...)`.
2. Audit events are attributed to the correct tenant.
3. PDP policy evaluation uses the correct policy set.

Three options were evaluated: JWT claim, gRPC metadata header, mTLS SAN.

## Decision

**JWT claim is the authoritative source.** `tenant_id` is embedded in the Ed25519-signed JWT (Phase 2). Services extract it from the verified token and never trust an unverified header.

**gRPC metadata is the transport mechanism.** Services propagate `x-tenant-id` in gRPC metadata as a convenience for inter-service calls where re-issuing a JWT would be expensive. This header is only accepted from authenticated peers (mTLS or shared Vault AppRole credentials).

**mTLS SAN** is deferred to Phase 15 (service mesh). It is not the primary mechanism.

## Consequences

**Good:**
- Tamper-proof: the JWT signature prevents tenant spoofing from external callers
- Consistent with W3C trace context propagation (additive metadata model)
- Phase 15 can layer mTLS SAN verification on top without breaking the contract

**Bad:**
- Services must verify the JWT on every inbound call — mitigation: session cache in Redis (Phase 2)
- gRPC metadata propagation requires careful interceptor discipline — mitigated by `pkg/grpc/interceptors`

## Alternatives rejected

- **mTLS SAN only:** requires service mesh (Phase 15); not available in Phase 0–14
- **Custom header with no JWT:** no tamper protection; rejected for security reasons
