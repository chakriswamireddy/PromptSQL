import { Pool, type PoolClient } from 'pg';
import type { Config } from '../../../config';
import type { PepGraphState } from '../../pep-schemas';
import { pepRowsStreamedTotal } from '../../../metrics';

// Proxy Executor: submits the validated SQL to the Phase 6 PG proxy.
// The proxy enforces row filters and column masks per PDP decision.
// The orchestrator never bypasses the proxy — it submits as the user.
//
// Connection: uses a short-lived pool keyed by (tenantId, dataSourceId).
// Credentials: db-token issued by api-gateway (user identity embedded).

const MAX_RESULT_ROWS = 10_000;

// Per-(tenantId, dataSourceId) connection pool to the proxy.
// In production, limit pool size to avoid exhausting proxy connections.
const proxyPools = new Map<string, Pool>();

function getProxyPool(cfg: Config, dataSourceId: string, dbToken: string, userId: string): Pool {
  const key = `${dataSourceId}:${userId}`;
  if (!proxyPools.has(key)) {
    const pool = new Pool({
      host: cfg.PROXY_HOST,
      port: cfg.PROXY_PORT,
      user: userId,
      password: dbToken,
      database: dataSourceId,  // proxy uses DB name field to identify the data source
      max: 3,
      idleTimeoutMillis: 30_000,
      connectionTimeoutMillis: 8_000,
      ssl: false,  // internal network; TLS terminated at service mesh
    });
    // Clean up pool on error
    pool.on('error', () => {
      proxyPools.delete(key);
    });
    proxyPools.set(key, pool);
  }
  return proxyPools.get(key)!;
}

export async function pepProxyExecutorNode(
  state: PepGraphState,
  cfg: Config,
  dbToken: string,
): Promise<Partial<PepGraphState>> {
  const start = Date.now();

  if (!state.validated_sql) {
    return span(state, start, { error: 'No validated SQL for execution', abort_reason: 'executor_no_sql' });
  }

  const pool = getProxyPool(cfg, state.data_source_id, dbToken, state.user_id);
  let client: PoolClient | null = null;

  try {
    client = await pool.connect();

    // Set session context so proxy + managed DB know who is executing
    await client.query(`SET LOCAL app.tenant_id = '${state.tenant_id}'`);
    await client.query(`SET LOCAL app.user_id   = '${state.user_id}'`);
    await client.query(`SET LOCAL app.session_id = '${state.session_id}'`);

    // Cap: if validated_sql already has a LIMIT (it must by validator), the DB enforces it.
    // Add a safety backstop at the driver level.
    const { rows, fields, rowCount } = await client.query({
      text: state.validated_sql,
      rowMode: 'array',  // faster, we rehydrate below
    });

    const columnNames = fields.map((f) => f.name);
    const dataRows = rows.slice(0, MAX_RESULT_ROWS).map((r: unknown[]) =>
      Object.fromEntries(columnNames.map((col, i) => [col, r[i]]))
    );
    const truncated = (rowCount ?? rows.length) > MAX_RESULT_ROWS;

    pepRowsStreamedTotal.inc({ tenant_id: state.tenant_id }, dataRows.length);

    return {
      ...span(state, start, {}),
      result: {
        columns: fields.map((f) => ({ name: f.name, type: String(f.dataTypeID) })),
        rows: dataRows,
        total_rows: rowCount ?? rows.length,
        truncated,
        citation: {
          snapshot_version: state.allowed_snapshot?.version ?? 'unknown',
          policy_set_version: state.allowed_snapshot?.policy_set_version,
          tables_accessed: extractTableNames(state.validated_sql),
        },
      },
    };
  } catch (err) {
    const msg = String(err);
    // Generic error to caller — do not expose DB internals
    return span(state, start, {
      error: 'Query execution failed. Check that your question matches the available data.',
      abort_reason: 'executor_query_failed',
      node_spans: [
        ...(state.node_spans ?? []),
        { node: 'pep_proxy_executor', latency_ms: Date.now() - start, error: msg },
      ],
    });
  } finally {
    client?.release();
  }
}

function extractTableNames(sql: string): string[] {
  // Simple regex for citation purposes only — not used for security decisions
  const matches = sql.matchAll(/FROM\s+([\w.]+)|JOIN\s+([\w.]+)/gi);
  return [...new Set([...matches].map((m) => m[1] ?? m[2]).filter(Boolean))];
}

function span(state: PepGraphState, start: number, patch: Partial<PepGraphState>): Partial<PepGraphState> {
  return {
    ...patch,
    node_spans: [
      ...(state.node_spans ?? []),
      { node: 'pep_proxy_executor', latency_ms: Date.now() - start, error: patch.error },
    ],
  };
}
