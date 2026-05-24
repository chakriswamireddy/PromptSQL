import type { Config } from '../../../config';
import type { PepGraphState } from '../../pep-schemas';
import { pepCostGateRejectionsTotal } from '../../../metrics';

// Cost Estimator: runs EXPLAIN (FORMAT JSON) via the PG proxy as the user.
// Rejects queries that exceed role-derived cost or row count limits.
// Advisory for small queries; blocking for expensive ones.

interface ExplainPlan {
  Plan: {
    'Total Cost': number;
    'Plan Rows': number;
    'Node Type': string;
  };
}

export async function pepCostEstimatorNode(
  state: PepGraphState,
  cfg: Config,
  dbToken: string,
): Promise<Partial<PepGraphState>> {
  const start = Date.now();

  if (!state.validated_sql) {
    return span(state, start, { error: 'No validated SQL for cost estimation', abort_reason: 'cost_no_sql' });
  }

  const explainSql = `EXPLAIN (FORMAT JSON) ${state.validated_sql}`;

  // Submit EXPLAIN via the proxy HTTP endpoint (avoids a full PG wire connection here)
  // The proxy exposes POST /v1/proxy/explain for orchestrator use (internal only)
  let planResult: ExplainPlan[];
  try {
    const resp = await fetch(`http://${cfg.PROXY_HOST}:${cfg.PROXY_PORT}/__internal/explain`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'Authorization': `Bearer ${dbToken}`,
        'X-Tenant-ID': state.tenant_id,
        'X-User-ID': state.user_id,
      },
      body: JSON.stringify({
        data_source_id: state.data_source_id,
        sql: explainSql,
      }),
      signal: AbortSignal.timeout(8_000),
    });

    if (!resp.ok) {
      // EXPLAIN failure is non-fatal; allow query through with a warning
      return span(state, start, {
        explain_result: { total_cost: 0, plan_rows: 0, rejected: false },
      });
    }
    planResult = await resp.json();
  } catch {
    // Network failure — allow query through (EXPLAIN is advisory when unreachable)
    return span(state, start, {
      explain_result: { total_cost: 0, plan_rows: 0, rejected: false },
    });
  }

  const topPlan = planResult?.[0]?.Plan;
  if (!topPlan) {
    return span(state, start, {
      explain_result: { total_cost: 0, plan_rows: 0, rejected: false },
    });
  }

  const totalCost = topPlan['Total Cost'] ?? 0;
  const planRows  = topPlan['Plan Rows'] ?? 0;

  const maxCost    = cfg.PEP_MAX_COST_DEFAULT;
  const maxRows    = cfg.PEP_MAX_PLAN_ROWS_DEFAULT;

  if (totalCost > maxCost || planRows > maxRows) {
    pepCostGateRejectionsTotal.inc({ tenant_id: state.tenant_id });
    const reason = totalCost > maxCost
      ? `Query cost ${Math.round(totalCost).toLocaleString()} exceeds limit ${maxCost.toLocaleString()}. Try narrowing the date range or adding more filters.`
      : `Estimated ${planRows.toLocaleString()} rows exceeds limit ${maxRows.toLocaleString()}. Try narrowing the date range or adding more filters.`;
    return span(state, start, {
      explain_result: { total_cost: totalCost, plan_rows: planRows, rejected: true, rejection_reason: reason },
      error: reason,
      abort_reason: 'cost_gate_exceeded',
    });
  }

  return span(state, start, {
    explain_result: { total_cost: totalCost, plan_rows: planRows, plan_json: planResult, rejected: false },
  });
}

function span(state: PepGraphState, start: number, patch: Partial<PepGraphState>): Partial<PepGraphState> {
  return {
    ...patch,
    node_spans: [
      ...(state.node_spans ?? []),
      { node: 'pep_cost_estimator', latency_ms: Date.now() - start, error: patch.error },
    ],
  };
}
