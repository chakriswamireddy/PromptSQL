# Runbook: ai-orchestrator

**Owner:** ML Team  
**SLO:** 99.0% availability, p99 latency < 10 s  
**Last reviewed:** 2026-05-21

## Health checks

- Liveness: `GET /healthz` → 200
- Readiness: `GET /readyz` → 200

## Common issues

| Symptom | Check | Action |
|---------|-------|--------|
| LLM timeout | LiteLLM routing logs | Check upstream provider status; switch to fallback model |
| Prompt injection detected | Audit log `outcome=blocked` | Expected; review blocked query for tuning |
| RAG returning empty results | Qdrant health; embedding service | Check Qdrant connectivity and AllowedSnapshot ACLs |
| High token spend | LiteLLM cost metrics | Check for runaway loops; review LangGraph recursion limits |

## LiteLLM model fallback

Update `LITELLM_FALLBACK_MODEL` env var and restart the pod to switch providers.

## Rollback

```bash
helm rollback ai-orchestrator <previous-revision> -n governance
```
