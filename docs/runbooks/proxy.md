# Runbook: proxy (PEP PostgreSQL Proxy)

**Owner:** Backend Team  
**SLO:** 99.9% availability, p99 latency < 200 ms  
**Last reviewed:** 2026-05-21

## Health checks

- Metrics: `GET /metrics` (Prometheus scrape)
- PostgreSQL wire: connect on port 5433 as any tenant user

## Common issues

| Symptom | Check | Action |
|---------|-------|--------|
| Connection refused | Proxy process alive? | Check pod status; review feature flag `pep-postgres-proxy` |
| Permission denied (legitimate) | PDP decision log | Normal; check policy configuration |
| Permission denied (unexpected) | PDP trace in Jaeger | Verify SessionContext propagation; check RLS role set |
| SQL rewrite error | Calcite sidecar logs | Check sidecar health; review SQL AST parsing |

## Rollback

```bash
helm rollback proxy <previous-revision> -n governance
```

> The proxy must never leak original table/column names in error messages — all errors surface as generic `permission denied` to the client.
