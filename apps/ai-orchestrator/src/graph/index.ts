import { StateGraph, END } from '@langchain/langgraph';
import type { Pool } from 'pg';
import type { RedisClientType } from 'redis';
import type { Config } from '../config';
import type { GraphState } from './schemas';
import { GraphStateSchema } from './schemas';
import { inputSanitizerNode } from './nodes/input-sanitizer';
import { intentParserNode } from './nodes/intent-parser';
import { schemaResolverNode } from './nodes/schema-resolver';
import { policyDrafterNode } from './nodes/policy-drafter';
import { policyValidatorNode } from './nodes/policy-validator';
import { simulatorNode } from './nodes/simulator';
import { auditExplainerNode } from './nodes/audit-explainer';
import { humanApprovalNode } from './nodes/human-approval';
import { graphDurationSeconds, graphRunsTotal } from '../metrics';

export type NodeEvent = {
  node: string;
  status: 'running' | 'done' | 'error';
  latency_ms?: number;
  error?: string;
  // safe subset of state to stream to UI
  partial?: Partial<Pick<GraphState, 'intent' | 'explanation' | 'draft_policy' | 'simulator_diff' | 'approval_state' | 'total_cost_usd'>>;
};

export type EventCallback = (event: NodeEvent) => void;

function shouldAbort(state: GraphState): boolean {
  return !!state.abort_reason || !!state.error;
}

export async function runPapGraph(opts: {
  initialState: Omit<GraphState, 'node_spans' | 'total_tokens_in' | 'total_tokens_out' | 'total_cost_usd'>;
  db: Pool;
  redis: RedisClientType | null;
  cfg: Config;
  onEvent: EventCallback;
  wallClockBudgetMs?: number;
}): Promise<GraphState> {
  const { db, redis, cfg, onEvent } = opts;
  const budget = opts.wallClockBudgetMs ?? cfg.GRAPH_WALL_CLOCK_BUDGET_MS;
  const deadline = Date.now() + budget;

  const state: GraphState = GraphStateSchema.parse({
    ...opts.initialState,
    node_spans: [],
    total_tokens_in: 0,
    total_tokens_out: 0,
    total_cost_usd: 0,
  });

  const graphStart = Date.now();

  async function runNode<T>(
    name: string,
    fn: () => Promise<Partial<GraphState>>,
  ): Promise<void> {
    if (shouldAbort(state)) return;
    if (Date.now() > deadline) {
      Object.assign(state, { abort_reason: 'wall_clock_budget_exceeded', error: 'Graph timed out' });
      onEvent({ node: name, status: 'error', error: 'timeout' });
      return;
    }
    onEvent({ node: name, status: 'running' });
    const nodeStart = Date.now();
    try {
      const patch = await fn();
      Object.assign(state, patch);
      const latency_ms = Date.now() - nodeStart;
      onEvent({
        node: name,
        status: patch.error ? 'error' : 'done',
        latency_ms,
        error: patch.error,
        partial: {
          intent: state.intent,
          explanation: state.explanation,
          draft_policy: state.draft_policy,
          simulator_diff: state.simulator_diff,
          approval_state: state.approval_state,
          total_cost_usd: state.total_cost_usd,
        },
      });
    } catch (err) {
      const error = String(err);
      Object.assign(state, { error, abort_reason: `${name}_unhandled_exception` });
      onEvent({ node: name, status: 'error', error });
    }
  }

  // Max 6 linear nodes — no cycles
  await runNode('input_sanitizer', () => Promise.resolve(inputSanitizerNode(state)));
  await runNode('intent_parser', () => intentParserNode(state, cfg));
  await runNode('schema_resolver', () => schemaResolverNode(state, db, redis));
  await runNode('policy_drafter', () => policyDrafterNode(state, cfg));
  await runNode('policy_validator', () => policyValidatorNode(state, db));
  await runNode('simulator', () => simulatorNode(state, db));
  await runNode('audit_explainer', () => auditExplainerNode(state, cfg, redis));
  await runNode('human_approval', () => humanApprovalNode(state, db));

  const totalMs = Date.now() - graphStart;
  const outcome = state.error ? 'error' : 'draft_pending';

  graphRunsTotal.inc({ tenant_id: state.tenant_id, outcome });
  graphDurationSeconds.observe({ tenant_id: state.tenant_id, outcome }, totalMs / 1000);

  return state;
}
