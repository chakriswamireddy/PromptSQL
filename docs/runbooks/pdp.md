# Runbook: pdp (Policy Decision Point)

**Owner:** Backend Team  
**SLO:** 99.99% availability, p99 latency < 50 ms  
**Last reviewed:** 2026-05-21

## Health checks

- Liveness: `GET /healthz` → 200
- Readiness: `GET /readyz` → 200 (checks DB + cache connectivity in Phase 3+)

## Common issues

| Symptom | Check | Action |
|---------|-------|--------|
| Decision latency > 50 ms | Cache hit ratio metric | Check Redis connectivity; verify two-tier cache warm |
| Wrong policy decisions | Audit trail in ClickHouse | Check policy version; use simulator in admin console |
| Service not starting | Feature flag `pdp-v1` in Unleash | Enable flag; check DB migrations are applied |

## Cache invalidation (Phase 3+)

Force policy cache flush:
```bash
redis-cli -h <host> DEL "pdp:policy:*"
```

## Rollback

```bash
helm rollback pdp <previous-revision> -n governance
```
