## Summary
<!-- 1-3 bullets: what changed and why -->
-

## Scope
- [ ] Feature / bug in: <!-- service or package name -->
- [ ] Migration included: <!-- yes / no -->
- [ ] Feature flag: <!-- flag name, or "none" -->

## Test plan
- [ ] Unit tests added/updated
- [ ] Integration tests added/updated
- [ ] Manually tested locally (`make up && ...`)
- [ ] Edge cases from the phase plan covered

## Rollback plan
<!-- How do we revert this? (Revert PR / disable flag / rollback migration) -->

## Observability impact
- [ ] New OTel spans added
- [ ] New Prometheus metrics added
- [ ] Alert rule updated

## Security impact
- [ ] No new secrets introduced / all secrets via Vault
- [ ] No raw SQL string concatenation
- [ ] RLS enforced on any new table
- [ ] No cross-tenant data path introduced
