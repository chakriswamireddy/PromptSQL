import type { GraphState } from '../schemas';
import type { Config } from '../../config';
import { anthropicText } from '../../llm/provider';

const SYSTEM_PROMPT = `You classify an administrator's natural-language request into one of these intents:
- role.create  — creating or defining a new role
- policy.update — updating or creating an access-control policy
- grant — granting access to a resource
- revoke — revoking access from a resource

Respond with ONLY one of: role.create, policy.update, grant, revoke
No explanation.`;

type Intent = GraphState['intent'];
const VALID_INTENTS: Intent[] = ['role.create', 'policy.update', 'grant', 'revoke'];

export async function intentParserNode(state: GraphState, cfg: Config): Promise<Partial<GraphState>> {
  const start = Date.now();

  if (!state.sanitized_prompt) {
    return span(state, 'intent_parser', Date.now() - start, { error: 'missing sanitized_prompt' });
  }

  let result;
  try {
    result = await anthropicText({
      apiKey: cfg.ANTHROPIC_API_KEY,
      model: cfg.DEFAULT_INTENT_MODEL,
      systemPrompt: SYSTEM_PROMPT,
      userMessage: state.sanitized_prompt,
      tenantId: state.tenant_id,
      node: 'intent_parser',
      temperature: 0.0,
    });
  } catch (err) {
    return span(state, 'intent_parser', Date.now() - start, { error: String(err) });
  }

  const intent = result.content.trim() as Intent;
  if (!VALID_INTENTS.includes(intent)) {
    // Default to policy.update if model drifts
    return span(state, 'intent_parser', Date.now() - start, {
      intent: 'policy.update',
      total_tokens_in: state.total_tokens_in + result.tokensIn,
      total_tokens_out: state.total_tokens_out + result.tokensOut,
      total_cost_usd: state.total_cost_usd + result.costUsd,
    });
  }

  return span(state, 'intent_parser', Date.now() - start, {
    intent,
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
