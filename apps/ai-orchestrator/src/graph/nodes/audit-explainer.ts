import crypto from 'crypto';
import type { RedisClientType } from 'redis';
import type { GraphState } from '../schemas';
import type { Config } from '../../config';
import { anthropicText } from '../../llm/provider';

const SYSTEM_PROMPT = `You are a compliance officer explaining an access-control policy change in plain English.
Write exactly 3 short paragraphs:
1. Who is affected (roles, users, conditions).
2. What changes (resources granted/restricted, column masks, row filters added or removed).
3. Risks and obligations (data sensitivity, deny-override conflicts, required reviews).

Be factual and concise. No legal advice. Maximum 300 words total.`;

export async function auditExplainerNode(
  state: GraphState,
  cfg: Config,
  redis: RedisClientType | null,
): Promise<Partial<GraphState>> {
  const start = Date.now();

  if (!state.draft_policy) {
    return span(state, 'audit_explainer', Date.now() - start, { error: 'no draft_policy' });
  }

  const policyHash = crypto
    .createHash('sha256')
    .update(JSON.stringify(state.draft_policy))
    .digest('hex');
  const diffHash = crypto
    .createHash('sha256')
    .update(JSON.stringify(state.simulator_diff ?? {}))
    .digest('hex');
  const cacheKey = `pap:explain:${policyHash}:${diffHash}`;

  if (redis) {
    const cached = await redis.get(cacheKey);
    if (cached) {
      return span(state, 'audit_explainer', Date.now() - start, { explanation: cached });
    }
  }

  const userMessage = `
Policy JSON:
${JSON.stringify(state.draft_policy, null, 2)}

Simulator diff summary:
${JSON.stringify(state.simulator_diff ?? {}, null, 2)}

Write the 3-paragraph explanation.`;

  let result;
  try {
    result = await anthropicText({
      apiKey: cfg.ANTHROPIC_API_KEY,
      model: cfg.DEFAULT_EXPLAINER_MODEL,
      systemPrompt: SYSTEM_PROMPT,
      userMessage,
      tenantId: state.tenant_id,
      node: 'audit_explainer',
      temperature: 0.0,
    });
  } catch (err) {
    return span(state, 'audit_explainer', Date.now() - start, { error: String(err) });
  }

  const explanation = result.content.trim();

  if (redis) {
    await redis.setEx(cacheKey, cfg.EXPLAINER_CACHE_TTL_SEC, explanation);
  }

  return span(state, 'audit_explainer', Date.now() - start, {
    explanation,
    total_tokens_in: state.total_tokens_in + result.tokensIn,
    total_tokens_out: state.total_tokens_out + result.tokensOut,
    total_cost_usd: state.total_cost_usd + result.costUsd,
  });
}

function span(
  state: GraphState,
  node: string,
  latency_ms: number,
  patch: Partial<GraphState>,
): Partial<GraphState> {
  return {
    ...patch,
    node_spans: [...(state.node_spans ?? []), { node, latency_ms, error: patch.error }],
  };
}
