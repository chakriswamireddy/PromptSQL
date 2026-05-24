# ADR-003: Container image base — Distroless (Go), python-slim (Python), node-alpine (TS)

**Date:** 2026-05-21  
**Status:** Accepted  
**Deciders:** Platform Lead, Security Engineer

## Context

Image base choice affects: CVE surface area, image size, debugging ergonomics, and supply-chain attestation complexity.

Options evaluated: Ubuntu, Alpine, Wolfi, Distroless.

## Decision

| Service type | Base image |
|---|---|
| Go services | `gcr.io/distroless/static-debian12:nonroot` |
| Python services | `python:3.12-slim-bookworm` (then `distroless/python3-debian12` at GA) |
| TypeScript / Next.js | `node:20-alpine` |

Multi-stage builds in all Dockerfiles: build stage uses a full SDK image; final stage copies only the binary/bundle into the minimal base.

## Consequences

**Good:**
- Distroless for Go: no shell, no package manager, minimal CVE surface
- `nonroot` user: no root processes
- Read-only rootfs enforced in Helm (`securityContext.readOnlyRootFilesystem: true`)
- CI `trivy` scan has significantly fewer findings vs Ubuntu

**Bad:**
- No shell in distroless means `kubectl exec` debugging is harder — mitigated by ephemeral debug containers (`kubectl debug`)
- Python slim still has more CVE noise than Distroless Python; accepted until GA when we migrate

## Alternatives rejected

- **Alpine for all:** musl vs glibc incompatibilities cause subtle bugs in Go CGO builds; rejected
- **Wolfi:** excellent choice but team is not yet familiar with `apko`; deferred to post-GA evaluation
