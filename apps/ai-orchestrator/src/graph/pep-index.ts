import type { Pool } from 'pg';
import type { RedisClientType } from 'redis';
import type { Config } from '../config';
import type { PepGraphState } from './pep-schemas';
import { PepGraphStateSchema } from './pep-schemas';
import { pepInputSanitizerNode }     from './nodes/pep/input-sanitizer';
import { pepPermissionResolverNode } from './nodes/pep/permission-resolver';
import { pepRetrieverNode }          from './nodes/pep/retriever';
import { pepSqlDrafterNode }         from './nodes/pep/sql-drafter';
import { pepAstValidatorNode }       from './nodes/pep/ast-validator';
import { pepCostEstimatorNode }      from './nodes/pep/cost-estimator';
import { pepProxyExecutorNode }      from './nodes/pep/proxy-executor';
import { pepResultFormatterNode }    from './nodes/pep/result-formatter';
import { pepGraphRunsTotal, pepGraphDurationSeconds } from '../metrics';

export type PepNodeEvent = {
  node: string;
  status: 'running' | 'done' | 'error';
  latency_ms?: number;
  error?: string;
  partial?: Partial<Pick<PepGraphState, 'allowed_snapshot' | 'validated_sql' | 'result' | 'explain_result' | 'total_cost_usd'>>;
};

export type PepEventCallback = (event: PepNodeEvent) => void;

function shouldAbort(state: PepGraphState): boolean {
  return !!state.abort_reason || !!state.error;
}

export async function runPepGraph(opts: {
  initialState: Omit<PepGraphState, 'node_spans' | 'total_tokens_in' | 'total_tokens_out' | 'total_cost_usd' | 'retry_count'>;
  db: Pool;
  redis: RedisClientType | null;
  cfg: Config;
  dbToken: string;
  onEvent: PepEventCallback;
  wallClockBudgetMs?: number;
}): Promise<PepGraphState> {
  const { db, redis, cfg, dbToken, onEvent } = opts;
  const budget   = opts.wallClockBudgetMs ?? cfg.PEP_WALL_CLOCK_BUDGET_MS;
  const deadline = Date.now() + budget;

  const state: PepGraphState = PepGraphStateSchema.parse({
    ...opts.initialState,
    node_spans:       [],
    total_tokens_in:  0,
    total_tokens_out: 0,
    total_cost_usd:   0,
    retry_count:      0,
  });

  const graphStart = Date.now();

  async function runNode(
    name: string,
    fn: () => Promise<Partial<PepGraphState>>,
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
          allowed_snapshot: state.allowed_snapshot,
          validated_sql:    state.validated_sql,
          result:           state.result,
          explain_result:   state.explain_result,
          total_cost_usd:   state.total_cost_usd,
        },
      });
    } catch (err) {
      const error = String(err);
      Object.assign(state, { error, abort_reason: `${name}_unhandled_exception` });
      onEvent({ node: name, status: 'error', error });
    }
  }

  // ─── Phase 10 PEP graph: 8 nodes, linear ────────────────────────────────
  await runNode('pep_input_sanitizer',     () => Promise.resolve(pepInputSanitizerNode(state)));
  await runNode('pep_permission_resolver', () => pepPermissionResolverNode(state, cfg, redis));
  await runNode('pep_retriever',           () => pepRetrieverNode(state, db, redis));

  // Drafter → Validator loop (max PEP_DRAFTER_MAX_RETRIES retries)
  const maxRetries = cfg.PEP_DRAFTER_MAX_RETRIES;
  let draftCycles = 0;
  while (!shouldAbort(state) && draftCycles <= maxRetries) {
    await runNode('pep_sql_drafter',   () => pepSqlDrafterNode(state, cfg));
    if (shouldAbort(state)) break;
    await runNode('pep_ast_validator', () => Promise.resolve(pepAstValidatorNode(state)));
    // If validation passed (no errors), break the loop
    if (!state.validation_errors?.length && !shouldAbort(state)) break;
    // If there are validation errors but abort_reason not set, allow retry
    if (state.validation_errors?.length && !state.abort_reason) {
      // Clear error so the loop can continue to the next drafter attempt
      state.error = undefined;
      draftCycles++;
    } else {
      break;
    }
  }

  await runNode('pep_cost_estimator', () => pepCostEstimatorNode(state, cfg, dbToken));
  await runNode('pep_proxy_executor', () => pepProxyExecutorNode(state, cfg, dbToken));
  await runNode('pep_result_formatter', () => Promise.resolve(pepResultFormatterNode(state, cfg)));

  const totalMs = Date.now() - graphStart;
  const outcome = state.error ? 'error' : 'done';

  pepGraphRunsTotal.inc({ tenant_id: state.tenant_id, outcome });
  pepGraphDurationSeconds.observe({ tenant_id: state.tenant_id, outcome }, totalMs / 1000);

  return state;
}
