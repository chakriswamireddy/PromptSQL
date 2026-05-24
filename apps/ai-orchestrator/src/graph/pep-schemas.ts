import { z } from 'zod';

// ─── Calcite-compatible SQL AST ───────────────────────────────────────────────
// The SQL Drafter emits this structured AST instead of raw SQL strings.
// Advantages: grammar-constrained, validator works on AST nodes directly,
// renders to any SQL dialect via Calcite SqlDialect.

export const LiteralNodeSchema = z.object({
  kind: z.literal('Literal'),
  value: z.union([z.string(), z.number(), z.boolean(), z.null()]),
  type: z.enum(['string', 'number', 'boolean', 'null', 'date', 'timestamp']).default('string'),
});

export const ColumnNodeSchema = z.object({
  kind: z.literal('Column'),
  table: z.string().min(1).max(128),
  column: z.string().min(1).max(128),
});

export const TableNodeSchema = z.object({
  kind: z.literal('Table'),
  schema: z.string().default('public'),
  name: z.string().min(1).max(128),
  alias: z.string().max(64).optional(),
});

export const FunctionCallNodeSchema = z.object({
  kind: z.literal('Function'),
  name: z.string().min(1).max(64),   // validated against allowlist in ast-validator
  args: z.array(z.lazy(() => ExprNodeSchema)),
});

export const CmpNodeSchema = z.object({
  kind: z.literal('Cmp'),
  op: z.enum(['=', '!=', '<', '<=', '>', '>=', 'LIKE', 'NOT LIKE', 'IN', 'NOT IN', 'IS NULL', 'IS NOT NULL']),
  lhs: z.lazy(() => ExprNodeSchema),
  rhs: z.lazy(() => ExprNodeSchema).optional(),  // optional for IS NULL / IS NOT NULL
});

export const AndNodeSchema = z.object({
  kind: z.literal('And'),
  args: z.array(z.lazy(() => ExprNodeSchema)).min(2).max(32),
});

export const OrNodeSchema = z.object({
  kind: z.literal('Or'),
  args: z.array(z.lazy(() => ExprNodeSchema)).min(2).max(32),
});

export const NotNodeSchema = z.object({
  kind: z.literal('Not'),
  arg: z.lazy(() => ExprNodeSchema),
});

export const BetweenNodeSchema = z.object({
  kind: z.literal('Between'),
  value: z.lazy(() => ExprNodeSchema),
  low: z.lazy(() => ExprNodeSchema),
  high: z.lazy(() => ExprNodeSchema),
});

// Discriminated union of all expression node types
export const ExprNodeSchema: z.ZodType<ExprNode> = z.lazy(() =>
  z.discriminatedUnion('kind', [
    LiteralNodeSchema,
    ColumnNodeSchema,
    FunctionCallNodeSchema,
    CmpNodeSchema,
    AndNodeSchema,
    OrNodeSchema,
    NotNodeSchema,
    BetweenNodeSchema,
  ])
);

export type ExprNode =
  | z.infer<typeof LiteralNodeSchema>
  | z.infer<typeof ColumnNodeSchema>
  | z.infer<typeof FunctionCallNodeSchema>
  | z.infer<typeof CmpNodeSchema>
  | z.infer<typeof AndNodeSchema>
  | z.infer<typeof OrNodeSchema>
  | z.infer<typeof NotNodeSchema>
  | z.infer<typeof BetweenNodeSchema>;

export const SelectItemSchema = z.union([
  z.object({ kind: z.literal('Column'), table: z.string(), column: z.string(), alias: z.string().optional() }),
  z.object({ kind: z.literal('Function'), name: z.string(), args: z.array(ExprNodeSchema), alias: z.string().optional() }),
  z.object({ kind: z.literal('Star'), table: z.string().optional() }),
]);

export const JoinNodeSchema = z.object({
  kind: z.literal('Join'),
  join_type: z.enum(['INNER', 'LEFT', 'RIGHT']).default('INNER'),
  table: TableNodeSchema,
  on: ExprNodeSchema,  // validated to be an FK-declared predicate
});

export const OrderByItemSchema = z.object({
  expr: ExprNodeSchema,
  dir: z.enum(['ASC', 'DESC']).default('ASC'),
  nulls: z.enum(['FIRST', 'LAST']).optional(),
});

// Top-level SELECT AST — the only statement type permitted in V1
export const SqlAstSchema = z.object({
  kind: z.literal('Select'),
  distinct: z.boolean().default(false),
  select: z.array(SelectItemSchema).min(1).max(64),
  from: TableNodeSchema,
  joins: z.array(JoinNodeSchema).max(6).optional(),
  where: ExprNodeSchema.optional(),
  group_by: z.array(ExprNodeSchema).max(8).optional(),
  having: ExprNodeSchema.optional(),
  order_by: z.array(OrderByItemSchema).max(8).optional(),
  limit: z.number().int().min(1).max(100_000),
  offset: z.number().int().min(0).optional(),
});

export type SqlAst = z.infer<typeof SqlAstSchema>;

// ─── Allowed Schema Snapshot ──────────────────────────────────────────────────
// Returned by the retrieval-service /snapshot endpoint.
// Defines the exact tables/columns the user may query.

export const SnapshotColumnSchema = z.object({
  name: z.string(),
  type: z.string(),
  nullable: z.boolean().optional(),
  masked: z.enum(['redact', 'partial', 'hash', 'tokenize']).optional(),
  classification: z.string().optional(),
  description: z.string().optional(),
  sample_values: z.array(z.string()).optional(),
});

export const SnapshotFKSchema = z.object({
  column: z.string(),
  ref_table: z.string(),
  ref_column: z.string(),
});

export const SnapshotTableSchema = z.object({
  name: z.string(),
  schema: z.string().default('public'),
  description: z.string().optional(),
  columns: z.array(SnapshotColumnSchema),
  foreign_keys: z.array(SnapshotFKSchema).optional(),
  row_filter_summary: z.string().optional(),
});

export const AllowedSnapshotSchema = z.object({
  version: z.string(),
  schema_version: z.string().optional(),
  policy_set_version: z.string().optional(),
  data_source_id: z.string().uuid(),
  tables: z.array(SnapshotTableSchema),
  truncated: z.boolean().optional(),
});

export type AllowedSnapshot = z.infer<typeof AllowedSnapshotSchema>;

// ─── Retriever output ─────────────────────────────────────────────────────────

export const RelevantTableSchema = z.object({
  table: SnapshotTableSchema,
  relevance_score: z.number(),
  reason: z.string().optional(),
});

// ─── Validation error ─────────────────────────────────────────────────────────

export const ValidationErrorSchema = z.object({
  code: z.string(),
  message: z.string(),
  hint: z.string().optional(),   // user-visible suggestion
  node_path: z.string().optional(), // e.g. "joins[0].table"
});

export type ValidationError = z.infer<typeof ValidationErrorSchema>;

// ─── EXPLAIN result ───────────────────────────────────────────────────────────

export const ExplainResultSchema = z.object({
  total_cost: z.number(),
  plan_rows: z.number(),
  plan_json: z.unknown().optional(),
  rejected: z.boolean().default(false),
  rejection_reason: z.string().optional(),
});

// ─── Formatted result (output of result-formatter node) ───────────────────────

export const ResultColumnSchema = z.object({
  name: z.string(),
  type: z.string().optional(),
  masked: z.boolean().default(false),
});

export const FormattedResultSchema = z.object({
  columns: z.array(ResultColumnSchema),
  rows: z.array(z.record(z.unknown())),
  total_rows: z.number(),
  truncated: z.boolean().default(false),
  citation: z.object({
    snapshot_version: z.string(),
    policy_set_version: z.string().optional(),
    tables_accessed: z.array(z.string()),
    provider: z.string().optional(),
    model: z.string().optional(),
  }),
});

export type FormattedResult = z.infer<typeof FormattedResultSchema>;

// ─── PEP graph state ──────────────────────────────────────────────────────────

export const PepGraphStateSchema = z.object({
  // Request context
  session_id: z.string().uuid(),
  tenant_id: z.string().uuid(),
  user_id: z.string().uuid(),
  data_source_id: z.string().uuid(),
  idempotency_key: z.string().min(1),
  prompt: z.string().min(1).max(8192),
  prompt_hash: z.string(),

  // Node outputs
  sanitized_prompt: z.string().optional(),
  allowed_snapshot: AllowedSnapshotSchema.optional(),
  relevant_tables: z.array(RelevantTableSchema).optional(),
  ast: SqlAstSchema.optional(),
  validated_sql: z.string().optional(),  // AST rendered to SQL string after validation
  explain_result: ExplainResultSchema.optional(),
  result: FormattedResultSchema.optional(),

  // Validation tracking
  validation_errors: z.array(ValidationErrorSchema).optional(),
  retry_count: z.number().int().min(0).default(0),

  // Telemetry
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

  // Error handling
  error: z.string().optional(),
  abort_reason: z.string().optional(),
});

export type PepGraphState = z.infer<typeof PepGraphStateSchema>;

// ─── HTTP request/response ────────────────────────────────────────────────────

export const PepAskRequestSchema = z.object({
  prompt: z.string().min(1).max(8192),
  data_source_id: z.string().uuid(),
  idempotency_key: z.string().min(1).max(128),
});

export const PepFeedbackRequestSchema = z.object({
  session_id: z.string().uuid(),
  thumbs_up: z.boolean(),
  comment: z.string().max(1024).optional(),
});

export const SaveQuestionRequestSchema = z.object({
  session_id: z.string().uuid(),
  name: z.string().min(1).max(255),
  description: z.string().max(2048).optional(),
});

export const RunSavedQuestionRequestSchema = z.object({
  saved_question_id: z.string().uuid(),
});
