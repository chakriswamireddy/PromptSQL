# Runbook: CI Outage

**Severity:** High  
**Owner:** Platform Team  
**Last reviewed:** 2026-05-21

## Symptoms

- PRs stuck in "Waiting for status checks" with no runner pickup
- GitHub Actions UI shows queued jobs that never start
- Engineers cannot merge to `main`

## Triage steps

1. Check [GitHub Status](https://www.githubstatus.com/) — hosted runner outage may be upstream.
2. Check self-hosted runner pool health: `gh api /repos/{org}/{repo}/actions/runners`.
3. Check runner VM health in the cloud console (CPU, disk, network).
4. Confirm the `ACTIONS_RUNNER_REGISTRATION_TOKEN` secret has not expired.

## Mitigation

### Hosted runner outage
- Wait for GitHub to restore service; subscribe to the status page.
- If blocking a hotfix: obtain manual approval from two leads and merge directly under documented break-glass procedure.

### Self-hosted runner down
1. SSH into runner VM or use cloud console session manager.
2. Check runner service: `sudo systemctl status actions.runner.*`
3. Restart if unhealthy: `sudo systemctl restart actions.runner.*`
4. If disk full: `journalctl --vacuum-size=500M` and clear `_work/` dirs for stale jobs.
5. If runner deregistered: re-register using a fresh token from GitHub → Settings → Actions → Runners.

### `main` broken and blocking all teams
1. Identify the offending commit: `git bisect` or look at the last green run in CI.
2. Revert the commit immediately — do not attempt a fix forward under pressure.
3. Open a post-mortem issue within 24 h.

## Prevention

- Self-hosted runners use auto-scaling group with min=2 healthy instances.
- Monitor runner queue depth via Prometheus `github_runner_queue_depth` metric.
- Alert fires if queue depth > 5 for > 10 min (see `infra/alerts/platform-phase0.yml`).
