# Runbook: Container Registry Outage

**Severity:** High  
**Owner:** Platform Team  
**Last reviewed:** 2026-05-21

## Symptoms

- Deployments fail at image pull: `ImagePullBackOff` or `ErrImagePull`
- CI build-and-push steps fail with registry connection errors
- `docker pull <image>` returns 5xx or network errors

## Triage steps

1. Determine which registry is affected: GitHub Container Registry (ghcr.io), ECR, or GAR.
2. Check the registry's status page or cloud provider status dashboard.
3. Test connectivity: `curl -I https://ghcr.io/v2/` (or equivalent for your registry).

## Mitigation

### ghcr.io outage
- Running pods continue to run (image already pulled); only new pod scheduling is affected.
- If a deployment is critical: promote the last known-good image from the internal mirror.
- Internal mirror is at `<internal-registry>/governance-platform/*`.

### ECR / GAR outage
- Same as above — use the regional replica registry if provisioned.
- Check that `imagePullSecrets` and IRSA annotations are still valid.

### Rollback a failed deploy
```bash
# Helm rollback (Phase 15+)
helm rollback <release> <revision> -n <namespace>
# Or force re-deploy of the last stable image tag
kubectl set image deployment/<name> <container>=<image>:<stable-sha>
```

## Prevention

- All production manifests reference immutable SHA digests, not floating tags.
- Critical images are mirrored to internal ECR/GAR on every successful CI build.
- Nightly supply chain scan re-pulls from the internal mirror (`.github/workflows/nightly-supplychain.yml`).
