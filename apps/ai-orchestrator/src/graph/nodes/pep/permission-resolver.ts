import type { RedisClientType } from 'redis';
import type { Config } from '../../../config';
import type { PepGraphState } from '../../pep-schemas';
import { AllowedSnapshotSchema } from '../../pep-schemas';

// Permission Resolver: fetch the AllowedSnapshot from retrieval-service.
// This defines exactly what tables and columns the user may query.
// Cache by (userId, dataSourceId, snapshotVersion) for 5 min.

const CACHE_TTL_SECONDS = 300;

export async function pepPermissionResolverNode(
  state: PepGraphState,
  cfg: Config,
  redis: RedisClientType | null,
): Promise<Partial<PepGraphState>> {
  const start = Date.now();

  const cacheKey = `pep:snapshot:${state.tenant_id}:${state.user_id}:${state.data_source_id}`;

  // Try cache first
  if (redis) {
    try {
      const cached = await redis.get(cacheKey);
      if (cached) {
        const parsed = AllowedSnapshotSchema.safeParse(JSON.parse(cached));
        if (parsed.success) {
          return span(state, start, { allowed_snapshot: parsed.data });
        }
      }
    } catch {
      // Cache miss or parse error — proceed to live call
    }
  }

  // Call retrieval-service /snapshot
  let response: Response;
  try {
    response = await fetch(`${cfg.RETRIEVAL_SERVICE_ADDR}/v1/snapshot`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'X-Tenant-ID': state.tenant_id,
        'X-User-ID': state.user_id,
      },
      body: JSON.stringify({ data_source_id: state.data_source_id }),
      signal: AbortSignal.timeout(10_000),
    });
  } catch (err) {
    return span(state, start, {
      error: `Retrieval service unreachable: ${String(err)}`,
      abort_reason: 'retrieval_service_unavailable',
    });
  }

  if (!response.ok) {
    return span(state, start, {
      error: `Retrieval service returned ${response.status}`,
      abort_reason: 'snapshot_fetch_failed',
    });
  }

  const raw = await response.json();
  const parsed = AllowedSnapshotSchema.safeParse(raw);
  if (!parsed.success) {
    return span(state, start, {
      error: 'AllowedSnapshot response did not match schema',
      abort_reason: 'snapshot_schema_error',
    });
  }

  const snapshot = parsed.data;

  if (snapshot.tables.length === 0) {
    return span(state, start, {
      allowed_snapshot: snapshot,
      error: 'No accessible tables for this user and data source',
      abort_reason: 'no_accessible_tables',
    });
  }

  // Write to cache
  if (redis) {
    redis.setEx(cacheKey, CACHE_TTL_SECONDS, JSON.stringify(snapshot)).catch(() => {});
  }

  return span(state, start, { allowed_snapshot: snapshot });
}

function span(state: PepGraphState, start: number, patch: Partial<PepGraphState>): Partial<PepGraphState> {
  return {
    ...patch,
    node_spans: [
      ...(state.node_spans ?? []),
      { node: 'pep_permission_resolver', latency_ms: Date.now() - start, error: patch.error },
    ],
  };
}
