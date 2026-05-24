# Runbook: Certificate Renewal Failure

**Severity:** High (Critical if cert already expired)  
**Owner:** Platform / SRE Team  
**Last reviewed:** 2026-05-21

## Symptoms

- Browser / client shows TLS certificate expired or untrusted warning
- cert-manager pods log `certificate renewal failed` or `ACME challenge failed`
- `kubectl get certificate -A` shows `READY: False`

## Triage steps

```bash
# List all certificates and their expiry
kubectl get certificate -A
kubectl describe certificate <name> -n <namespace>

# Check cert-manager logs
kubectl logs -n cert-manager deploy/cert-manager | grep -i error
```

## ACME (Let's Encrypt) renewal failure

1. Verify DNS propagation: the ACME DNS-01 challenge requires the `_acme-challenge` TXT record to resolve.
2. Check rate limits at https://letsencrypt.org/docs/rate-limits/ — staging uses `letsencrypt-staging` issuer for testing.
3. Check cert-manager `ClusterIssuer` status: `kubectl describe clusterissuer letsencrypt-prod`
4. Delete and re-create the `Certificate` resource to force a renewal attempt.

## AWS ACM renewal failure

ACM renews automatically when the cert is in use by a load balancer. If renewal fails:
1. Check that the cert is associated with a load balancer in the ACM console.
2. Validate the domain ownership record is still present in Route 53.
3. Contact AWS support if validation records are correct but renewal is stuck.

## Emergency — certificate already expired

1. Trigger an immediate manual renewal:
   ```bash
   kubectl annotate certificate <name> -n <namespace> \
     cert-manager.io/issuer-kind=ClusterIssuer --overwrite
   ```
2. If cert-manager is broken: provision a short-lived cert manually via `certbot` and inject as a secret.
3. Communicate the incident via the incident channel; update status page.

## Prevention

- Alert fires when any certificate expires within 14 days (`infra/alerts/platform-phase0.yml` — add to Phase 15).
- Staging cert renewal is tested as part of the deploy-staging workflow.
