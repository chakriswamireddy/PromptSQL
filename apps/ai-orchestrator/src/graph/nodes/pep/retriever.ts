import type { Pool } from 'pg';
import type { RedisClientType } from 'redis';
import type { PepGraphState, RelevantTableSchema } from '../../pep-schemas';
import type { z } from 'zod';

type RelevantTable = z.infer<typeof RelevantTableSchema>;

// Retriever: rank the snapshot tables by relevance to the user's prompt.
// Uses PG trigram similarity over table/column names and descriptions.
// Returns top-K tables ordered by relevance score.

const TOP_K = 8;

export async function pepRetrieverNode(
  state: PepGraphState,
  db: Pool,
  redis: RedisClientType | null,
): Promise<Partial<PepGraphState>> {
  const start = Date.now();

  if (!state.allowed_snapshot || state.allowed_snapshot.tables.length === 0) {
    return span(state, start, { error: 'No snapshot to retrieve from', abort_reason: 'retriever_no_snapshot' });
  }

  const prompt = state.sanitized_prompt ?? state.prompt;

  // If the snapshot is small enough, return all tables (no need for vector search).
  if (state.allowed_snapshot.tables.length <= TOP_K) {
    const relevant: RelevantTable[] = state.allowed_snapshot.tables.map((t) => ({
      table: t,
      relevance_score: 1.0,
    }));
    return span(state, start, { relevant_tables: relevant });
  }

  // Semantic ranking via trigram similarity in Postgres.
  // We query schema_metadata for tables that appear in the snapshot,
  // ranked by similarity of (table_name + column_names + description) to the prompt.
  const allowedTableNames = state.allowed_snapshot.tables.map(
    (t) => `${t.schema}.${t.name}`
  );

  const cacheKey = `pep:retriever:${state.tenant_id}:${state.data_source_id}:${
    Buffer.from(prompt).toString('base64').slice(0, 32)
  }`;

  if (redis) {
    try {
      const cached = await redis.get(cacheKey);
      if (cached) {
        return span(state, start, { relevant_tables: JSON.parse(cached) });
      }
    } catch { /* ignore */ }
  }

  let client;
  try {
    client = await db.connect();
    await client.query(`SET LOCAL ROLE app_readonly`);
    await client.query(`SELECT set_config('app.tenant_id', $1, true)`, [state.tenant_id]);

    const { rows } = await client.query<{
      table_schema: string;
      table_name: string;
      similarity: number;
    }>(
      `SELECT table_schema, table_name,
              similarity(
                table_name || ' ' || COALESCE(description, '') || ' ' ||
                string_agg(column_name, ' '),
                $1
              ) AS similarity
         FROM schema_metadata
        WHERE data_source_id = $2
          AND quarantine = FALSE
          AND (table_schema || '.' || table_name) = ANY($3::text[])
        GROUP BY table_schema, table_name, description
        ORDER BY similarity DESC
        LIMIT $4`,
      [prompt, state.data_source_id, allowedTableNames, TOP_K],
    );

    const snapshotByKey = new Map(
      state.allowed_snapshot.tables.map((t) => [`${t.schema}.${t.name}`, t])
    );

    const relevant: RelevantTable[] = rows
      .map((r) => {
        const key = `${r.table_schema}.${r.table_name}`;
        const tableSnap = snapshotByKey.get(key);
        if (!tableSnap) return null;
        return { table: tableSnap, relevance_score: Number(r.similarity), reason: undefined };
      })
      .filter((x): x is RelevantTable => x !== null);

    // Add any tables not returned by similarity (score 0) to ensure completeness up to TOP_K
    if (relevant.length < TOP_K) {
      const seen = new Set(relevant.map((r) => `${r.table.schema}.${r.table.name}`));
      for (const t of state.allowed_snapshot.tables) {
        if (!seen.has(`${t.schema}.${t.name}`)) {
          relevant.push({ table: t, relevance_score: 0 });
          if (relevant.length >= TOP_K) break;
        }
      }
    }

    if (redis) {
      redis.setEx(cacheKey, 60, JSON.stringify(relevant)).catch(() => {});
    }

    return span(state, start, { relevant_tables: relevant });
  } catch (err) {
    // Fallback: return all snapshot tables unranked
    const fallback: RelevantTable[] = state.allowed_snapshot.tables
      .slice(0, TOP_K)
      .map((t) => ({ table: t, relevance_score: 0.5 }));
    return span(state, start, {
      relevant_tables: fallback,
      node_spans: [
        ...(state.node_spans ?? []),
        { node: 'pep_retriever', latency_ms: Date.now() - start, error: `DB error (fallback used): ${String(err)}` },
      ],
    });
  } finally {
    client?.release();
  }
}

function span(state: PepGraphState, start: number, patch: Partial<PepGraphState>): Partial<PepGraphState> {
  return {
    ...patch,
    node_spans: [
      ...(state.node_spans ?? []),
      { node: 'pep_retriever', latency_ms: Date.now() - start, error: patch.error },
    ],
  };
}
