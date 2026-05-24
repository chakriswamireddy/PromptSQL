import crypto from 'crypto';
import type { FastifyInstance, FastifyPluginOptions } from 'fastify';
import type { Pool } from 'pg';
import type { RedisClientType } from 'redis';
import type { Unleash } from 'unleash-client';
import type { Config } from '../config';
import {
  PepAskRequestSchema,
  PepFeedbackRequestSchema,
  SaveQuestionRequestSchema,
} from '../graph/pep-schemas';
import { runPepGraph } from '../graph/pep-index';
import { checkTokenBudget, recordTokenUsage } from '../middleware/token-budget';
import { pepFeedbackTotal } from '../metrics';

interface PepPluginOpts extends FastifyPluginOptions {
  db: Pool;
  redis: RedisClientType | null;
  cfg: Config;
  unleash: Unleash;
}

export async function buildPepRouter(app: FastifyInstance, opts: PepPluginOpts) {
  const { db, redis, cfg, unleash } = opts;

  function featureGate(): boolean {
    return unleash.isEnabled('ai-pep-graph');
  }

  // ─── POST /v1/ai/pep/ask ──────────────────────────────────────────────────
  // SSE stream: emits progress events per node, then final result.
  // The caller supplies data_source_id; the graph resolves the allowed snapshot.
  app.post('/v1/ai/pep/ask', async (req, reply) => {
    if (!featureGate()) {
      return reply.code(404).send({ code: 'feature_disabled', message: 'ai-pep-graph is disabled' });
    }

    const body = PepAskRequestSchema.safeParse(req.body);
    if (!body.success) {
      return reply.code(400).send({ code: 'invalid_request', errors: body.error.issues });
    }

    const tenantId = req.headers['x-tenant-id'] as string;
    const userId   = req.headers['x-user-id']   as string;
    const dbToken  = req.headers['x-db-token']  as string;

    if (!tenantId || !userId) {
      return reply.code(401).send({ code: 'unauthenticated', message: 'Missing tenant/user headers' });
    }
    if (!dbToken) {
      return reply.code(401).send({ code: 'db_token_required', message: 'X-DB-Token header is required for data queries' });
    }

    // Token budget check (shared with PAP, per graph_type)
    const budget = await checkTokenBudget(tenantId, db);
    if (!budget.allowed) {
      return reply.code(429).send({ code: 'budget_exceeded', message: budget.reason });
    }

    const sessionId   = crypto.randomUUID();
    const promptHash  = crypto.createHash('sha256').update(body.data.prompt).digest('hex');

    // Create ai_pep_sessions record
    const client = await db.connect();
    try {
      await client.query(`SET LOCAL ROLE app_readwrite`);
      await client.query(`SELECT set_config('app.tenant_id', $1, true)`, [tenantId]);
      await client.query(
        `INSERT INTO ai_pep_sessions
           (id, tenant_id, user_id, data_source_id, idempotency_key, prompt, prompt_hash, status)
           VALUES ($1, $2, $3, $4, $5, $6, $7, 'running')
           ON CONFLICT (tenant_id, idempotency_key) DO NOTHING`,
        [sessionId, tenantId, userId, body.data.data_source_id, body.data.idempotency_key, body.data.prompt, promptHash],
      );

      // Idempotency: return cached session if key already used
      const existing = await client.query<{ id: string; status: string; sql_text: string | null }>(
        `SELECT id, status, sql_text FROM ai_pep_sessions
          WHERE tenant_id = $1 AND idempotency_key = $2 AND id != $3
          LIMIT 1`,
        [tenantId, body.data.idempotency_key, sessionId],
      );
      if (existing.rows.length > 0) {
        await client.query('COMMIT').catch(() => {});
        return reply.send({ type: 'cached', session_id: existing.rows[0]!.id, status: existing.rows[0]!.status });
      }
    } finally {
      client.release();
    }

    // SSE
    reply.raw.setHeader('Content-Type', 'text/event-stream');
    reply.raw.setHeader('Cache-Control', 'no-cache');
    reply.raw.setHeader('Connection', 'keep-alive');
    reply.raw.flushHeaders?.();

    function sse(data: unknown): void {
      reply.raw.write(`data: ${JSON.stringify(data)}\n\n`);
    }

    // Run PEP graph
    const finalState = await runPepGraph({
      initialState: {
        session_id:       sessionId,
        tenant_id:        tenantId,
        user_id:          userId,
        data_source_id:   body.data.data_source_id,
        idempotency_key:  body.data.idempotency_key,
        prompt:           body.data.prompt,
        prompt_hash:      promptHash,
      },
      db,
      redis: redis as RedisClientType | null,
      cfg,
      dbToken,
      onEvent: (event) => sse({ type: 'node', ...event }),
    });

    // Persist final session state
    const dbClient = await db.connect();
    try {
      await dbClient.query(`SET LOCAL ROLE app_readwrite`);
      await dbClient.query(`SELECT set_config('app.tenant_id', $1, true)`, [tenantId]);
      await dbClient.query(
        `UPDATE ai_pep_sessions SET
           status = $1, sql_text = $2, snapshot_hash = $3,
           ast_json = $4, rows_returned = $5, cost_usd = $6,
           tokens_in = $7, tokens_out = $8,
           validation_errors = $9, rejection_reason = $10,
           ended_at = NOW()
         WHERE id = $11`,
        [
          finalState.error ? 'error' : 'done',
          finalState.validated_sql ?? null,
          finalState.allowed_snapshot?.version ?? null,
          finalState.ast ? JSON.stringify(finalState.ast) : null,
          finalState.result?.total_rows ?? null,
          finalState.total_cost_usd,
          finalState.total_tokens_in,
          finalState.total_tokens_out,
          finalState.validation_errors ? JSON.stringify(finalState.validation_errors) : null,
          finalState.abort_reason ?? null,
          sessionId,
        ],
      );
    } finally {
      dbClient.release();
    }

    // Record token usage for budget tracking
    await recordTokenUsage({
      tenantId,
      tokensIn:  finalState.total_tokens_in,
      tokensOut: finalState.total_tokens_out,
      costUsd:   finalState.total_cost_usd,
      db,
    });

    sse({
      type: 'done',
      session_id:        sessionId,
      status:            finalState.error ? 'error' : 'done',
      validated_sql:     finalState.validated_sql,
      result:            finalState.result,
      explain_result:    finalState.explain_result,
      validation_errors: finalState.validation_errors,
      total_cost_usd:    finalState.total_cost_usd,
      error:             finalState.error,
    });
    reply.raw.end();
  });

  // ─── GET /v1/ai/pep/sessions/:id ─────────────────────────────────────────
  app.get('/v1/ai/pep/sessions/:id', async (req, reply) => {
    if (!featureGate()) return reply.code(404).send({ code: 'feature_disabled' });

    const tenantId = req.headers['x-tenant-id'] as string;
    if (!tenantId) return reply.code(401).send({ code: 'unauthenticated' });

    const { id } = req.params as { id: string };
    const client = await db.connect();
    try {
      await client.query(`SET LOCAL ROLE app_readonly`);
      await client.query(`SELECT set_config('app.tenant_id', $1, true)`, [tenantId]);
      const { rows } = await client.query(
        `SELECT id, status, prompt, sql_text, rows_returned, cost_usd, started_at, ended_at,
                validation_errors, rejection_reason, thumbs_up
           FROM ai_pep_sessions
          WHERE id = $1 AND tenant_id = $2`,
        [id, tenantId],
      );
      if (rows.length === 0) return reply.code(404).send({ code: 'not_found' });
      return reply.send(rows[0]);
    } finally {
      client.release();
    }
  });

  // ─── POST /v1/ai/pep/feedback ─────────────────────────────────────────────
  app.post('/v1/ai/pep/feedback', async (req, reply) => {
    if (!featureGate()) return reply.code(404).send({ code: 'feature_disabled' });

    const body = PepFeedbackRequestSchema.safeParse(req.body);
    if (!body.success) return reply.code(400).send({ code: 'invalid_request', errors: body.error.issues });

    const tenantId = req.headers['x-tenant-id'] as string;
    if (!tenantId) return reply.code(401).send({ code: 'unauthenticated' });

    const client = await db.connect();
    try {
      await client.query(`SET LOCAL ROLE app_readwrite`);
      await client.query(`SELECT set_config('app.tenant_id', $1, true)`, [tenantId]);
      const { rowCount } = await client.query(
        `UPDATE ai_pep_sessions SET thumbs_up = $1, feedback_comment = $2
          WHERE id = $3 AND tenant_id = $4 AND status = 'done'`,
        [body.data.thumbs_up, body.data.comment ?? null, body.data.session_id, tenantId],
      );
      if (rowCount === 0) return reply.code(404).send({ code: 'not_found' });

      pepFeedbackTotal.inc({ tenant_id: tenantId, sentiment: body.data.thumbs_up ? 'positive' : 'negative' });
      return reply.send({ ok: true });
    } finally {
      client.release();
    }
  });

  // ─── POST /v1/ai/pep/saved-questions ─────────────────────────────────────
  app.post('/v1/ai/pep/saved-questions', async (req, reply) => {
    if (!featureGate()) return reply.code(404).send({ code: 'feature_disabled' });

    const body = SaveQuestionRequestSchema.safeParse(req.body);
    if (!body.success) return reply.code(400).send({ code: 'invalid_request', errors: body.error.issues });

    const tenantId = req.headers['x-tenant-id'] as string;
    const userId   = req.headers['x-user-id']   as string;
    if (!tenantId || !userId) return reply.code(401).send({ code: 'unauthenticated' });

    const client = await db.connect();
    try {
      await client.query(`SET LOCAL ROLE app_readwrite`);
      await client.query(`SELECT set_config('app.tenant_id', $1, true)`, [tenantId]);

      // Fetch session to copy sql + snapshot info
      const { rows: sessions } = await client.query<{
        prompt: string; sql_text: string; snapshot_hash: string; data_source_id: string;
      }>(
        `SELECT prompt, sql_text, snapshot_hash, data_source_id
           FROM ai_pep_sessions
          WHERE id = $1 AND tenant_id = $2 AND status = 'done'`,
        [body.data.session_id, tenantId],
      );
      if (sessions.length === 0) {
        return reply.code(404).send({ code: 'not_found', message: 'Session not found or not complete' });
      }
      const s = sessions[0]!;
      if (!s.sql_text) {
        return reply.code(409).send({ code: 'no_sql', message: 'Session has no SQL to save' });
      }

      const { rows } = await client.query<{ id: string }>(
        `INSERT INTO saved_questions
           (tenant_id, user_id, data_source_id, name, description, prompt, sql_text, snapshot_hash)
           VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
           RETURNING id`,
        [tenantId, userId, s.data_source_id, body.data.name, body.data.description ?? null, s.prompt, s.sql_text, s.snapshot_hash ?? ''],
      );

      return reply.code(201).send({ id: rows[0]!.id, name: body.data.name });
    } finally {
      client.release();
    }
  });

  // ─── GET /v1/ai/pep/saved-questions ──────────────────────────────────────
  app.get('/v1/ai/pep/saved-questions', async (req, reply) => {
    if (!featureGate()) return reply.code(404).send({ code: 'feature_disabled' });

    const tenantId = req.headers['x-tenant-id'] as string;
    const userId   = req.headers['x-user-id']   as string;
    if (!tenantId) return reply.code(401).send({ code: 'unauthenticated' });

    const client = await db.connect();
    try {
      await client.query(`SET LOCAL ROLE app_readonly`);
      await client.query(`SELECT set_config('app.tenant_id', $1, true)`, [tenantId]);

      // Return own questions + published team questions
      const { rows } = await client.query(
        `SELECT id, name, description, prompt, data_source_id, run_count,
                last_run_at, is_published, created_at
           FROM saved_questions
          WHERE tenant_id = $1
            AND (user_id = $2 OR is_published = TRUE)
          ORDER BY last_run_at DESC NULLS LAST, created_at DESC
          LIMIT 100`,
        [tenantId, userId],
      );
      return reply.send({ items: rows });
    } finally {
      client.release();
    }
  });

  // ─── POST /v1/ai/pep/saved-questions/:id/run ─────────────────────────────
  // Re-runs a saved question using its cached SQL (skips LLM if snapshot unchanged).
  app.post('/v1/ai/pep/saved-questions/:id/run', async (req, reply) => {
    if (!featureGate()) return reply.code(404).send({ code: 'feature_disabled' });

    const tenantId = req.headers['x-tenant-id'] as string;
    const userId   = req.headers['x-user-id']   as string;
    const dbToken  = req.headers['x-db-token']  as string;

    if (!tenantId || !userId) return reply.code(401).send({ code: 'unauthenticated' });
    if (!dbToken) return reply.code(401).send({ code: 'db_token_required' });

    const { id } = req.params as { id: string };
    const client = await db.connect();
    try {
      await client.query(`SET LOCAL ROLE app_readonly`);
      await client.query(`SELECT set_config('app.tenant_id', $1, true)`, [tenantId]);

      const { rows } = await client.query<{
        id: string; name: string; prompt: string; sql_text: string;
        snapshot_hash: string; data_source_id: string;
      }>(
        `SELECT id, name, prompt, sql_text, snapshot_hash, data_source_id
           FROM saved_questions
          WHERE id = $1 AND tenant_id = $2
            AND (user_id = $3 OR is_published = TRUE)`,
        [id, tenantId, userId],
      );
      if (rows.length === 0) return reply.code(404).send({ code: 'not_found' });

      const q = rows[0]!;
      // Kick off a new PEP session using the saved SQL directly (no LLM)
      // by returning the session ID that the client can poll.
      const sessionId = crypto.randomUUID();
      const ikey = `saved:${id}:${Date.now()}`;

      // Update run stats
      await client.query('COMMIT').catch(() => {});
      const wClient = await db.connect();
      try {
        await wClient.query(`SET LOCAL ROLE app_readwrite`);
        await wClient.query(`SELECT set_config('app.tenant_id', $1, true)`, [tenantId]);
        await wClient.query(
          `UPDATE saved_questions SET run_count = run_count + 1, last_run_at = NOW() WHERE id = $1`,
          [id],
        );
        await wClient.query(
          `INSERT INTO ai_pep_sessions
             (id, tenant_id, user_id, data_source_id, idempotency_key, prompt, prompt_hash, sql_text, status)
             VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'running')`,
          [sessionId, tenantId, userId, q.data_source_id, ikey, q.prompt,
           crypto.createHash('sha256').update(q.prompt).digest('hex'), q.sql_text],
        );
      } finally {
        wClient.release();
      }

      // Return session ID; client streams via GET /sessions/:id or re-polls
      return reply.send({ session_id: sessionId, sql_text: q.sql_text, name: q.name });
    } finally {
      client.release();
    }
  });
}
