# Runbook: admin-console

**Owner:** Platform Team  
**SLO:** 99.5% availability, p99 latency < 1 s  
**Last reviewed:** 2026-05-21

## Health checks

- Next.js health: `GET /api/health` → 200

## Common issues

| Symptom | Check | Action |
|---------|-------|--------|
| Blank screen / 500 | Next.js server logs | Check API Gateway connectivity; verify OIDC issuer |
| Login redirect loop | OIDC mock / IdP status | Check `OIDC_ISSUER` env var; verify JWKS endpoint |
| Policy editor not loading | Feature flag `admin-console` | Enable flag in Unleash |
| Simulator returns wrong result | PDP service health | Verify PDP is running and `pdp-v1` flag is enabled |

## Rollback

```bash
helm rollback admin-console <previous-revision> -n governance
```
