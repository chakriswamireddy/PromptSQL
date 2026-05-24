# ADR-001: Monorepo structure with pnpm workspaces and go.work

**Date:** 2026-05-21  
**Status:** Accepted  
**Deciders:** Platform Engineering Lead, Backend Lead, Frontend Lead

## Context

The platform ships 6 services (Go + TypeScript + Python), 4 shared TypeScript packages, and 4 shared Go packages. We need a unified repo strategy.

## Decision

Use a single Git monorepo with:
- `pnpm` workspaces for all TypeScript apps and packages
- `go.work` for all Go modules
- `turbo` for cached task graphs across all packages

## Consequences

**Good:**
- Single PR spans multiple services — atomic changes with no cross-repo coordination
- Shared packages (`@platform/audit-client`, `pkg/telemetry`) are importable with zero publishing overhead
- Turbo remote cache means CI is fast even as the repo grows
- `golangci-lint` and ESLint run across the whole repo in one invocation

**Bad:**
- `git clone` is large if teams only work on one service (mitigated by sparse checkout)
- Renovate grouped PRs need careful grouping to avoid noise
- Go module boundaries must be enforced manually via `depguard` (no native workspace boundary enforcement)

## Alternatives rejected

- **Poly-repo:** rejected because cross-service atomic changes require multi-repo PRs and coordinated releases, which is too costly at this scale
- **Nx:** rejected in favour of Turbo; Turbo is simpler for a mixed Go/TS/Python repo
