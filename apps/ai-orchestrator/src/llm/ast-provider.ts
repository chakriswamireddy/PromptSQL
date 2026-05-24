import Anthropic from '@anthropic-ai/sdk';
import OpenAI from 'openai';
import type { Config } from '../config';
import { llmCallsTotal, llmTokensTotal, llmCostUsd } from '../metrics';

// USD cost per 1K tokens (kept in sync with provider.ts)
const COST_TABLE: Record<string, { in: number; out: number }> = {
  'claude-sonnet-4-6':         { in: 0.003,  out: 0.015  },
  'claude-haiku-4-5-20251001': { in: 0.00025, out: 0.00125 },
  'claude-opus-4-7':           { in: 0.015,  out: 0.075  },
  'gpt-4o':                    { in: 0.005,  out: 0.015  },
  'gpt-4o-mini':               { in: 0.00015, out: 0.0006 },
};

function calcCost(model: string, tokensIn: number, tokensOut: number): number {
  const t = COST_TABLE[model] ?? { in: 0.01, out: 0.03 };
  return (tokensIn / 1000) * t.in + (tokensOut / 1000) * t.out;
}

export interface AstDraftResponse {
  ast: unknown;        // Caller validates with SqlAstSchema
  tokensIn: number;
  tokensOut: number;
  costUsd: number;
  provider: string;
  model: string;
}

// ─── SQL AST JSON Schema (passed to LLM for constrained output) ───────────────
// This mirrors SqlAstSchema from pep-schemas.ts as a JSON Schema object.
// Only the fields the model needs to fill are represented.

const SQL_AST_JSON_SCHEMA: Record<string, unknown> = {
  type: 'object',
  required: ['kind', 'select', 'from', 'limit'],
  additionalProperties: false,
  properties: {
    kind:     { type: 'string', const: 'Select' },
    distinct: { type: 'boolean', default: false },
    select: {
      type: 'array', minItems: 1, maxItems: 64,
      items: {
        oneOf: [
          {
            type: 'object', required: ['kind', 'table', 'column'],
            properties: {
              kind:   { type: 'string', const: 'Column' },
              table:  { type: 'string' },
              column: { type: 'string' },
              alias:  { type: 'string' },
            },
            additionalProperties: false,
          },
          {
            type: 'object', required: ['kind', 'name', 'args'],
            properties: {
              kind:  { type: 'string', const: 'Function' },
              name:  { type: 'string' },
              args:  { type: 'array', items: { type: 'object' } },
              alias: { type: 'string' },
            },
            additionalProperties: false,
          },
          {
            type: 'object', required: ['kind'],
            properties: {
              kind:  { type: 'string', const: 'Star' },
              table: { type: 'string' },
            },
            additionalProperties: false,
          },
        ],
      },
    },
    from: {
      type: 'object', required: ['kind', 'name'],
      properties: {
        kind:   { type: 'string', const: 'Table' },
        schema: { type: 'string', default: 'public' },
        name:   { type: 'string' },
        alias:  { type: 'string' },
      },
      additionalProperties: false,
    },
    joins: {
      type: 'array', maxItems: 6,
      items: {
        type: 'object', required: ['kind', 'table', 'on'],
        properties: {
          kind:      { type: 'string', const: 'Join' },
          join_type: { type: 'string', enum: ['INNER', 'LEFT', 'RIGHT'] },
          table: {
            type: 'object', required: ['kind', 'name'],
            properties: {
              kind:   { type: 'string', const: 'Table' },
              schema: { type: 'string' },
              name:   { type: 'string' },
              alias:  { type: 'string' },
            },
            additionalProperties: false,
          },
          on: { type: 'object' },
        },
        additionalProperties: false,
      },
    },
    where:    { type: 'object' },
    group_by: { type: 'array', maxItems: 8, items: { type: 'object' } },
    having:   { type: 'object' },
    order_by: {
      type: 'array', maxItems: 8,
      items: {
        type: 'object', required: ['expr'],
        properties: {
          expr: { type: 'object' },
          dir:  { type: 'string', enum: ['ASC', 'DESC'] },
          nulls: { type: 'string', enum: ['FIRST', 'LAST'] },
        },
        additionalProperties: false,
      },
    },
    limit:  { type: 'integer', minimum: 1, maximum: 100000 },
    offset: { type: 'integer', minimum: 0 },
  },
};

// ─── Anthropic AST constrained (tool_use) ────────────────────────────────────

async function anthropicAstDraft(opts: {
  apiKey: string;
  model: string;
  systemPrompt: string;
  userMessage: string;
  tenantId: string;
}): Promise<AstDraftResponse> {
  const client = new Anthropic({ apiKey: opts.apiKey });
  const tool: Anthropic.Tool = {
    name: 'draft_sql_ast',
    description: 'Output the SQL query as a structured AST JSON object.',
    input_schema: SQL_AST_JSON_SCHEMA as Anthropic.Tool['input_schema'],
  };

  let response: Anthropic.Message;
  try {
    response = await client.messages.create({
      model: opts.model,
      max_tokens: 4096,
      temperature: 0.1,
      system: opts.systemPrompt,
      messages: [{ role: 'user', content: opts.userMessage }],
      tools: [tool],
      tool_choice: { type: 'any' },
    });
  } catch (err) {
    llmCallsTotal.inc({ node: 'pep_sql_drafter', provider: 'anthropic', model: opts.model, outcome: 'error' });
    throw err;
  }

  const toolBlock = response.content.find((b): b is Anthropic.ToolUseBlock => b.type === 'tool_use');
  if (!toolBlock) {
    llmCallsTotal.inc({ node: 'pep_sql_drafter', provider: 'anthropic', model: opts.model, outcome: 'no_tool_use' });
    throw new Error('Anthropic did not invoke draft_sql_ast tool');
  }

  const tokensIn  = response.usage.input_tokens;
  const tokensOut = response.usage.output_tokens;
  const costUsd   = calcCost(opts.model, tokensIn, tokensOut);

  llmCallsTotal.inc({ node: 'pep_sql_drafter', provider: 'anthropic', model: opts.model, outcome: 'success' });
  llmTokensTotal.inc({ node: 'pep_sql_drafter', provider: 'anthropic', model: opts.model, direction: 'in' }, tokensIn);
  llmTokensTotal.inc({ node: 'pep_sql_drafter', provider: 'anthropic', model: opts.model, direction: 'out' }, tokensOut);
  llmCostUsd.inc({ tenant_id: opts.tenantId, provider: 'anthropic', model: opts.model }, costUsd);

  return { ast: toolBlock.input, tokensIn, tokensOut, costUsd, provider: 'anthropic', model: opts.model };
}

// ─── OpenAI AST constrained (json_schema) ────────────────────────────────────

async function openaiAstDraft(opts: {
  apiKey: string;
  model: string;
  systemPrompt: string;
  userMessage: string;
  tenantId: string;
}): Promise<AstDraftResponse> {
  const client = new OpenAI({ apiKey: opts.apiKey });

  let response: OpenAI.Chat.ChatCompletion;
  try {
    response = await client.chat.completions.create({
      model: opts.model,
      temperature: 0.1,
      messages: [
        { role: 'system', content: opts.systemPrompt },
        { role: 'user',   content: opts.userMessage },
      ],
      response_format: {
        type: 'json_schema',
        json_schema: { name: 'draft_sql_ast', strict: false, schema: SQL_AST_JSON_SCHEMA },
      },
    });
  } catch (err) {
    llmCallsTotal.inc({ node: 'pep_sql_drafter', provider: 'openai', model: opts.model, outcome: 'error' });
    throw err;
  }

  const raw = response.choices[0]?.message?.content ?? '{}';
  const tokensIn  = response.usage?.prompt_tokens ?? 0;
  const tokensOut = response.usage?.completion_tokens ?? 0;
  const costUsd   = calcCost(opts.model, tokensIn, tokensOut);

  llmCallsTotal.inc({ node: 'pep_sql_drafter', provider: 'openai', model: opts.model, outcome: 'success' });
  llmTokensTotal.inc({ node: 'pep_sql_drafter', provider: 'openai', model: opts.model, direction: 'in' }, tokensIn);
  llmTokensTotal.inc({ node: 'pep_sql_drafter', provider: 'openai', model: opts.model, direction: 'out' }, tokensOut);
  llmCostUsd.inc({ tenant_id: opts.tenantId, provider: 'openai', model: opts.model }, costUsd);

  let ast: unknown;
  try {
    ast = JSON.parse(raw);
  } catch {
    throw new Error('OpenAI returned non-JSON AST output');
  }
  return { ast, tokensIn, tokensOut, costUsd, provider: 'openai', model: opts.model };
}

// ─── Dispatcher with failover ─────────────────────────────────────────────────

export async function drafterAst(opts: {
  cfg: Config;
  tenantId: string;
  systemPrompt: string;
  userMessage: string;
}): Promise<AstDraftResponse> {
  const { cfg } = opts;

  try {
    if (cfg.DEFAULT_DRAFTER_PROVIDER === 'anthropic' && cfg.ANTHROPIC_API_KEY) {
      return await anthropicAstDraft({
        apiKey: cfg.ANTHROPIC_API_KEY,
        model: cfg.PEP_DEFAULT_SQL_MODEL,
        systemPrompt: opts.systemPrompt,
        userMessage: opts.userMessage,
        tenantId: opts.tenantId,
      });
    }
  } catch (err) {
    console.warn('Primary AST drafter failed, trying fallback:', String(err));
  }

  if (cfg.FALLBACK_DRAFTER_PROVIDER === 'openai' && cfg.OPENAI_API_KEY) {
    return await openaiAstDraft({
      apiKey: cfg.OPENAI_API_KEY,
      model: cfg.FALLBACK_DRAFTER_MODEL,
      systemPrompt: opts.systemPrompt,
      userMessage: opts.userMessage,
      tenantId: opts.tenantId,
    });
  }

  throw new Error('All LLM providers unavailable for AST generation');
}
