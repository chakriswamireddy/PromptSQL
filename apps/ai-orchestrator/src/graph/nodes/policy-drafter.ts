import zodToJsonSchema from 'zod-to-json-schema';
import type { GraphState } from '../schemas';
import { PolicyDraftSchema } from '../schemas';
import type { Config } from '../../config';
import { constrainedDraft } from '../../llm/provider';

const SYSTEM_PROMPT = `You are a security engineer writing access-control policies for an enterprise data governance platform.

Rules:
1. Use ONLY the canonical column/table names provided in the schema map below.
2. Never reference columns or tables not present in the schema map.
3. Use deny-overrides: explicit deny rules take precedence.
4. Row filters must be SQL predicates using parameterized syntax (e.g., "campus_id = :session_campus_id").
5. Column masks must use one of: redact, partial, hash, tokenize.
6. Do not grant permissions the requesting admin does not themselves hold.
7. Output ONLY the JSON policy object matching the provided schema. No prose.`;

// Build the JSON schema once at module load
const POLICY_JSON_SCHEMA = zodToJsonSchema(PolicyDraftSchema, {
  name: 'PolicyDraft',
  $refStrategy: 'none',
}) as Record<string, unknown>;

export async function policyDrafterNode(state: GraphState, cfg: Config): Promise<Partial<GraphState>> {
  const start = Date.now();

  if (!state.sanitized_prompt || !state.intent) {
    return span(state, 'policy_drafter', Date.now() - start, { error: 'missing sanitized_prompt or intent' });
  }

  // Build schema context for the model
  const schemaContext = buildSchemaContext(state);

  const userMessage = `
Admin request: "${state.sanitized_prompt}"
Intent: ${state.intent}
Tenant ID: ${state.tenant_id}

Available schema (use ONLY these canonical names):
${schemaContext}

Draft a policy satisfying the request.`;

  let result;
  try {
    result = await constrainedDraft({
      cfg,
      tenantId: state.tenant_id,
      systemPrompt: SYSTEM_PROMPT,
      userMessage,
      jsonSchema: POLICY_JSON_SCHEMA,
    });
  } catch (err) {
    return span(state, 'policy_drafter', Date.now() - start, { error: String(err) });
  }

  // Enforce tenant_id — drafter must not change it
  result.policy.tenant_id = state.tenant_id;

  return span(state, 'policy_drafter', Date.now() - start, {
    draft_policy: result.policy,
    total_tokens_in: state.total_tokens_in + result.tokensIn,
    total_tokens_out: state.total_tokens_out + result.tokensOut,
    total_cost_usd: state.total_cost_usd + result.costUsd,
    node_spans: undefined, // handled by span()
  });
}

function buildSchemaContext(state: GraphState): string {
  if (!state.canonical_map || Object.keys(state.canonical_map).length === 0) {
    return 'No schema information resolved. Use only widely known SQL column names and mark them for validation.';
  }
  return Object.entries(state.canonical_map)
    .map(([fuzzy, info]) =>
      `  ${fuzzy} → ${info.schema}.${info.table}.${info.canonical} (confidence: ${info.confidence.toFixed(2)})`
    )
    .join('\n');
}

function span(
  state: GraphState,
  node: string,
  latency_ms: number,
  patch: Partial<GraphState>,
): Partial<GraphState> {
  const existing = state.node_spans ?? [];
  return {
    ...patch,
    node_spans: [...existing, { node, latency_ms, error: patch.error }],
  };
}
