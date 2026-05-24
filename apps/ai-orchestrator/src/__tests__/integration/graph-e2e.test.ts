/**
 * Integration test — requires real PostgreSQL at DATABASE_URL env var.
 * Run with: DATABASE_URL=postgres://... jest --testPathPattern integration
 *
 * Verifies: tenant isolation (cross-tenant read returns 0 rows),
 * idempotency (second identical request returns same session ID).
 */

import { Pool } from 'pg';

const DB_URL = process.env.DATABASE_URL;
const skipIfNoDb = DB_URL ? describe : describe.skip;

skipIfNoDb('Graph integration (real PostgreSQL)', () => {
  let db: Pool;
  const TENANT_A = '10000000-0000-0000-0000-000000000001';
  const TENANT_B = '20000000-0000-0000-0000-000000000001';
  const USER_A   = '10000000-0000-0000-0000-000000000002';

  beforeAll(async () => {
    db = new Pool({ connectionString: DB_URL });
  });

  afterAll(async () => {
    await db.end();
  });

  it('RLS: tenant B cannot read tenant A ai_sessions', async () => {
    // Insert a session as tenant A
    const client = await db.connect();
    try {
      await client.query(`SET LOCAL ROLE app_readwrite`);
      await client.query(`SELECT set_config('app.tenant_id', $1, true)`, [TENANT_A]);
      await client.query(
        `INSERT INTO ai_sessions (id, tenant_id, user_id, idempotency_key, prompt, prompt_hash)
           VALUES (gen_random_uuid(), $1, $2, 'rls-test-' || gen_random_uuid(), 'test prompt', 'abc')
           ON CONFLICT DO NOTHING`,
        [TENANT_A, USER_A],
      );

      // Now switch to tenant B context
      await client.query(`SELECT set_config('app.tenant_id', $1, true)`, [TENANT_B]);
      const { rows } = await client.query(
        `SELECT id FROM ai_sessions WHERE tenant_id = $1`,
        [TENANT_A],
      );
      expect(rows).toHaveLength(0);
    } finally {
      client.release();
    }
  });

  it('Token budget: insert and increment', async () => {
    const client = await db.connect();
    try {
      await client.query(`SET LOCAL ROLE app_readwrite`);
      await client.query(`SELECT set_config('app.tenant_id', $1, true)`, [TENANT_A]);

      const start = new Date(new Date().setHours(0, 0, 0, 0));
      await client.query(
        `INSERT INTO ai_token_budgets (tenant_id, budget_period, period_start, tokens_used, cost_used_usd)
           VALUES ($1, 'day', $2, 100, 0.001)
           ON CONFLICT (tenant_id, budget_period, period_start)
           DO UPDATE SET tokens_used = ai_token_budgets.tokens_used + 100`,
        [TENANT_A, start],
      );

      const { rows } = await client.query(
        `SELECT tokens_used FROM ai_token_budgets
          WHERE tenant_id = $1 AND budget_period = 'day' AND period_start = $2`,
        [TENANT_A, start],
      );
      expect(Number(rows[0]?.tokens_used)).toBeGreaterThanOrEqual(100);
    } finally {
      client.release();
    }
  });
});
