import type { Pool } from 'pg';
import type { GraphState } from '../schemas';

// Compiler writes the draft to the policies table with status='draft'.
// It is idempotent: (tenant_id, ai_session_id) duplicate → return existing id.
// Approval is handled separately; the policy transitions to 'pending_review' via Phase 4 workflow.

export async function compilerNode(state: GraphState, db: Pool): Promise<Partial<GraphState>> {
  const start = Date.now();

  if (!state.draft_policy) {
    return span(state, 'compiler', Date.now() - start, { error: 'no draft_policy to compile' });
  }
  if (state.approval_state !== 'pending') {
    return span(state, 'compiler', Date.now() - start, {
      error: 'approval_state is not pending — human approval gate was bypassed',
    });
  }

  const client = await db.connect();
  try {
    await client.query('BEGIN');
    await client.query(`SET LOCAL ROLE app_readwrite`);
    await client.query(`SELECT set_config('app.tenant_id', $1, true)`, [state.tenant_id]);
    await client.query(`SELECT set_config('app.user_id', $1, true)`, [state.user_id]);

    // Idempotency: check if we already compiled this session
    const existing = await client.query<{ id: string }>(
      `SELECT id FROM policies WHERE ai_session_id = $1 AND tenant_id = $2 LIMIT 1`,
      [state.session_id, state.tenant_id],
    );
    if (existing.rows.length > 0) {
      await client.query('ROLLBACK');
      return span(state, 'compiler', Date.now() - start, {
        compiled_policy_id: existing.rows[0]!.id,
      });
    }

    const policy = state.draft_policy;
    const { rows } = await client.query<{ id: string }>(
      `INSERT INTO policies
           (tenant_id, name, description, subject, rules, status,
            created_by, created_by_ai, ai_session_id, model_metadata)
         VALUES ($1, $2, $3, $4, $5, 'draft', $6, true, $7, $8)
         RETURNING id`,
      [
        state.tenant_id,
        policy.name,
        policy.description ?? '',
        JSON.stringify(policy.subject),
        JSON.stringify(policy.rules),
        state.user_id,
        state.session_id,
        JSON.stringify({
          session_id: state.session_id,
          tokens_in: state.total_tokens_in,
          tokens_out: state.total_tokens_out,
          cost_usd: state.total_cost_usd,
          spans: state.node_spans,
        }),
      ],
    );

    const policyId = rows[0]!.id;

    // Outbox event for cache invalidation (Phase 3 / PDP pub-sub)
    await client.query(
      `INSERT INTO outbox_events (tenant_id, event_type, payload, idempotency_key)
         VALUES ($1, 'policy.drafted', $2, $3)`,
      [
        state.tenant_id,
        JSON.stringify({ policy_id: policyId, session_id: state.session_id }),
        `drafted:${state.session_id}`,
      ],
    );

    await client.query('COMMIT');

    return span(state, 'compiler', Date.now() - start, {
      compiled_policy_id: policyId,
    });
  } catch (err) {
    await client.query('ROLLBACK').catch(() => {});
    return span(state, 'compiler', Date.now() - start, { error: String(err) });
  } finally {
    client.release();
  }
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
