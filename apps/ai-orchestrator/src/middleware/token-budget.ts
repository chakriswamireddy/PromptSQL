import type { Pool } from 'pg';
import { tokenBudgetThrottlesTotal } from '../metrics';

const THROTTLE_PERCENT = 0.8;

export interface BudgetCheckResult {
  allowed: boolean;
  reason?: string;
  period: 'minute' | 'day';
}

export async function checkTokenBudget(
  tenantId: string,
  db: Pool,
): Promise<BudgetCheckResult> {
  const client = await db.connect();
  try {
    await client.query(`SET LOCAL ROLE app_readwrite`);
    await client.query(`SELECT set_config('app.tenant_id', $1, true)`, [tenantId]);

    for (const period of ['minute', 'day'] as const) {
      const start = period === 'minute'
        ? new Date(Math.floor(Date.now() / 60_000) * 60_000)
        : new Date(new Date().setHours(0, 0, 0, 0));

      const { rows } = await client.query<{
        tokens_used: string; tokens_limit: string;
        cost_used_usd: string; cost_limit_usd: string;
      }>(
        `INSERT INTO ai_token_budgets (tenant_id, budget_period, period_start)
           VALUES ($1, $2, $3)
           ON CONFLICT (tenant_id, budget_period, period_start) DO NOTHING;
         SELECT tokens_used, tokens_limit, cost_used_usd, cost_limit_usd
           FROM ai_token_budgets
          WHERE tenant_id = $1 AND budget_period = $2 AND period_start = $3`,
        [tenantId, period, start],
      );

      if (!rows[0]) continue;
      const { tokens_used, tokens_limit, cost_used_usd, cost_limit_usd } = rows[0];

      const tokenPct = Number(tokens_used) / Number(tokens_limit);
      const costPct  = Number(cost_used_usd) / Number(cost_limit_usd);

      if (tokenPct >= 1.0 || costPct >= 1.0) {
        tokenBudgetThrottlesTotal.inc({ tenant_id: tenantId, period });
        return { allowed: false, reason: `${period} budget exhausted`, period };
      }
      if (tokenPct >= THROTTLE_PERCENT || costPct >= THROTTLE_PERCENT) {
        tokenBudgetThrottlesTotal.inc({ tenant_id: tenantId, period });
        return { allowed: false, reason: `${period} budget at ${Math.round(Math.max(tokenPct, costPct) * 100)}%`, period };
      }
    }
    return { allowed: true, period: 'day' };
  } finally {
    client.release();
  }
}

export async function recordTokenUsage(opts: {
  tenantId: string;
  tokensIn: number;
  tokensOut: number;
  costUsd: number;
  db: Pool;
}): Promise<void> {
  const client = await opts.db.connect();
  try {
    await client.query(`SET LOCAL ROLE app_readwrite`);
    await client.query(`SELECT set_config('app.tenant_id', $1, true)`, [opts.tenantId]);

    for (const period of ['minute', 'day'] as const) {
      const start = period === 'minute'
        ? new Date(Math.floor(Date.now() / 60_000) * 60_000)
        : new Date(new Date().setHours(0, 0, 0, 0));

      await client.query(
        `INSERT INTO ai_token_budgets (tenant_id, budget_period, period_start, tokens_used, cost_used_usd)
           VALUES ($1, $2, $3, $4, $5)
           ON CONFLICT (tenant_id, budget_period, period_start)
           DO UPDATE SET
             tokens_used   = ai_token_budgets.tokens_used   + EXCLUDED.tokens_used,
             cost_used_usd = ai_token_budgets.cost_used_usd + EXCLUDED.cost_used_usd`,
        [opts.tenantId, period, start, opts.tokensIn + opts.tokensOut, opts.costUsd],
      );
    }
  } finally {
    client.release();
  }
}
