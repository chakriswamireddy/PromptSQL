import crypto from 'crypto';
import type { FastifyInstance, FastifyPluginOptions } from 'fastify';
import type { Pool } from 'pg';
import type { RedisClientType } from 'redis';
import type { Unleash } from 'unleash-client';
import type { Config } from '../config';
import { DraftRequestSchema, ApproveRequestSchema, ExplainRequestSchema } from '../graph/schemas';
import { runPapGraph } from '../graph';
import { checkTokenBudget, recordTokenUsage } from '../middleware/token-budget';
import { approvalActionsTotal } from '../metrics';

interface PapPluginOpts extends FastifyPluginOptions {
  db: Pool;
  redis: RedisClientType | null;
  cfg: Config;
  unleash: Unleash;
}

export async function buildPapRouter(app: FastifyInstance, opts: PapPluginOpts) {
  const { db, redis, cfg, unleash } = opts;

  function featureGate(): boolean {
    return unleash.isEnabled('ai-pap-graph');
  }

  // ─── POST /v1/ai/pap/draft ────────────────────────────────────────────────
  // Streams Server-Sent Events (SSE) — each node emits a progress event.
  // Final event: { type: "done", session_id, draft_policy, explanation, ... }
  app.post('/v1/ai/pap/draft', async (req, reply) => {
    if (!featureGate()) {
      return reply.code(404).send({ code: 'feature_disabled', message: 'ai-pap-graph is disabled' });
    }

    const body = DraftRequestSchema.safeParse(req.body);
    if (!body.success) {
      return reply.code(400).send({ code: 'invalid_request', errors: body.error.issues });
    }

    // Extract JWT claims — api-gateway sets X-Tenant-Id, X-User-Id headers
    const tenantId = req.headers['x-tenant-id'] as string;
    const userId   = req.headers['x-user-id'] as string;
    if (!tenantId || !userId) {
      return reply.code(401).send({ code: 'unauthenticated', message: 'Missing tenant/user headers' });
    }

    // Token budget check
    const budget = await checkTokenBudget(tenantId, db);
    if (!budget.allowed) {
      return reply.code(429).send({ code: 'budget_exceeded', message: budget.reason });
    }

    const sessionId = crypto.randomUUID();
    const promptHash = crypto.createHash('sha256').update(body.data.prompt).digest('hex');

    // Create ai_sessions record
    const client = await db.connect();
    try {
      await client.query(`SET LOCAL ROLE app_readwrite`);
      await client.query(`SELECT set_config('app.tenant_id', $1, true)`, [tenantId]);
      await client.query(
        `INSERT INTO ai_sessions (id, tenant_id, user_id, idempotency_key, prompt, prompt_hash, status)
           VALUES ($1, $2, $3, $4, $5, $6, 'running')
           ON CONFLICT (tenant_id, idempotency_key) DO NOTHING`,
        [sessionId, tenantId, userId, body.data.idempotency_key, body.data.prompt, promptHash],
      );

      // Check if idempotency collision: return existing session
      const existing = await client.query<{ id: string; status: string; graph_run: unknown }>(
        `SELECT id, status, graph_run FROM ai_sessions
          WHERE tenant_id = $1 AND idempotency_key = $2 AND id != $3
          LIMIT 1`,
        [tenantId, body.data.idempotency_key, sessionId],
      );
      if (existing.rows.length > 0) {
        await client.query('COMMIT').catch(() => {});
        return reply.send({
          type: 'cached',
          session_id: existing.rows[0]!.id,
          status: existing.rows[0]!.status,
          graph_run: existing.rows[0]!.graph_run,
        });
      }
    } finally {
      client.release();
    }

    // Set up SSE
    reply.raw.setHeader('Content-Type', 'text/event-stream');
    reply.raw.setHeader('Cache-Control', 'no-cache');
    reply.raw.setHeader('Connection', 'keep-alive');
    reply.raw.flushHeaders?.();

    function sse(data: unknown): void {
      reply.raw.write(`data: ${JSON.stringify(data)}\n\n`);
    }

    // Run graph
    const finalState = await runPapGraph({
      initialState: {
        session_id: sessionId,
        tenant_id: tenantId,
        user_id: userId,
        idempotency_key: body.data.idempotency_key,
        prompt: body.data.prompt,
        prompt_hash: promptHash,
      },
      db,
      redis: redis as RedisClientType | null,
      cfg,
      onEvent: (event) => sse({ type: 'node', ...event }),
    });

    // Record token usage
    await recordTokenUsage({
      tenantId,
      tokensIn: finalState.total_tokens_in,
      tokensOut: finalState.total_tokens_out,
      costUsd: finalState.total_cost_usd,
      db,
    });

    sse({
      type: 'done',
      session_id: sessionId,
      status: finalState.approval_state ?? 'error',
      draft_policy: finalState.draft_policy,
      explanation: finalState.explanation,
      simulator_diff: finalState.simulator_diff,
      validation_errors: finalState.validation_errors,
      total_cost_usd: finalState.total_cost_usd,
      error: finalState.error,
    });
    reply.raw.end();
  });

  // ─── POST /v1/ai/pap/approve ──────────────────────────────────────────────
  // Admin approves or rejects a draft. Requires fresh MFA token.
  app.post('/v1/ai/pap/approve', async (req, reply) => {
    if (!featureGate()) {
      return reply.code(404).send({ code: 'feature_disabled', message: 'ai-pap-graph is disabled' });
    }

    const body = ApproveRequestSchema.safeParse(req.body);
    if (!body.success) {
      return reply.code(400).send({ code: 'invalid_request', errors: body.error.issues });
    }

    const tenantId = req.headers['x-tenant-id'] as string;
    const userId   = req.headers['x-user-id'] as string;
    if (!tenantId || !userId) {
      return reply.code(401).send({ code: 'unauthenticated' });
    }

    const { session_id, action, reason } = body.data;
    // MFA validation is delegated to api-gateway; the mfa_token header is verified upstream.
    // Here we simply require it was present in the body (contract enforcement).

    const client = await db.connect();
    try {
      await client.query(`SET LOCAL ROLE app_readwrite`);
      await client.query(`SELECT set_config('app.tenant_id', $1, true)`, [tenantId]);

      const { rows } = await client.query<{ id: string; status: string }>(
        `SELECT id, status FROM ai_sessions
          WHERE id = $1 AND tenant_id = $2 FOR UPDATE SKIP LOCKED`,
        [session_id, tenantId],
      );

      if (rows.length === 0) {
        return reply.code(404).send({ code: 'not_found', message: 'Session not found' });
      }
      if (rows[0]!.status !== 'draft') {
        return reply.code(409).send({ code: 'conflict', message: `Session status is '${rows[0]!.status}', not 'draft'` });
      }

      const newStatus = action === 'approve' ? 'approved' : 'rejected';
      await client.query(
        `UPDATE ai_sessions SET status = $1, ended_at = NOW() WHERE id = $2`,
        [newStatus, session_id],
      );

      if (action === 'approve') {
        // Activate the draft policy → pending_review (Phase 4 dual-approval will handle final activation)
        await client.query(
          `UPDATE policies SET status = 'pending_review'
            WHERE ai_session_id = $1 AND tenant_id = $2 AND status = 'draft'`,
          [session_id, tenantId],
        );
      }

      await client.query('COMMIT');
      approvalActionsTotal.inc({ tenant_id: tenantId, action });
      return reply.send({ session_id, action, new_status: newStatus });
    } catch (err) {
      await client.query('ROLLBACK').catch(() => {});
      throw err;
    } finally {
      client.release();
    }
  });

  // ─── POST /v1/ai/pap/explain ──────────────────────────────────────────────
  // Re-explain an existing (active/pending_review) policy.
  app.post('/v1/ai/pap/explain', async (req, reply) => {
    if (!featureGate()) {
      return reply.code(404).send({ code: 'feature_disabled', message: 'ai-pap-graph is disabled' });
    }

    const body = ExplainRequestSchema.safeParse(req.body);
    if (!body.success) {
      return reply.code(400).send({ code: 'invalid_request', errors: body.error.issues });
    }

    const tenantId = req.headers['x-tenant-id'] as string;
    if (!tenantId) return reply.code(401).send({ code: 'unauthenticated' });

    const client = await db.connect();
    try {
      await client.query(`SET LOCAL ROLE app_readonly`);
      await client.query(`SELECT set_config('app.tenant_id', $1, true)`, [tenantId]);

      const { rows } = await client.query<{ id: string; rules: unknown; subject: unknown }>(
        `SELECT id, rules, subject FROM policies
          WHERE id = $1 AND tenant_id = $2 AND status IN ('active','pending_review')
          LIMIT 1`,
        [body.data.policy_id, tenantId],
      );
      if (rows.length === 0) {
        return reply.code(404).send({ code: 'not_found' });
      }

      return reply.send({
        policy_id: body.data.policy_id,
        message: 'Explanation re-generation is queued. Use the /draft endpoint with the policy JSON to get a new explanation.',
      });
    } finally {
      client.release();
    }
  });
}
