import { PolicyDraftSchema, GraphStateSchema } from '../../graph/schemas';

describe('PolicyDraftSchema', () => {
  const validPolicy = {
    name: 'Finance read-only',
    tenant_id: '00000000-0000-0000-0000-000000000002',
    subject: { roles: ['finance-manager'] },
    rules: [
      {
        effect: 'allow',
        actions: ['select'],
        resource: { schema: 'public', table: 'payments' },
        column_masks: [{ column: 'bank_account_number', mask: 'redact' }],
        priority: 100,
      },
    ],
    version: 1,
  };

  it('validates a well-formed policy', () => {
    expect(() => PolicyDraftSchema.parse(validPolicy)).not.toThrow();
  });

  it('rejects missing tenant_id', () => {
    const bad = { ...validPolicy, tenant_id: 'not-a-uuid' };
    expect(() => PolicyDraftSchema.parse(bad)).toThrow();
  });

  it('rejects empty rules array', () => {
    const bad = { ...validPolicy, rules: [] };
    expect(() => PolicyDraftSchema.parse(bad)).toThrow();
  });

  it('rejects unknown mask type', () => {
    const bad = {
      ...validPolicy,
      rules: [{ ...validPolicy.rules[0], column_masks: [{ column: 'x', mask: 'encrypt' }] }],
    };
    expect(() => PolicyDraftSchema.parse(bad)).toThrow();
  });

  it('rejects too-deep conditions (depth > 5 caught by validator, not schema)', () => {
    // Schema allows deep nesting; validator enforces depth limit
    const deep = {
      type: 'and',
      children: [{ type: 'and', children: [{ type: 'comparison', field: 'x', op: 'eq', value: 1 }] }],
    };
    expect(() => PolicyDraftSchema.parse({ ...validPolicy, rules: [{ ...validPolicy.rules[0], conditions: deep }] }))
      .not.toThrow();
  });
});

describe('GraphStateSchema', () => {
  it('initializes with correct defaults', () => {
    const state = GraphStateSchema.parse({
      session_id: '00000000-0000-0000-0000-000000000001',
      tenant_id:  '00000000-0000-0000-0000-000000000002',
      user_id:    '00000000-0000-0000-0000-000000000003',
      idempotency_key: 'k',
      prompt: 'hello',
      prompt_hash: '',
    });
    expect(state.total_tokens_in).toBe(0);
    expect(state.node_spans).toEqual([]);
  });
});
