import { z } from 'zod';

// ─── Policy JSON schema (canonical output of Drafter) ────────────────────────

export const ConditionSchema: z.ZodType<unknown> = z.lazy(() =>
  z.union([
    z.object({
      type: z.literal('comparison'),
      field: z.string().min(1),
      op: z.enum(['eq', 'neq', 'lt', 'lte', 'gt', 'gte', 'in', 'not_in', 'matches_re2']),
      value: z.union([z.string(), z.number(), z.boolean(), z.array(z.string())]),
    }),
    z.object({
      type: z.enum(['and', 'or']),
      children: z.array(ConditionSchema).min(1).max(64),
    }),
    z.object({
      type: z.literal('not'),
      child: ConditionSchema,
    }),
  ])
);

export const ColumnMaskSchema = z.object({
  column: z.string().min(1),
  mask: z.enum(['redact', 'partial', 'hash', 'tokenize']),
  format: z.string().optional(),
});

export const PolicyRuleSchema = z.object({
  id: z.string().optional(),
  effect: z.enum(['allow', 'deny']),
  actions: z.array(z.enum(['select', 'insert', 'update', 'delete', 'explain'])).min(1),
  resource: z.object({
    schema: z.string().min(1),
    table: z.string().min(1),
    columns: z.array(z.string()).optional(),
  }),
  conditions: ConditionSchema.optional(),
  row_filter: z.string().optional(),     // SQL predicate (parameterized)
  column_masks: z.array(ColumnMaskSchema).optional(),
  obligations: z.array(z.string()).optional(),
  priority: z.number().int().min(0).max(1000).default(100),
});

export const PolicyDraftSchema = z.object({
  name: z.string().min(1).max(255),
  description: z.string().max(2048).optional(),
  version: z.number().int().min(1).default(1),
  tenant_id: z.string().uuid(),
  subject: z.object({
    roles: z.array(z.string()).optional(),
    user_ids: z.array(z.string().uuid()).optional(),
    attributes: z.record(z.union([z.string(), z.number(), z.boolean()])).optional(),
  }),
  rules: z.array(PolicyRuleSchema).min(1).max(50),
});

export type PolicyDraft = z.infer<typeof PolicyDraftSchema>;

// ─── Graph state ──────────────────────────────────────────────────────────────

export const GraphStateSchema = z.object({
  // request context
  session_id: z.string().uuid(),
  tenant_id: z.string().uuid(),
  user_id: z.string().uuid(),
  idempotency_key: z.string().min(1),
  prompt: z.string().min(1).max(4096),
  prompt_hash: z.string(),

  // node outputs
  sanitized_prompt: z.string().optional(),
  intent: z.enum(['role.create', 'policy.update', 'grant', 'revoke']).optional(),
  canonical_map: z.record(z.object({
    canonical: z.string(),
    confidence: z.number().min(0).max(1),
    schema: z.string(),
    table: z.string(),
  })).optional(),
  draft_policy: PolicyDraftSchema.optional(),
  validation_errors: z.array(z.string()).optional(),
  simulator_diff: z.unknown().optional(),
  explanation: z.string().optional(),
  approval_state: z.enum(['pending', 'approved', 'rejected']).optional(),
  compiled_policy_id: z.string().uuid().optional(),

  // telemetry
  total_tokens_in: z.number().default(0),
  total_tokens_out: z.number().default(0),
  total_cost_usd: z.number().default(0),
  node_spans: z.array(z.object({
    node: z.string(),
    provider: z.string().optional(),
    model: z.string().optional(),
    tokens_in: z.number().optional(),
    tokens_out: z.number().optional(),
    cost_usd: z.number().optional(),
    latency_ms: z.number(),
    error: z.string().optional(),
  })).default([]),

  // error handling
  error: z.string().optional(),
  abort_reason: z.string().optional(),
});

export type GraphState = z.infer<typeof GraphStateSchema>;

// ─── HTTP request/response ────────────────────────────────────────────────────

export const DraftRequestSchema = z.object({
  prompt: z.string().min(1).max(4096),
  idempotency_key: z.string().min(1).max(128),
});

export const ExplainRequestSchema = z.object({
  policy_id: z.string().uuid(),
});

export const ApproveRequestSchema = z.object({
  session_id: z.string().uuid(),
  action: z.enum(['approve', 'reject']),
  reason: z.string().max(1024).optional(),
  mfa_token: z.string().min(6).max(8),
});
