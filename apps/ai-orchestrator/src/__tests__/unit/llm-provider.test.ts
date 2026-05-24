import { PolicyDraftSchema } from '../../graph/schemas';

// Verify the Zod backstop catches malformed LLM output before it propagates.
describe('PolicyDraftSchema Zod backstop', () => {
  it('rejects free-text output (never parsed as policy)', () => {
    const freeText = 'Sure! I would be happy to create a policy for you.';
    expect(() => PolicyDraftSchema.parse(freeText)).toThrow();
  });

  it('rejects partial JSON (missing required fields)', () => {
    const partial = { name: 'My Policy' };
    expect(() => PolicyDraftSchema.parse(partial)).toThrow();
  });

  it('rejects policy with cross-tenant UUID format error', () => {
    const bad = {
      name: 'p',
      tenant_id: 'not-a-uuid',
      subject: {},
      rules: [{ effect: 'allow', actions: ['select'], resource: { schema: 's', table: 't' }, priority: 100 }],
      version: 1,
    };
    expect(() => PolicyDraftSchema.parse(bad)).toThrow();
  });

  it('accepts minimal valid policy', () => {
    const minimal = {
      name: 'minimal',
      tenant_id: '11111111-1111-1111-1111-111111111111',
      subject: {},
      rules: [{ effect: 'allow', actions: ['select'], resource: { schema: 'public', table: 'orders' }, priority: 100 }],
      version: 1,
    };
    expect(() => PolicyDraftSchema.parse(minimal)).not.toThrow();
  });
});
