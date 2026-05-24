import type { Pool } from 'pg';
import type { GraphState } from '../schemas';
import { approvalActionsTotal } from '../../metrics';

// The human-approval node sets approval_state = 'pending' and writes the
// session record to the DB. Approval/rejection is handled out-of-band via
// POST /v1/ai/pap/approve, which updates the session and unblocks the Compiler.
// This node NEVER auto-approves.

export async function humanApprovalNode(state: GraphState, db: Pool): Promise<Partial<GraphState>> {
  const start = Date.now();

  if (!state.draft_policy) {
    return span(state, 'human_approval', Date.now() - start, { error: 'no draft_policy to approve' });
  }

  // Persist the draft session so the admin can act on it
  await db.query(
    `UPDATE ai_sessions
        SET status        = 'draft',
            graph_run     = $1,
            tokens_in     = $2,
            tokens_out    = $3,
            cost_usd      = $4,
            ended_at      = NOW()
      WHERE id = $5`,
    [
      JSON.stringify({
        draft_policy: state.draft_policy,
        simulator_diff: state.simulator_diff,
        explanation: state.explanation,
        validation_errors: state.validation_errors,
      }),
      state.total_tokens_in,
      state.total_tokens_out,
      state.total_cost_usd,
      state.session_id,
    ],
  );

  approvalActionsTotal.inc({ tenant_id: state.tenant_id, action: 'draft_ready' });

  return span(state, 'human_approval', Date.now() - start, {
    approval_state: 'pending',
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
