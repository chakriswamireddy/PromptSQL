import type { Pool } from 'pg';
import type { GraphState, PolicyDraft } from '../schemas';

interface SimulatorPersona {
  user_id: string;
  roles: string[];
  attributes: Record<string, string | number | boolean>;
}

interface SimulatorResult {
  allowed: boolean;
  masked_columns: string[];
  row_filter_applied: string | null;
  persona: SimulatorPersona;
}

export interface SimulatorDiff {
  personas_tested: number;
  allowed_count: number;
  denied_count: number;
  changes: Array<{
    persona_label: string;
    before: 'allowed' | 'denied';
    after: 'allowed' | 'denied';
    masked_columns: string[];
  }>;
  warnings: string[];
}

// Calls the PDP Simulator endpoint (Phase 4 gRPC stub) or falls back to local evaluation.
export async function simulatorNode(state: GraphState, db: Pool): Promise<Partial<GraphState>> {
  const start = Date.now();

  if (!state.draft_policy) {
    return span(state, 'simulator', Date.now() - start, { error: 'no draft_policy to simulate' });
  }

  let diff: SimulatorDiff;
  try {
    diff = await runSimulation(state.draft_policy, state.tenant_id, db);
  } catch (err) {
    // Simulator timeout / unavailable → surface partial results with warning
    diff = {
      personas_tested: 0,
      allowed_count: 0,
      denied_count: 0,
      changes: [],
      warnings: [`Simulator unavailable: ${String(err)}`],
    };
  }

  return span(state, 'simulator', Date.now() - start, { simulator_diff: diff });
}

async function runSimulation(policy: PolicyDraft, tenantId: string, db: Pool): Promise<SimulatorDiff> {
  const personas = await fetchSyntheticPersonas(tenantId, db);
  const changes: SimulatorDiff['changes'] = [];
  const warnings: string[] = [];
  let allowed = 0;
  let denied = 0;

  for (const persona of personas) {
    const result = evaluatePolicy(policy, persona);
    if (result.allowed) allowed++; else denied++;

    changes.push({
      persona_label: `${persona.roles.join(',') || 'no-role'}@${tenantId.slice(0, 8)}`,
      before: 'denied',  // baseline: no policy = deny
      after: result.allowed ? 'allowed' : 'denied',
      masked_columns: result.masked_columns,
    });
  }

  if (policy.rules.some((r) => r.effect === 'allow' && r.conditions === undefined)) {
    warnings.push('Unconditional allow rule: all matching personas will receive access');
  }

  return {
    personas_tested: personas.length,
    allowed_count: allowed,
    denied_count: denied,
    changes,
    warnings,
  };
}

async function fetchSyntheticPersonas(tenantId: string, db: Pool): Promise<SimulatorPersona[]> {
  // Fetch up to 5 real users from the tenant for simulation
  const { rows } = await db.query<{ user_id: string; roles: string[] }>(
    `SELECT u.id AS user_id,
            COALESCE(array_agg(r.name) FILTER (WHERE r.name IS NOT NULL), '{}') AS roles
       FROM users u
       LEFT JOIN user_roles ur ON ur.user_id = u.id
       LEFT JOIN roles r ON r.id = ur.role_id AND r.tenant_id = $1
      WHERE u.tenant_id = $1
      GROUP BY u.id
      LIMIT 5`,
    [tenantId],
  );

  return rows.map((r) => ({
    user_id: r.user_id,
    roles: r.roles,
    attributes: {},
  }));
}

function evaluatePolicy(policy: PolicyDraft, persona: SimulatorPersona): SimulatorResult {
  // Simplified local evaluation: check if persona's roles intersect policy subject roles
  const subjectRoles = policy.subject.roles ?? [];
  const personaRoles = new Set(persona.roles);

  const roleMatch = subjectRoles.length === 0 || subjectRoles.some((r) => personaRoles.has(r));

  const allowRules = policy.rules.filter((r) => r.effect === 'allow');
  const denyRules  = policy.rules.filter((r) => r.effect === 'deny');

  const hasDeny = denyRules.some(() => roleMatch);  // simplified: check presence
  const hasAllow = allowRules.length > 0 && roleMatch;

  const allowed = hasAllow && !hasDeny;

  const maskedColumns = allowed
    ? allowRules.flatMap((r) => (r.column_masks ?? []).map((m) => m.column))
    : [];

  const rowFilter = allowed
    ? (allowRules.find((r) => r.row_filter)?.row_filter ?? null)
    : null;

  return { allowed, masked_columns: maskedColumns, row_filter_applied: rowFilter, persona };
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
