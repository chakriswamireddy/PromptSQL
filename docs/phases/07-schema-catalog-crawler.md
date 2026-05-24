# Phase 7 — Schema Catalog & Metadata Crawler

> **Duration:** 14–16 weeks (≈2–3 weeks of focused work) &nbsp; · &nbsp; **Owner:** Backend &nbsp; · &nbsp; **Dependencies:** Phases 0–6
> **Companion:** [`../implementation-plan.md` §Phase 7](../implementation-plan.md)

---

## 1. Phase Objective & Business Purpose

Every connected database is automatically discovered, classified, and embedded so the platform — and the AI orchestrator (Phases 8–10) — knows every table, column, type, FK, and *sensitivity classification* before any query is generated. New columns enter **quarantine**, invisible to AI retrieval until a steward classifies them.

**Business rationale:** AI hallucinations almost always trace back to schema ignorance. A continuously-refreshed, classification-aware catalog is the difference between an AI that confidently invents columns and an AI that operates on facts. Quarantine for unclassified columns is a hard safety property that prevents accidental exposure of new PII.

---

## 2. Scope Boundaries & Ownership

**In scope**
- `apps/schema-crawler` (Go) service.
- Read-only connectors for PostgreSQL (Phase 11 expands).
- Sampling of distinct values for low-classification columns only.
- Diff detection across runs (new / renamed / dropped / type-changed).
- Quarantine semantics + steward notification.
- Embedding generation for classified columns (OpenAI `text-embedding-3-large` or local model).
- Classification UI in admin console with bulk-pattern actions.

**Out of scope**
- Multi-DB connectors (Phase 11 expands).
- Document corpus ingestion (Phase 8 / RAG).
- Per-row sensitivity scoring (deferred).
- Auto-classification via LLM (reserved; manual + pattern rules in V1).

**Ownership**
- **Drives:** Backend engineer.
- **Reviews:** Security (sampling boundaries), Data steward (UX), AI engineer (embedding strategy).

---

## 3. Hard Dependencies & Sequencing

- Phase 6: PG connector + Vault-stored credentials.
- Phase 1: `schema_metadata`, `data_classifications` tables with `pgvector`.
- Phase 5: audit pipeline (crawler events emit).

Sequencing: crawler service skeleton → discovery → sampling → diff → quarantine → embedding → admin UI → bulk classification → tests.

---

## 4. Detailed Sub-Phases & Implementation Tasks

### 7.1 — Crawler Service

`apps/schema-crawler` per `data_source`:
1. Acquire read-only connection via Vault-issued credentials.
2. Query `information_schema.tables`, `information_schema.columns`, `pg_constraint` (FKs), `pg_index` (indices), `pg_description` (comments).
3. For each column: capture name, type, nullable, default, position, FK references, comment.
4. If `data_classifications` for column has `classification ∈ {public, internal}` OR column is unclassified → sample up to 10 distinct values via `SELECT DISTINCT … LIMIT 10`; gate sample by table size + per-column data-volume cap.
5. Upsert into `schema_metadata` with composite uniqueness `(tenant_id, data_source_id, schema_name, table_name, column_name)`.
6. Mark new columns `quarantine=true`.
7. Diff against last crawl; emit `schema.drift` events (Phase 5 pipeline).

Schedule: every 6 hours per data source, plus admin-triggered "refresh now."

### 7.2 — Classification UI (Admin Console)

Page: **Classifications**.
- Top: counts (unclassified, quarantined, classified by category).
- Filter: data source, schema, table, column-name regex, classification.
- Row actions: classify (single), bulk-classify by pattern, override.
- **Suggestion engine:**
  - Column-name patterns: `%ssn%`→`restricted+pii`, `%email%`→`confidential+contact`, `%phone%`→`confidential+contact`, `%credit%card%`→`restricted+pci`, `%dob%|%birth%`→`confidential+pii`.
  - Data-type heuristics: `numeric(19,4)` + name `%amount%` → `confidential+financial`.
- Bulk-apply UX: preview affected columns, dry-run sample, confirm.

### 7.3 — Embeddings

For each classified column, compose:
```
{schema}.{table}.{column} ({type}) [{tags}]: {description or table_comment + column_comment}
```
Embed via `text-embedding-3-large` (3072 dims) or `text-embedding-3-small` (1536 dims).

V1 default: **`text-embedding-3-small`** (cheaper, sufficient for retrieval). High-tier tenants opt into 3072 dims.

- Batch in 100s to control cost.
- Re-embed on description change, classification change, or table comment update.
- Store in `schema_metadata.embedding`.
- `ivfflat` index for cosine similarity (`vector_cosine_ops`).

### 7.4 — Quarantine Semantics

- New rows in `schema_metadata` with `quarantine=true`.
- Phase 8 retrieval filters out quarantined rows; AI never "sees" them.
- Steward notification (Slack/email via webhook fanout once Phase 12 lands; in V1, an in-app notification badge).
- Auto-promote rule: a tenant can opt-in to "auto-promote columns matching a strict pattern" (e.g., `%_id` UUID columns → `internal` after 24 h). Audit each auto-promotion.

### 7.5 — Drift Handling

| Change            | Action                                                                                                      |
| ----------------- | ----------------------------------------------------------------------------------------------------------- |
| **New column**    | Quarantine + steward alert.                                                                                 |
| **Renamed**       | Heuristic match (type + position + sample similarity); flag policies referencing old name; require confirm. |
| **Dropped**       | Mark `last_seen_at`; orphan policies surfaced for steward review; quarantine until re-confirmed.            |
| **Type changed**  | Flag; many drift scenarios are benign deploys, but masks and predicates may break.                          |
| **Reordered**     | Position drift is informational only.                                                                       |
| **Comment update**| Re-embed.                                                                                                   |

### 7.6 — Sampling Safety

- Hard rule: never sample columns classified `confidential` or `restricted`.
- Default: 10 distinct values; configurable down to 0 per data source for paranoid tenants.
- Sample stored in `schema_metadata.sample_values text[]`; visible only to users with `classification.read.{level}` permissions.
- For Phase 8 retrieval, sample values flow into prompts only at `classification ≤ internal`.

### 7.7 — Performance & Scheduling

- Per-data-source crawl in parallel with a global concurrency limit.
- Long crawls (large schemas) yield checkpoints to disk; resumable.
- Backoff if backend reports high load; never crawl during a tenant's defined "blackout window."

### 7.8 — Observability

- `crawler_run_total{result}` counter.
- `crawler_columns_discovered_total{kind=new|changed|dropped}`.
- `crawler_run_duration_seconds`.
- `embedding_cost_usd_total{tenant}` for budget tracking.

---

## 5. Architectural Gaps & Missing Requirements

1. **Auto-classification ML.** Pattern matching covers ~70% of fields; a future ML classifier (Phase 13+) catches the long tail. Reserve `data_classifications.classified_by ∈ {pattern, steward, ml}`.
2. **Cross-table relationships.** FK graph stored; do we also infer relationships from queries seen? Reserve `inferred_relationships` table.
3. **PII detection on sample values.** Run a regex / Presidio scan on sampled values to *also* suggest classification beyond name patterns.
4. **Data lineage.** Inferring lineage from query patterns is a Phase 13+ concern; design `lineage_edges` schema.
5. **Per-row sensitivity.** Some tables mix sensitivity per row (e.g., `internal` + `restricted`); reserve column `row_sensitivity_attr` referencing a determining column.
6. **Schema fingerprinting.** A whole-table fingerprint enables "did anything change?" fast-paths.
7. **Backwards compatibility for policies.** When a column is renamed, policies referencing the old name need migration tooling.
8. **Embeddings provider switch.** Locking into one provider is risky; design an abstraction (`EmbeddingProvider` interface) so swap to local model is one-line config change.
9. **GDPR consideration on sample values.** Even `internal` samples should not include PII; safety net via Presidio scan before storage.

---

## 6. Edge Cases & Failure Modes

| Scenario                                                          | Mitigation                                                                                       |
| ----------------------------------------------------------------- | ------------------------------------------------------------------------------------------------ |
| Tenant with 50k columns                                           | Bulk classification by pattern; suggestion accept-all confirmation.                              |
| Backend DB temporarily unreachable                                | Backoff with jitter; alert after 3 consecutive failures.                                          |
| Sampling exposes PII to non-cleared steward                       | Classification fence: pre-classification samples shown only to `classification.full` permission. |
| Renamed column with identical type & sample distribution          | Auto-bind suggestion; steward confirms; orphan policy auto-rewired.                              |
| Embedding API outage                                              | Queue work; retry with backoff; alarm if queue > 1 h.                                             |
| Cost spike from embedding 1M new columns                          | Per-tenant daily budget cap; pause re-embed; surface to admin.                                    |
| Vendor catalog limits (`information_schema` truncation in some DB)| Document per-DB caveats (Phase 11 expands).                                                       |
| Schema discovery races with active query                          | Read-only connection; uses repeatable-read isolation.                                             |
| Quarantine bypass via direct SQL by admin                         | Native RLS mirror (Phase 6) enforces; quarantined column is in deny list.                        |
| pgvector index degraded                                           | Quarterly REINDEX scheduled; alarm on slow retrieval.                                             |

---

## 7. Non-Functional Concerns

### 7.1 Scalability
- Crawl 100k columns per tenant in < 5 min; embeddings async.
- Per-tenant queue priority for re-embed.
- Vector index rebuild scheduled during low-traffic windows.

### 7.2 Security
- Read-only credentials in Vault, per-data-source.
- Sampling boundaries enforced server-side; UI cannot bypass.
- Embedding payloads never include `confidential`/`restricted` content.
- Embeddings of sensitive content (descriptions or comments containing PII) gated by Presidio scan pre-embed.

### 7.3 Multi-Tenant Isolation
- `tenant_id` first column of every index.
- Per-tenant vector namespace (table prefix or Qdrant collection if external vector store).
- Steward UI scoped per tenant; super-admin sees aggregated view only.

### 7.4 Concurrency
- Per-data-source advisory lock prevents overlapping crawls.
- Embedding worker pool with backpressure on provider.

### 7.5 Performance
- Crawl runtime < 5 min for 10k columns.
- Vector similarity p95 < 50 ms.
- Bulk classify endpoint < 2 s for 1k columns.

---

## 8. Recommended Improvements

### Architecture
- Abstract `Crawler` interface so Phase 11 DBs slot in without refactoring.
- Abstract `EmbeddingProvider` so swapping OpenAI → Bedrock → local model is config-only.
- Treat `schema_metadata` as a *materialized projection*; the crawler is its sole writer.

### DX
- `crawler-cli inspect --data-source=…` for local debugging.
- A `make refresh-catalog` shortcut runs a crawl in dev.
- Storybook screens for the classification UI.

### UX
- Inline "test in simulator" for any classification decision.
- Heatmap view of classifications by table.
- "Stewardship inbox": all pending classifications + drift in one place; supports keyboard-driven triage.

### Reliability
- Resume from checkpoint on crawl crash.
- Embedding provider failover.
- Quarterly catalog audit: random-sample a tenant's classifications and re-prompt the steward to confirm.

### Observability
- Coverage metric: `% of columns classified per tenant`; surfaced in dashboards.
- Cost dashboards per tenant for embeddings.
- Per-pattern suggestion accuracy tracked over time.

### Maintainability
- ADR: provider choice, pattern rules ownership, sampling defaults.
- Pattern rules live in a tenant-overridable JSON file in the repo.

---

## 9. Technical Considerations

### 9.1 DB Design
- Extend `schema_metadata`: `last_seen_at`, `sample_values text[]`, `embedding_model`, `embedding_dimensions`, `quarantine`, `classified_by`.
- `inferred_relationships(id, tenant_id, from_table, from_column, to_table, to_column, confidence, source)`.
- `data_classifications` includes `pattern_id` when bulk-classified.

### 9.2 API Contracts
- `/api/v1/catalog/columns` (list/filter), `/api/v1/catalog/classify` (single/bulk), `/api/v1/catalog/crawl` (trigger).
- All write endpoints idempotent + audited.

### 9.3 RBAC
- `catalog.read`, `catalog.classify`, `catalog.bulk_classify`, `catalog.crawl_trigger`, `catalog.sample.view` (gated by classification level).

### 9.4 Validation Flows
- Classification must reference a column present in current `schema_metadata`.
- Pattern bulk-apply has a preview/confirm with row-count guardrail (`< 10000` per single apply by default).

### 9.5 Caching
- Suggestion engine evaluation cached per-tenant per crawl run.
- Classification reads cached in PDP per Phase 3 mechanism.

### 9.6 Queues & Background Jobs
- Crawl scheduler (Redis stream or cron).
- Embedding worker pool.
- Quarantine notifier.
- Reindex (vector) quarterly cron.

### 9.7 Audit Logs
- Crawler runs (`audit.system`).
- Classification mutations (`audit.policy`).
- Auto-promotion events (`audit.policy`).

### 9.8 Retry & Idempotency
- Idempotency keys on all classification writes.
- Embeddings dedup by `sha256(payload + model + dims)`.

### 9.9 Monitoring
Alerts: crawler failure > 2 consecutive runs; embedding cost spike; quarantine count > N for > 24 h.

### 9.10 CI/CD
- Synthetic schema generator + crawler integration test.
- Embedding provider mock in CI; real provider in `staging` only.

---

## 10. Risks, Rollback & Future Extensibility

### Risks
| Risk                                                              | Likelihood | Impact   | Mitigation                                                                                       |
| ----------------------------------------------------------------- | ---------- | -------- | ------------------------------------------------------------------------------------------------ |
| Classification fatigue (steward never finishes)                   | High       | Med      | Pattern matcher + suggested defaults + bulk-apply.                                               |
| Sample values leak PII because of mis-classification              | Med        | Critical | Pre-embed Presidio scan; default classification high (`confidential`) until proven otherwise.    |
| Crawler load spikes on backend                                    | Med        | Med      | Backoff + per-tenant blackout windows.                                                            |
| Embedding cost runaway                                            | Med        | Med      | Per-tenant budget cap; alarm.                                                                     |
| Schema rename misattributed                                       | Med        | Med      | Steward confirmation required; policy auto-rewire opt-in.                                         |
| Pattern rules drift across tenants                                | Med        | Med      | Tenant-overridable, but a maintained default ruleset versioned in repo.                          |
| Vector index degradation as data grows                            | Med        | Med      | Quarterly REINDEX + monitoring.                                                                  |

### Rollback
- Feature flag per tenant for the entire catalog pipeline.
- Embedding model swap reversible (column stores model name).
- Pattern rules versioned; rollback restores prior version.

### Future Extensibility
- Phase 11 connectors plug in via `Crawler` interface.
- ML classifier deferred; schema reserves `classified_by='ml'` slot.
- Lineage edges populate later via query introspection.
- Per-row sensitivity attributes lay foundation for ABAC over row values.

---

## 11. Deliverables & Acceptance Criteria

### Deliverables
- [ ] `apps/schema-crawler` deployed to `dev`/`staging`.
- [ ] PG connector with read-only Vault credentials.
- [ ] Sampling with safety boundaries.
- [ ] Diff detection + quarantine.
- [ ] Embedding pipeline with provider abstraction.
- [ ] Admin UI: classification page + bulk-apply.
- [ ] Drift handler.
- [ ] Audit + metrics + dashboards.

### Acceptance Criteria
- [ ] All connected PG instances crawled every 6 h.
- [ ] New column appears in quarantine within 6 h + alert.
- [ ] Bulk pattern classification reduces a 1k-column tenant to < 50 manual triage cases.
- [ ] Vector similarity returns sensible results on a held-out test set.
- [ ] No PII appears in any `sample_values` row at `confidential`/`restricted`.

---

## 12. Production Readiness Checklist

- [ ] Per-tenant blackout windows configurable.
- [ ] Embedding budget alerts.
- [ ] Reindex cron scheduled.
- [ ] DR runbook: crawler outage, embedding outage, vector index loss.
- [ ] Steward UX accessibility verified.
- [ ] Pattern rules versioned + reviewed quarterly.

---

## 13. Remaining Risks Carried Forward

- **Multi-DB connectors** absent until Phase 11.
- **Auto-classifier ML** not yet built; long-tail manual.
- **Lineage** unimplemented.
- **Per-row sensitivity** schema reserved but not enforced.
- **PII detector** is regex / Presidio in V1; LLM-based detector is a future improvement.
- **Embedding inversion risk** for `restricted` content addressed by *not embedding* it; if customer requires retrieval, a tenant-private vector store is required (Phase 8).
