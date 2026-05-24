import { Counter, Histogram, Registry } from 'prom-client';

export const registry = new Registry();

export const graphRunsTotal = new Counter({
  name: 'pap_graph_runs_total',
  help: 'Total PAP graph run attempts',
  labelNames: ['tenant_id', 'outcome'] as const,
  registers: [registry],
});

export const graphDurationSeconds = new Histogram({
  name: 'pap_graph_duration_seconds',
  help: 'End-to-end PAP graph wall-clock time',
  labelNames: ['tenant_id', 'outcome'] as const,
  buckets: [0.5, 1, 2, 5, 10, 15, 20, 30],
  registers: [registry],
});

export const llmCallsTotal = new Counter({
  name: 'pap_llm_calls_total',
  help: 'Total LLM calls per node/provider/model',
  labelNames: ['node', 'provider', 'model', 'outcome'] as const,
  registers: [registry],
});

export const llmTokensTotal = new Counter({
  name: 'pap_llm_tokens_total',
  help: 'Total LLM tokens consumed',
  labelNames: ['node', 'provider', 'model', 'direction'] as const,
  registers: [registry],
});

export const llmCostUsd = new Counter({
  name: 'pap_llm_cost_usd_total',
  help: 'Cumulative LLM cost in USD',
  labelNames: ['tenant_id', 'provider', 'model'] as const,
  registers: [registry],
});

export const validationFailuresTotal = new Counter({
  name: 'pap_validation_failures_total',
  help: 'Policy validation failures by reason',
  labelNames: ['reason'] as const,
  registers: [registry],
});

export const injectionRefusalsTotal = new Counter({
  name: 'pap_injection_refusals_total',
  help: 'Prompt injection attempts detected and refused',
  labelNames: ['tenant_id'] as const,
  registers: [registry],
});

export const tokenBudgetThrottlesTotal = new Counter({
  name: 'pap_token_budget_throttles_total',
  help: 'Requests throttled by token budget',
  labelNames: ['tenant_id', 'period'] as const,
  registers: [registry],
});

export const approvalActionsTotal = new Counter({
  name: 'pap_approval_actions_total',
  help: 'Human approval / rejection actions',
  labelNames: ['tenant_id', 'action'] as const,
  registers: [registry],
});

// ─── PEP graph metrics ────────────────────────────────────────────────────────

export const pepGraphRunsTotal = new Counter({
  name: 'pep_graph_runs_total',
  help: 'Total PEP graph run attempts',
  labelNames: ['tenant_id', 'outcome'] as const,
  registers: [registry],
});

export const pepGraphDurationSeconds = new Histogram({
  name: 'pep_graph_duration_seconds',
  help: 'End-to-end PEP graph wall-clock time',
  labelNames: ['tenant_id', 'outcome'] as const,
  buckets: [0.2, 0.5, 1, 2, 3, 5, 10, 20, 30],
  registers: [registry],
});

export const pepAstValidationFailuresTotal = new Counter({
  name: 'pep_ast_validation_failures_total',
  help: 'AST validation rejections by reason',
  labelNames: ['reason'] as const,
  registers: [registry],
});

export const pepCostGateRejectionsTotal = new Counter({
  name: 'pep_cost_gate_rejections_total',
  help: 'Queries rejected by cost gate',
  labelNames: ['tenant_id'] as const,
  registers: [registry],
});

export const pepRowsStreamedTotal = new Counter({
  name: 'pep_rows_streamed_total',
  help: 'Total result rows streamed to users',
  labelNames: ['tenant_id'] as const,
  registers: [registry],
});

export const pepFeedbackTotal = new Counter({
  name: 'pep_feedback_total',
  help: 'User thumbs-up / thumbs-down feedback',
  labelNames: ['tenant_id', 'sentiment'] as const,
  registers: [registry],
});

export const pepDrafterRetriesTotal = new Counter({
  name: 'pep_drafter_retries_total',
  help: 'SQL drafter retry attempts after validator rejection',
  labelNames: ['tenant_id'] as const,
  registers: [registry],
});
