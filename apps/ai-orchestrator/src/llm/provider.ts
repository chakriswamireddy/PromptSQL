import Anthropic from '@anthropic-ai/sdk';
import OpenAI from 'openai';
import type { Config } from '../config';
import type { PolicyDraft } from '../graph/schemas';
import { PolicyDraftSchema } from '../graph/schemas';
import { llmCallsTotal, llmTokensTotal, llmCostUsd } from '../metrics';

// USD cost per 1K tokens (rough, update as needed)
const COST_TABLE: Record<string, { in: number; out: number }> = {
  'claude-sonnet-4-6':         { in: 0.003,  out: 0.015  },
  'claude-haiku-4-5-20251001': { in: 0.00025, out: 0.00125 },
  'claude-opus-4-7':           { in: 0.015,  out: 0.075  },
  'gpt-4o':                    { in: 0.005,  out: 0.015  },
  'gpt-4o-mini':               { in: 0.00015, out: 0.0006 },
};

export interface LLMResponse {
  content: string;
  tokensIn: number;
  tokensOut: number;
  costUsd: number;
  provider: string;
  model: string;
}

export interface LLMConstrainedResponse {
  policy: PolicyDraft;
  tokensIn: number;
  tokensOut: number;
  costUsd: number;
  provider: string;
  model: string;
}

function calcCost(model: string, tokensIn: number, tokensOut: number): number {
  const t = COST_TABLE[model] ?? { in: 0.01, out: 0.03 };
  return (tokensIn / 1000) * t.in + (tokensOut / 1000) * t.out;
}

// ─── Anthropic constrained-decoding (tool_use) ────────────────────────────────

export async function anthropicConstrained(opts: {
  apiKey: string;
  model: string;
  systemPrompt: string;
  userMessage: string;
  jsonSchema: Record<string, unknown>;
  tenantId: string;
  node: string;
}): Promise<LLMConstrainedResponse> {
  const client = new Anthropic({ apiKey: opts.apiKey });

  const tool: Anthropic.Tool = {
    name: 'draft_policy',
    description: 'Output the drafted access-control policy as structured JSON.',
    input_schema: opts.jsonSchema as Anthropic.Tool['input_schema'],
  };

  let response: Anthropic.Message;
  try {
    response = await client.messages.create({
      model: opts.model,
      max_tokens: 4096,
      temperature: 0.2,
      system: opts.systemPrompt,
      messages: [{ role: 'user', content: opts.userMessage }],
      tools: [tool],
      tool_choice: { type: 'any' },
    });
  } catch (err) {
    llmCallsTotal.inc({ node: opts.node, provider: 'anthropic', model: opts.model, outcome: 'error' });
    throw err;
  }

  const toolBlock = response.content.find((b): b is Anthropic.ToolUseBlock => b.type === 'tool_use');
  if (!toolBlock) {
    llmCallsTotal.inc({ node: opts.node, provider: 'anthropic', model: opts.model, outcome: 'no_tool_use' });
    throw new Error('Anthropic did not invoke draft_policy tool');
  }

  const tokensIn  = response.usage.input_tokens;
  const tokensOut = response.usage.output_tokens;
  const costUsd   = calcCost(opts.model, tokensIn, tokensOut);

  llmCallsTotal.inc({ node: opts.node, provider: 'anthropic', model: opts.model, outcome: 'success' });
  llmTokensTotal.inc({ node: opts.node, provider: 'anthropic', model: opts.model, direction: 'in' }, tokensIn);
  llmTokensTotal.inc({ node: opts.node, provider: 'anthropic', model: opts.model, direction: 'out' }, tokensOut);
  llmCostUsd.inc({ tenant_id: opts.tenantId, provider: 'anthropic', model: opts.model }, costUsd);

  const parsed = PolicyDraftSchema.parse(toolBlock.input);
  return { policy: parsed, tokensIn, tokensOut, costUsd, provider: 'anthropic', model: opts.model };
}

// ─── OpenAI constrained-decoding (json_schema) ───────────────────────────────

export async function openaiConstrained(opts: {
  apiKey: string;
  model: string;
  systemPrompt: string;
  userMessage: string;
  jsonSchema: Record<string, unknown>;
  tenantId: string;
  node: string;
}): Promise<LLMConstrainedResponse> {
  const client = new OpenAI({ apiKey: opts.apiKey });

  let response: OpenAI.Chat.ChatCompletion;
  try {
    response = await client.chat.completions.create({
      model: opts.model,
      temperature: 0.2,
      messages: [
        { role: 'system', content: opts.systemPrompt },
        { role: 'user', content: opts.userMessage },
      ],
      response_format: {
        type: 'json_schema',
        json_schema: { name: 'draft_policy', strict: true, schema: opts.jsonSchema },
      },
    });
  } catch (err) {
    llmCallsTotal.inc({ node: opts.node, provider: 'openai', model: opts.model, outcome: 'error' });
    throw err;
  }

  const raw = response.choices[0]?.message?.content ?? '';
  const tokensIn  = response.usage?.prompt_tokens ?? 0;
  const tokensOut = response.usage?.completion_tokens ?? 0;
  const costUsd   = calcCost(opts.model, tokensIn, tokensOut);

  llmCallsTotal.inc({ node: opts.node, provider: 'openai', model: opts.model, outcome: 'success' });
  llmTokensTotal.inc({ node: opts.node, provider: 'openai', model: opts.model, direction: 'in' }, tokensIn);
  llmTokensTotal.inc({ node: opts.node, provider: 'openai', model: opts.model, direction: 'out' }, tokensOut);
  llmCostUsd.inc({ tenant_id: opts.tenantId, provider: 'openai', model: opts.model }, costUsd);

  let parsed: PolicyDraft;
  try {
    parsed = PolicyDraftSchema.parse(JSON.parse(raw));
  } catch (e) {
    throw new Error(`OpenAI output failed Zod validation: ${String(e)}`);
  }
  return { policy: parsed, tokensIn, tokensOut, costUsd, provider: 'openai', model: opts.model };
}

// ─── Simple text completion (intent parser, explainer) ────────────────────────

export async function anthropicText(opts: {
  apiKey: string;
  model: string;
  systemPrompt: string;
  userMessage: string;
  tenantId: string;
  node: string;
  temperature?: number;
}): Promise<LLMResponse> {
  const client = new Anthropic({ apiKey: opts.apiKey });
  const response = await client.messages.create({
    model: opts.model,
    max_tokens: 1024,
    temperature: opts.temperature ?? 0.0,
    system: opts.systemPrompt,
    messages: [{ role: 'user', content: opts.userMessage }],
  });

  const content = response.content
    .filter((b): b is Anthropic.TextBlock => b.type === 'text')
    .map((b) => b.text)
    .join('');
  const tokensIn  = response.usage.input_tokens;
  const tokensOut = response.usage.output_tokens;
  const costUsd   = calcCost(opts.model, tokensIn, tokensOut);

  llmCallsTotal.inc({ node: opts.node, provider: 'anthropic', model: opts.model, outcome: 'success' });
  llmTokensTotal.inc({ node: opts.node, provider: 'anthropic', model: opts.model, direction: 'in' }, tokensIn);
  llmTokensTotal.inc({ node: opts.node, provider: 'anthropic', model: opts.model, direction: 'out' }, tokensOut);
  llmCostUsd.inc({ tenant_id: opts.tenantId, provider: 'anthropic', model: opts.model }, costUsd);

  return { content, tokensIn, tokensOut, costUsd, provider: 'anthropic', model: opts.model };
}

// ─── Dispatch helper with provider failover ──────────────────────────────────

export async function constrainedDraft(opts: {
  cfg: Config;
  tenantId: string;
  systemPrompt: string;
  userMessage: string;
  jsonSchema: Record<string, unknown>;
}): Promise<LLMConstrainedResponse> {
  try {
    if (opts.cfg.DEFAULT_DRAFTER_PROVIDER === 'anthropic' && opts.cfg.ANTHROPIC_API_KEY) {
      return await anthropicConstrained({
        apiKey: opts.cfg.ANTHROPIC_API_KEY,
        model: opts.cfg.DEFAULT_DRAFTER_MODEL,
        systemPrompt: opts.systemPrompt,
        userMessage: opts.userMessage,
        jsonSchema: opts.jsonSchema,
        tenantId: opts.tenantId,
        node: 'drafter',
      });
    }
  } catch (err) {
    console.warn('Primary drafter failed, trying fallback:', String(err));
  }

  // Fallback
  if (opts.cfg.FALLBACK_DRAFTER_PROVIDER === 'openai' && opts.cfg.OPENAI_API_KEY) {
    return await openaiConstrained({
      apiKey: opts.cfg.OPENAI_API_KEY,
      model: opts.cfg.FALLBACK_DRAFTER_MODEL,
      systemPrompt: opts.systemPrompt,
      userMessage: opts.userMessage,
      jsonSchema: opts.jsonSchema,
      tenantId: opts.tenantId,
      node: 'drafter',
    });
  }

  throw new Error('All LLM providers unavailable');
}
