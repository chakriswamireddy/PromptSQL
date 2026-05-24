# Runbook: schema-crawler

**Owner:** Backend Team  
**SLO:** 99.5% availability, p99 crawl latency < 5 s  
**Last reviewed:** 2026-05-21

## Health checks

- Liveness: `GET /healthz` → 200
- Readiness: `GET /readyz` → 200

## Common issues

| Symptom | Check | Action |
|---------|-------|--------|
| Stale schema in catalog | Last crawl timestamp in DB | Trigger manual crawl via API (Phase 7+) |
| Crawler fails with DB error | Crawler logs; Postgres connectivity | Check DB credentials in Vault; verify network access |
| Embedding generation slow | GPU / CPU utilization | Scale embedding workers; check model endpoint latency |

## Manual crawl trigger (Phase 7+)

```bash
curl -X POST http://schema-crawler:8082/v1/crawl \
  -H "Authorization: Bearer <service-token>"
```

## Rollback

```bash
helm rollback schema-crawler <previous-revision> -n governance
```
