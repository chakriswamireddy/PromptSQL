import { z } from "zod";
import { zodToJsonSchema } from "zod-to-json-schema";

// ── DSL Condition ─────────────────────────────────────────────────────────────

const LeafOp = z.enum([
  "eq", "ne", "lt", "lte", "gt", "gte",
  "in", "not_in", "contains", "starts_with", "ends_with",
  "is_null", "is_not_null", "regex",
]);

type ConditionNode =
  | { field: string; op: z.infer<typeof LeafOp>; value?: unknown }
  | { all: ConditionNode[] }
  | { any: ConditionNode[] }
  | { not: ConditionNode };

const ConditionSchema: z.ZodType<ConditionNode> = z.lazy(() =>
  z.union([
    z.object({
      field: z.string().max(128),
      op: LeafOp,
      value: z.unknown().optional(),
    }),
    z.object({ all: z.array(ConditionSchema).min(1).max(64) }),
    z.object({ any: z.array(ConditionSchema).min(1).max(64) }),
    z.object({ not: ConditionSchema }),
  ])
);

// ── Subject / Resource match ──────────────────────────────────────────────────

const SubjectMatchSchema = z.object({
  roles: z.array(z.string()).optional(),
  userId: z.string().uuid().optional(),
  attributes: z.record(z.unknown()).optional(),
});

const ResourceMatchSchema = z.object({
  dataSourceId: z.string().uuid().optional(),
  schema: z.string().optional(),
  table: z.string().optional(),
  tags: z.array(z.string()).optional(),
});

// ── Obligation ────────────────────────────────────────────────────────────────

const ObligationSchema = z.object({
  kind: z.enum(["require_mfa", "log_access", "rate_limit", "notify"]),
  parameters: z.record(z.unknown()).optional(),
});

// ── Policy ────────────────────────────────────────────────────────────────────

export const PolicyDraftSchema = z.object({
  name: z.string().min(1).max(255),
  effect: z.enum(["allow", "deny"]).default("allow"),
  action: z.string().min(1).max(64).default("read"),
  subjectMatch: SubjectMatchSchema.default({}),
  resourceMatch: ResourceMatchSchema.default({}),
  conditions: ConditionSchema.optional(),
  allowedColumns: z.array(z.string()).optional(),
  deniedColumns: z.array(z.string()).optional(),
  columnMasks: z.record(z.string()).optional(),
  rowFilter: ConditionSchema.optional(),
  obligations: z.array(ObligationSchema).optional(),
  effectiveFrom: z.string().datetime().optional(),
  effectiveTo: z.string().datetime().optional(),
});

export type PolicyDraft = z.infer<typeof PolicyDraftSchema>;

export const POLICY_JSON_SCHEMA = zodToJsonSchema(PolicyDraftSchema, {
  name: "PolicyDraft",
  $refStrategy: "none",
});

export const POLICY_TEMPLATE = JSON.stringify(
  {
    name: "my-policy",
    effect: "allow",
    action: "read",
    subjectMatch: { roles: ["analyst"] },
    resourceMatch: { table: "orders" },
    conditions: {
      all: [
        { field: "subject.attributes.department", op: "eq", value: "finance" },
      ],
    },
    allowedColumns: ["id", "amount", "status"],
    deniedColumns: [],
    columnMasks: {},
    obligations: [],
  } satisfies PolicyDraft,
  null,
  2
);
