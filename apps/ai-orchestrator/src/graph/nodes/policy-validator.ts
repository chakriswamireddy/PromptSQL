import type { Pool } from 'pg';
import type { GraphState } from '../schemas';
import { validationFailuresTotal } from '../../metrics';

// Deterministic validator — no LLM involved.
// Checks: schema existence, role existence, privilege escalation, cross-tenant, deny conflicts.

export async function policyValidatorNode(state: GraphState, db: Pool): Promise<Partial<GraphState>> {
  const start = Date.now();

  if (!state.draft_policy) {
    return span(state, 'policy_validator', Date.now() - start, { error: 'no draft_policy to validate' });
  }

  const errors: string[] = [];
  const policy = state.draft_policy;

  // 1. Tenant containment
  if (policy.tenant_id !== state.tenant_id) {
    errors.push(`cross_tenant: policy.tenant_id ${policy.tenant_id} !== session ${state.tenant_id}`);
    validationFailuresTotal.inc({ reason: 'cross_tenant' });
  }

  // 2. All referenced tables/columns exist in schema_metadata
  const tableSet = new Set<string>();
  for (const rule of policy.rules) {
    tableSet.add(`${rule.resource.schema}.${rule.resource.table}`);
  }
  for (const fqTable of tableSet) {
    const [schema, table] = fqTable.split('.');
    const { rows } = await db.query(
      `SELECT 1 FROM schema_metadata
        WHERE tenant_id = $1 AND schema_name = $2 AND table_name = $3
        LIMIT 1`,
      [state.tenant_id, schema, table],
    );
    if (rows.length === 0) {
      errors.push(`unknown_table: ${fqTable}`);
      validationFailuresTotal.inc({ reason: 'unknown_table' });
    }
  }

  // 3. Referenced roles exist (if any)
  const referencedRoles = policy.subject.roles ?? [];
  for (const role of referencedRoles) {
    const { rows } = await db.query(
      `SELECT 1 FROM roles WHERE tenant_id = $1 AND name = $2 LIMIT 1`,
      [state.tenant_id, role],
    );
    if (rows.length === 0) {
      errors.push(`unknown_role: ${role}`);
      validationFailuresTotal.inc({ reason: 'unknown_role' });
    }
  }

  // 4. Privilege escalation guard — drafter cannot grant super-admin permissions
  const adminRoles = await getAdminRoles(state.user_id, state.tenant_id, db);
  if (!adminRoles.includes('super-admin')) {
    for (const rule of policy.rules) {
      if (rule.effect === 'allow' && rule.actions.includes('insert') && rule.actions.includes('delete')) {
        // Warn but not hard block — surface as warning
        errors.push('privilege_escalation_warning: full write+delete grant requires super-admin review');
        validationFailuresTotal.inc({ reason: 'privilege_escalation_warning' });
      }
    }
  }

  // 5. Check for conflict with active deny rules
  const denyCount = await checkActiveDenyConflicts(policy, state.tenant_id, db);
  if (denyCount > 0) {
    errors.push(
      `active_deny_conflict: ${denyCount} active deny rule(s) would override this allow — review carefully`,
    );
    validationFailuresTotal.inc({ reason: 'deny_conflict' });
  }

  // 6. DSL depth/node check
  for (const rule of policy.rules) {
    if (rule.conditions) {
      const depth = measureDepth(rule.conditions);
      if (depth > 5) {
        errors.push(`dsl_depth: rule "${rule.id ?? 'unnamed'}" condition depth ${depth} exceeds max 5`);
        validationFailuresTotal.inc({ reason: 'dsl_depth' });
      }
    }
  }

  const hardErrors = errors.filter((e) => !e.includes('_warning'));
  if (hardErrors.length > 0) {
    return span(state, 'policy_validator', Date.now() - start, {
      validation_errors: errors,
      error: `Validation failed: ${hardErrors.join('; ')}`,
    });
  }

  return span(state, 'policy_validator', Date.now() - start, {
    validation_errors: errors.length > 0 ? errors : undefined,
  });
}

async function getAdminRoles(userId: string, tenantId: string, db: Pool): Promise<string[]> {
  const { rows } = await db.query<{ name: string }>(
    `SELECT r.name FROM roles r
       JOIN user_roles ur ON ur.role_id = r.id
      WHERE ur.user_id = $1 AND r.tenant_id = $2`,
    [userId, tenantId],
  );
  return rows.map((r) => r.name);
}

async function checkActiveDenyConflicts(
  policy: GraphState['draft_policy'],
  tenantId: string,
  db: Pool,
): Promise<number> {
  if (!policy) return 0;
  let count = 0;
  for (const rule of policy.rules) {
    if (rule.effect !== 'allow') continue;
    const { rows } = await db.query<{ cnt: string }>(
      `SELECT COUNT(*) AS cnt FROM policy_rules pr
         JOIN policies p ON p.id = pr.policy_id
        WHERE p.tenant_id = $1
          AND p.status = 'active'
          AND pr.effect = 'deny'
          AND pr.resource_schema = $2
          AND pr.resource_table = $3`,
      [tenantId, rule.resource.schema, rule.resource.table],
    );
    count += parseInt(rows[0]?.cnt ?? '0', 10);
  }
  return count;
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any
function measureDepth(node: any, depth = 0): number {
  if (!node || typeof node !== 'object') return depth;
  if (node.children) {
    return Math.max(...(node.children as unknown[]).map((c) => measureDepth(c, depth + 1)));
  }
  if (node.child) return measureDepth(node.child, depth + 1);
  return depth;
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
