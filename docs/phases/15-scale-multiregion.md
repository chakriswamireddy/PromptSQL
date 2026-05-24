# Phase 15 — Scale-Out: Kubernetes, HA, Multi-Region

> **Duration:** 36–42 weeks (≈6 weeks focused) &nbsp; · &nbsp; **Owner:** SRE + Platform &nbsp; · &nbsp; **Dependencies:** Phases 0–14
> **Companion:** [`../implementation-plan.md` §Phase 15](../implementation-plan.md)

---

## 1. Phase Objective & Business Purpose

Migrate from Docker Compose to managed Kubernetes, add service mesh + mTLS, move stateful services to managed offerings, deploy **active-active control plane across two regions** with single-writer + automated failover, and survive a regional outage with **< 30 min RTO** and **< 5 min RPO**. Run a real DR drill in `staging` and (eventually) in `prod`.

**Business rationale:** Enterprise buyers ask "where do you run, can you fail over, when did you last drill?" Concrete answers — multi-region active-active, monthly drills, < 30 min RTO — close deals. This is also the phase where compounding tech debt from earlier phases gets paid down before GA.

---

## 2. Scope Boundaries & Ownership

**In scope**
- Managed Kubernetes per cloud (EKS, GKE, AKS — pick the primary).
- Helm charts per service; ArgoCD GitOps.
- Service mesh (Linkerd) with automatic mTLS.
- Stateful services on managed offerings (RDS/Cloud SQL/AlloyDB; ElastiCache/Memorystore; MSK/Confluent Cloud; ClickHouse Cloud; S3).
- Multi-region control plane (active-active read, single-writer with failover).
- Regional proxy fleets + data residency enforcement.
- Cross-region audit replication.
- Backup, DR drills, performance load testing.
- Cost controls (per-tenant budgets, spot/preemptible, idle shutdown).
- mTLS replaces the HMAC service trust from Phase 2.

**Out of scope**
- Multi-cloud (sticking with primary cloud; multi-cloud reserved).
- Active-active *writes* for the control plane (too dangerous for policy data).
- Per-tenant dedicated clusters (Enterprise tier offering, defer).
- Serverless conversion of services (not needed).

**Ownership**
- **Drives:** SRE Lead + Platform Lead.
- **Reviews:** Security (network design, mTLS, key mgmt), Eng leads per service, Finance (cost model).

---

## 3. Hard Dependencies & Sequencing

- Phases 0–14 complete and behind feature flags.
- Phase 0 IaC patterns + observability scaffold.
- Phase 5 audit pipeline + WORM cross-region replication design.
- Phase 13 Flink checkpoints already to S3.

Sequencing: K8s landing zone → migrate one service end-to-end (e.g., PDP) → service mesh + mTLS → migrate stateful → second region → cross-region replication → DR drill → cost optimization → cutover.

---

## 4. Detailed Sub-Phases & Implementation Tasks

### 15.1 — Kubernetes Migration

- Pick **EKS** (assuming AWS primary; analogous for GKE/AKS).
- Cluster-per-environment (`dev`, `staging`, `prod`) per region.
- **Karpenter** for node autoscaling; spot instances for stateless workloads.
- **Cluster Autoscaler** as fallback.
- **HPA** + **VPA recommender mode** per workload.
- **Pod Disruption Budgets** on stateful pods.
- **Helm charts** per service in `infra/helm/`; values per environment in `infra/values/`.
- **ArgoCD** application-of-applications pattern; GitOps from `main`.
- Each service template includes: HPA, PDB, ServiceAccount with IAM Role for Service Accounts (IRSA), NetworkPolicy, OTel sidecar config.
- Migration order: PDP → admin console → ai-orchestrator → schema-crawler → live-feed → webhook-fanout → proxy → audit consumers.

### 15.2 — Service Mesh

- **Linkerd** (simpler, smaller footprint than Istio).
- Automatic mTLS between all internal services.
- Traffic policies for canary deploys.
- Per-namespace policy: zero-trust default deny; explicit allow per service-to-service path.
- Sidecar metrics scraped into Prometheus.
- **mTLS replaces** the Phase 2 HMAC-signed SessionContext trust path for internal calls. Gateway still signs the SessionContext payload; the mesh provides identity + mTLS.

### 15.3 — Stateful Services to Managed

- **PostgreSQL** → AWS RDS / Aurora PostgreSQL (or AlloyDB on GCP).
  - Multi-AZ.
  - Logical replication enabled for cross-region (`wal_level=logical`).
  - PgBouncer fleet alongside in K8s (or use RDS Proxy).
- **Redis** → ElastiCache Redis (cluster mode) or Memorystore.
- **Kafka** → MSK or Confluent Cloud; 3-broker minimum + ZooKeeper-less or external Zk.
- **ClickHouse** → ClickHouse Cloud or self-managed via Altinity operator.
- **MinIO** → S3 with Object Lock Compliance; SSE-KMS.
- **Qdrant** → Qdrant Cloud or self-managed K8s operator (if used vs pgvector).
- **Vault** → HCP Vault or self-managed HA cluster with cloud KMS auto-unseal.

### 15.4 — Multi-Region Control Plane

Two regions: e.g., `us-east-1` (primary) + `eu-west-1` (secondary).

- **Active-active reads:** both regions serve API traffic; clients geo-routed via Route53 latency-based routing.
- **Single-writer with failover:** writes pinned to primary via routing rule + per-tenant `home_region` in `tenants` table.
- **PostgreSQL logical replication** primary → secondary; promote on failover (Aurora Global if AWS).
- **Redis** per-region; not replicated for cache (lazy populated); session-state Redis replicated.
- **Kafka MirrorMaker 2** for audit topics primary → secondary.
- **ClickHouse**: per-region; queries within region; cross-region only for compliance reporting.
- **Vault** replicated; performance secondaries; failover documented.
- **DNS-based failover** via Route53 + Cloudflare with health checks (active-active read + single-writer failover orchestrated by a runbook).

**Per-tenant data residency:** `tenants.data_residency` ∈ {`us`, `eu`, `apac`, `multi`}. Routing layer enforces; cross-region traffic for `eu` tenants refused.

### 15.5 — Regional Proxy Fleets

Per region:
- Proxy + Calcite sidecar replica set.
- Backend DB connections strictly within region.
- Connection-token issued by gateway in the user's home region.
- Cross-region query refused (residency enforcement).

### 15.6 — Backup & Disaster Recovery

- Continuous WAL archiving (PostgreSQL).
- Daily base backups, retained 30 days; weekly cross-region copies retained 1 year.
- Audit WORM cross-region replicated (S3 CRR).
- ClickHouse periodic snapshot + cross-region replication.
- Flink checkpoints to S3 with CRR.
- **Quarterly DR drill** in `staging` (and once-yearly in `prod` against a real partition isolated tenant).

DR runbooks per scenario:
- PG primary region loss.
- Kafka cluster loss.
- ClickHouse loss.
- WORM bucket compromise.
- Vault unsealed unavailability.

### 15.7 — Performance Load Testing

- Run k6 / Locust at 5× expected peak for 2 h.
- Profile bottlenecks:
  - Proxy CPU/memory.
  - PDP CPU + Redis IOPS.
  - Kafka throughput.
  - ClickHouse ingest.
  - Flink state size + checkpoint latency.
- Fix top 3 bottlenecks; document headroom at 3× steady-state.

### 15.8 — Cost Controls

- Per-tenant budget alerts.
- LLM cost optimization (prompt cache; route to smaller models where possible).
- Spot/preemptible for stateless workloads.
- Idle environment shutdown for `dev` outside business hours.
- Reserved/savings plans for steady-state compute.
- ClickHouse query routing: heavy tenants → dedicated cluster; small tenants → shared.

### 15.9 — Network & Security Hardening

- Private subnets only for all stateful + most stateless workloads.
- Public ALB/CloudFront for ingress; WAF rules.
- DDoS protection (Shield Advanced / Cloud Armor).
- VPC peering or PrivateLink for cross-region replication.
- Egress controls; no service can reach the internet except explicitly allowed.
- Secrets via IRSA + external-secrets-operator pulling from Vault.
- All KMS keys per environment per region.

### 15.10 — Operational Readiness

- On-call rotation; PagerDuty integrated.
- Tier-1 runbooks per service.
- Synthetic transaction probes per region.
- SLO dashboards.
- Capacity planning doc reviewed quarterly.
- Cost dashboards per tenant + per region.
- Change-management process: production changes via PR + ArgoCD + 2-approver.

---

## 5. Architectural Gaps & Missing Requirements

1. **Active-active write architecture** for ultra-low-RTO tenants (deferred; complex policy conflict resolution).
2. **Data residency for `apac` / specific countries** — add as customers demand; design space reserved.
3. **Customer-managed clusters** (BYO K8s) — Enterprise tier; deferred.
4. **GPU node pools** for V2 ML risk model — plan capacity now.
5. **WAN-link redundancy** between regions; if cloud provider region peering fails, what's the fallback?
6. **Cold-region warm-up time.** Failover assumes secondary is warm; document warm-up SLA.
7. **Cross-region eventual consistency tolerance** documented per data type (policies vs audit vs risk).
8. **Per-tenant rate-limits at L7 edge** (Cloudflare / WAF rules).
9. **Database fleet upgrade strategy** — major version upgrades with downtime windows; document.
10. **Per-region observability stack** vs centralized; recommendation: per-region with cross-region rollup.

---

## 6. Edge Cases & Failure Modes

| Scenario                                                          | Mitigation                                                                                       |
| ----------------------------------------------------------------- | ------------------------------------------------------------------------------------------------ |
| Primary region partial outage (degraded but not down)             | Manual failover button; auto-failover requires high confidence to avoid flapping.                |
| Cross-region replication lag spike                                | Alert + halt failover if lag > RPO; prioritize manual intervention.                              |
| K8s cluster control plane outage                                  | Workloads keep running; new deploys paused; failover plan defined.                                |
| Linkerd cert rotation failure                                     | Workload identities fail to verify; automated re-issuance + alarm.                                |
| Vault unseal lost (KMS key issue)                                 | Out-of-band emergency unseal procedure; documented.                                              |
| Kafka MirrorMaker lag                                             | Per-topic lag alarms; backfill possible from local broker.                                       |
| Cost spike from runaway autoscaling                               | Per-cluster cost cap; HPA max bounded.                                                            |
| Image registry outage                                             | Mirror cache in cluster; deploy paused, not crashed.                                              |
| Cross-region DNS poisoning                                        | DNSSEC; periodic verify.                                                                          |
| ArgoCD out of sync (drift)                                        | Auto-sync with conflict alarm; manual review for safety-critical resources.                       |
| Network policy blocks legitimate traffic                          | Pre-staging test; rollback flag per namespace.                                                    |

---

## 7. Non-Functional Concerns

### 7.1 Scalability
- HPA targets defined per service.
- Karpenter selects right instance types per workload.
- Per-tenant fairness already addressed in Phase 3 / 12.

### 7.2 Security
- mTLS everywhere internal.
- Network policies (zero-trust default).
- WAF + DDoS at edge.
- Per-service IAM least-privilege via IRSA.
- KMS per env per region.

### 7.3 Multi-Tenant Isolation
- Data residency enforced at routing + DB layer.
- Per-region clusters with strict policy isolation.
- Enterprise-tier dedicated K8s namespaces if required.

### 7.4 Concurrency
- Single-writer for control-plane writes; reads load-balanced.
- Replication consistency documented.

### 7.5 Performance
- p99 proxy overhead unchanged < 15 ms.
- Cross-region failover RTO < 30 min, RPO < 5 min.
- Drill validates these regularly.

---

## 8. Recommended Improvements

### Architecture
- A clean **routing layer** (in-house or via Cloudflare Workers) that knows tenant home region.
- **Outbox pattern** end-to-end so all pub/sub events are durable across failover.
- **CDC-driven** native policy syncer (Phase 6 hourly cron → realtime CDC).

### DX
- Local K8s parity via Kind / Minikube clusters with same Helm charts.
- `kubectl` plugins for common ops.
- ArgoCD UI training docs.

### UX
- Per-tenant region selector during onboarding.
- Customer-facing status page (statuspage.io).
- Incident communication runbook.

### Reliability
- Synthetic probes every 60 s per region per critical path.
- Chaos engineering (Chaos Mesh / LitmusChaos) gameday quarterly.
- Auto-scaling validated under sustained load.

### Observability
- Cross-region trace correlation.
- Per-region SLO dashboards.
- Cost dashboards aligned with SLO.

### Maintainability
- ADRs: K8s primary cloud, mesh choice, multi-region topology.
- Quarterly architecture review.
- Versioning of Helm charts pinned + tested.

---

## 9. Technical Considerations

### 9.1 DB Design
- Logical replication setup in Aurora Global Database.
- Per-tenant `home_region` and `data_residency` columns.
- Cross-region read-replicas for read scaling.

### 9.2 API Contracts
- Tenant routing header `X-Janus-Region` honored end-to-end.
- Region-aware OpenAPI examples.

### 9.3 RBAC
- Cluster-level RBAC via OIDC-federated K8s users.
- Per-namespace RBAC restricting service identities.

### 9.4 Validation Flows
- Helm chart linting; conftest policies (OPA) enforce required labels, network policies, PDBs.
- Cross-region replication health checks gate failover.

### 9.5 Caching
- Per-region cache; warm-up scripts on failover.

### 9.6 Queues & Background Jobs
- Schedule consistency: jobs run in primary region only; secondary picks up post-failover.

### 9.7 Audit Logs
- Cross-region WORM replication.
- Per-region ClickHouse + nightly rollup to global view.

### 9.8 Retry & Idempotency
- All ops idempotent; cross-region replays safe.

### 9.9 Monitoring
- SLO + error budget per service per region.
- Replication lag dashboards.
- Failover-readiness scorecard published weekly.

### 9.10 CI/CD
- ArgoCD pull-based deploys; PR opens deploy.
- Progressive delivery: canary → 10% → 50% → 100% per region.
- Automated rollback on SLO breach.
- DR drill in CI for `staging` quarterly.

---

## 10. Risks, Rollback & Future Extensibility

### Risks
| Risk                                                              | Likelihood | Impact   | Mitigation                                                                                       |
| ----------------------------------------------------------------- | ---------- | -------- | ------------------------------------------------------------------------------------------------ |
| DR drill not executed → DR doesn't actually work                  | High       | Critical | Mandatory quarterly drill; calendar invite owned by SRE Lead.                                    |
| Cost explosion under autoscaling bug                              | Med        | High     | Per-cluster cost cap; alarms.                                                                    |
| K8s misconfiguration leaks tenant data                            | Med        | Critical | Network policies tested; OPA gates.                                                              |
| Replication lag silently grows                                    | Med        | High     | Replication SLO + alert.                                                                          |
| Failover flap during partial outage                               | Med        | High     | Manual approval for failover unless severe; staged rollout.                                       |
| Vault unseal failure                                              | Low        | Critical | Multi-AZ + KMS auto-unseal + runbook.                                                            |
| Service mesh adds latency / instability                           | Med        | Med      | Bake in `staging`; canary per workload.                                                          |
| Cross-region egress costs                                         | Med        | Med      | Architect to minimize cross-region; monitor.                                                     |

### Rollback
- Per-service rollback via ArgoCD revision.
- Per-region drain + reroute via routing layer.
- Failover reversible: re-promote original primary post-recovery.

### Future Extensibility
- Multi-cloud (active-passive across providers).
- Per-tenant dedicated clusters.
- Edge points-of-presence for low-latency proxy access.
- GPU pools for V2 ML.
- Confidential computing nodes for `restricted` data tenants.

---

## 11. Deliverables & Acceptance Criteria

### Deliverables
- [ ] All services on K8s in primary region.
- [ ] Linkerd mTLS across the mesh.
- [ ] Stateful services on managed offerings.
- [ ] Second region with active-active reads + single-writer failover.
- [ ] Cross-region replication for PG, Kafka, WORM, Flink state.
- [ ] Helm charts + ArgoCD GitOps live.
- [ ] DR runbooks per scenario.
- [ ] Quarterly DR drill executed in `staging`.
- [ ] Load test at 5× peak.
- [ ] Cost controls live + dashboards.

### Acceptance Criteria
- [ ] System handles 5× peak with p99 budgets met.
- [ ] Primary → secondary failover in < 30 min (drill).
- [ ] mTLS verified end-to-end between internal services.
- [ ] All stateful services on managed offerings.
- [ ] Per-tenant data residency enforced.
- [ ] Cost dashboards drive operational decisions.

---

## 12. Production Readiness Checklist

- [ ] On-call rotations established per region.
- [ ] Synthetic probes per region.
- [ ] SLO + error budgets defined per service.
- [ ] WAF + DDoS protection live.
- [ ] Pen-test scope includes K8s, mesh, multi-region.
- [ ] Quarterly drill cadence calendared.
- [ ] Customer-facing status page + comms templates.

---

## 13. Remaining Risks Carried Forward

- **Active-active writes** for control plane deferred indefinitely.
- **Multi-cloud** deferred.
- **Per-tenant dedicated clusters** Enterprise-tier only.
- **GPU pools** capacity reserved but not provisioned.
- **Edge PoPs** for low-latency proxy access deferred.
- **Compliance hardening** (Phase 16) is the final mile before GA.
