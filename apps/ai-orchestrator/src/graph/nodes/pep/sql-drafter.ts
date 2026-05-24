import type { Config } from '../../../config';
import type { PepGraphState } from '../../pep-schemas';
import { SqlAstSchema } from '../../pep-schemas';
import type { AllowedSnapshot } from '../../pep-schemas';
import { drafterAst } from '../../../llm/ast-provider';
import { pepDrafterRetriesTotal } from '../../../metrics';

// SQL Drafter: calls frontier LLM to produce a Calcite-compatible AST.
// The model is instructed to only reference tables/columns present in the
// AllowedSnapshot (which is its only available schema source).
// Retries once on validator rejection before surfacing the error.

export async function pepSqlDrafterNode(
  state: PepGraphState,
  cfg: Config,
): Promise<Partial<PepGraphState>> {
  const start = Date.now();

  if (!state.allowed_snapshot || !state.relevant_tables?.length) {
    return span(state, start, { error: 'No schema context for SQL Drafter', abort_reason: 'drafter_no_schema' });
  }

  const maxRetries = cfg.PEP_DRAFTER_MAX_RETRIES;
  if (state.retry_count >= maxRetries) {
    return span(state, start, {
      error: `SQL Drafter exhausted ${maxRetries} retries`,
      abort_reason: 'drafter_max_retries',
    });
  }

  const systemPrompt = buildSystemPrompt(state.allowed_snapshot, cfg);
  const userMessage  = buildUserMessage(state.sanitized_prompt ?? state.prompt, state.validation_errors);

  let result;
  try {
    result = await drafterAst({ cfg, tenantId: state.tenant_id, systemPrompt, userMessage });
  } catch (err) {
    return span(state, start, {
      error: `LLM call failed: ${String(err)}`,
      abort_reason: 'drafter_llm_error',
    });
  }

  // Validate the AST schema immediately — malformed output never leaves this node
  const parsed = SqlAstSchema.safeParse(result.ast);
  if (!parsed.success) {
    pepDrafterRetriesTotal.inc({ tenant_id: state.tenant_id });
    return span(state, start, {
      error: `Drafter output failed AST schema: ${parsed.error.message}`,
      abort_reason: 'drafter_schema_invalid',
      retry_count: (state.retry_count ?? 0) + 1,
      total_tokens_in:  (state.total_tokens_in  ?? 0) + result.tokensIn,
      total_tokens_out: (state.total_tokens_out ?? 0) + result.tokensOut,
      total_cost_usd:   (state.total_cost_usd   ?? 0) + result.costUsd,
    });
  }

  return {
    ...span(state, start, { ast: parsed.data }),
    total_tokens_in:  (state.total_tokens_in  ?? 0) + result.tokensIn,
    total_tokens_out: (state.total_tokens_out ?? 0) + result.tokensOut,
    total_cost_usd:   (state.total_cost_usd   ?? 0) + result.costUsd,
    node_spans: [
      ...(state.node_spans ?? []),
      {
        node: 'pep_sql_drafter',
        provider: result.provider,
        model: result.model,
        tokens_in: result.tokensIn,
        tokens_out: result.tokensOut,
        cost_usd: result.costUsd,
        latency_ms: Date.now() - start,
      },
    ],
  };
}

function buildSystemPrompt(snapshot: AllowedSnapshot, cfg: Config): string {
  const tableList = snapshot.tables
    .map((t) => {
      const cols = t.columns
        .map((c) => `    - ${c.name} (${c.type})${c.masked ? ' [MASKED]' : ''}${c.description ? ` — ${c.description}` : ''}`)
        .join('\n');
      const fks = (t.foreign_keys ?? [])
        .map((fk) => `    FK: ${fk.column} → ${fk.ref_table}.${fk.ref_column}`)
        .join('\n');
      return `  Table: ${t.schema}.${t.name}\n${cols}${fks ? '\n' + fks : ''}`;
    })
    .join('\n\n');

  return `You are a SQL query generator. You ONLY have access to the following schema.
You must ONLY reference tables and columns listed below.
You must output a valid SQL AST in the specified JSON format.

CONSTRAINTS:
- Only SELECT statements are allowed.
- No DDL, DML, CTEs, window functions, or recursive constructs.
- JOINs must use only the FK edges listed.
- A LIMIT is required (default ${cfg.PEP_MAX_ROWS_DEFAULT}, max ${cfg.PEP_MAX_ROWS_DEFAULT}).
- Do not reference tables or columns not listed below.
- Do not include comments in the AST.

AVAILABLE SCHEMA:
${tableList}

OUTPUT: Respond with ONLY the JSON AST using the draft_sql_ast tool. No explanations.`;
}

function buildUserMessage(prompt: string, previousErrors?: PepGraphState['validation_errors']): string {
  if (previousErrors?.length) {
    const hints = previousErrors.map((e) => `- ${e.message}${e.hint ? ` (hint: ${e.hint})` : ''}`).join('\n');
    return `User question: ${prompt}\n\nPrevious attempt was rejected:\n${hints}\nPlease fix these issues.`;
  }
  return `User question: ${prompt}`;
}

function span(state: PepGraphState, start: number, patch: Partial<PepGraphState>): Partial<PepGraphState> {
  return {
    ...patch,
    node_spans: [
      ...(state.node_spans ?? []),
      { node: 'pep_sql_drafter', latency_ms: Date.now() - start, error: patch.error },
    ],
  };
}
