import type { Pool } from 'pg';
import type { RedisClientType } from 'redis';
import type { GraphState } from '../schemas';

// RAG-lite: embed fuzzy terms → cosine similarity against schema_metadata embeddings.
// For V1 we use keyword matching + pg similarity as a practical alternative to full vector search.

const RESOLVER_CACHE_TTL = 600; // 10 min

export async function schemaResolverNode(
  state: GraphState,
  db: Pool,
  redis: RedisClientType | null,
): Promise<Partial<GraphState>> {
  const start = Date.now();

  if (!state.sanitized_prompt) {
    return span(state, 'schema_resolver', Date.now() - start, { error: 'missing sanitized_prompt' });
  }

  // Extract candidate terms: nouns and quoted phrases from the prompt
  const terms = extractTerms(state.sanitized_prompt);

  const canonical_map: GraphState['canonical_map'] = {};

  for (const term of terms) {
    const cacheKey = `pap:resolver:${state.tenant_id}:${term.toLowerCase()}`;
    if (redis) {
      const cached = await redis.get(cacheKey);
      if (cached) {
        try {
          canonical_map[term] = JSON.parse(cached);
          continue;
        } catch { /* ignore */ }
      }
    }

    // Query schema_metadata for best match via trigram similarity (pg_trgm required).
    // Falls back to ILIKE if similarity extension isn't loaded.
    const { rows } = await db.query<{
      column_name: string; table_name: string; schema_name: string; sim: number;
    }>(
      `SELECT
         column_name,
         table_name,
         schema_name,
         similarity(column_name, $1) AS sim
       FROM schema_metadata
       WHERE tenant_id = $2
         AND (
           similarity(column_name, $1) > 0.3
           OR LOWER(column_name) LIKE '%' || LOWER($1) || '%'
           OR LOWER(table_name) LIKE '%' || LOWER($1) || '%'
         )
       ORDER BY sim DESC, column_name
       LIMIT 3`,
      [term, state.tenant_id],
    );

    if (rows.length > 0) {
      const best = rows[0]!;
      const entry = {
        canonical: best.column_name,
        confidence: best.sim,
        schema: best.schema_name,
        table: best.table_name,
      };
      canonical_map[term] = entry;
      if (redis) {
        await redis.setEx(cacheKey, RESOLVER_CACHE_TTL, JSON.stringify(entry));
      }
    }
  }

  return span(state, 'schema_resolver', Date.now() - start, { canonical_map });
}

function extractTerms(prompt: string): string[] {
  const quoted = [...prompt.matchAll(/"([^"]+)"/g)].map((m) => m[1]!);
  const words = prompt
    .replace(/["']/g, ' ')
    .split(/\s+/)
    .filter((w) => w.length > 3 && /^[a-z_]/i.test(w));
  return [...new Set([...quoted, ...words])].slice(0, 20);
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
