# Runbook: api-gateway

**Owner:** Platform Team  
**SLO:** 99.9% availability, p99 latency < 500 ms  
**Last reviewed:** 2026-05-21

## Health checks

- Liveness: `GET /healthz` → 200
- Readiness: `GET /readyz` → 200

## Common issues

| Symptom | Check | Action |
|---------|-------|--------|
| 5xx rate spike | OTel traces in Jaeger; error logs | Identify slow downstream; trip circuit breaker |
| Latency p99 > 500 ms | `request_duration_seconds` histogram | Check downstream PDp latency; scale replicas |
| Service not starting | Feature flag `api-gateway` in Unleash | Enable flag or check env config |

## Scaling

Phase 15 adds HPA. Until then, scale manually via `kubectl scale` or adjust `replicas` in the Helm chart.

## Rollback

```bash
helm rollback api-gateway <previous-revision> -n governance
```
