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
