import { z } from 'zod';

export const PapNodeEventSchema = z.object({
  type: z.literal('node'),
  node: z.string(),
  status: z.enum(['running', 'done', 'error']),
  latency_ms: z.number().optional(),
  error: z.string().optional(),
  partial: z.object({
    intent: z.string().optional(),
    explanation: z.string().optional(),
    total_cost_usd: z.number().optional(),
  }).optional(),
});

export const PapDoneEventSchema = z.object({
  type: z.literal('done'),
  session_id: z.string().uuid(),
  status: z.string(),
  draft_policy: z.unknown().optional(),
  explanation: z.string().optional(),
  simulator_diff: z.unknown().optional(),
  validation_errors: z.array(z.string()).optional(),
  total_cost_usd: z.number().optional(),
  error: z.string().optional(),
});

export type PapNodeEvent = z.infer<typeof PapNodeEventSchema>;
export type PapDoneEvent = z.infer<typeof PapDoneEventSchema>;
export type PapEvent = { type: 'node' } & PapNodeEvent | PapDoneEvent;

export interface PapClientOptions {
  baseUrl: string;
  authToken: string;
  tenantId: string;
  userId: string;
}

export class PapClient {
  constructor(private readonly opts: PapClientOptions) {}

  async *draft(prompt: string, idempotencyKey: string): AsyncGenerator<PapEvent> {
    const response = await fetch(`${this.opts.baseUrl}/v1/ai/pap/draft`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        Authorization: `Bearer ${this.opts.authToken}`,
        'X-Tenant-Id': this.opts.tenantId,
        'X-User-Id': this.opts.userId,
      },
      body: JSON.stringify({ prompt, idempotency_key: idempotencyKey }),
    });

    if (!response.ok) {
      const err = await response.json().catch(() => ({ message: response.statusText }));
      throw new Error(`PAP draft failed ${response.status}: ${JSON.stringify(err)}`);
    }

    const contentType = response.headers.get('content-type') ?? '';
    if (!contentType.includes('text/event-stream')) {
      // Cached result
      const json = await response.json();
      yield json as PapDoneEvent;
      return;
    }

    const reader = response.body!.getReader();
    const decoder = new TextDecoder();
    let buffer = '';

    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      buffer += decoder.decode(value, { stream: true });
      const lines = buffer.split('\n');
      buffer = lines.pop() ?? '';
      for (const line of lines) {
        if (line.startsWith('data: ')) {
          const raw = line.slice(6);
          try {
            yield JSON.parse(raw) as PapEvent;
          } catch { /* ignore malformed lines */ }
        }
      }
    }
  }

  async approve(sessionId: string, action: 'approve' | 'reject', mfaToken: string, reason?: string) {
    const response = await fetch(`${this.opts.baseUrl}/v1/ai/pap/approve`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        Authorization: `Bearer ${this.opts.authToken}`,
        'X-Tenant-Id': this.opts.tenantId,
        'X-User-Id': this.opts.userId,
      },
      body: JSON.stringify({ session_id: sessionId, action, mfa_token: mfaToken, reason }),
    });
    if (!response.ok) throw new Error(`PAP approve failed ${response.status}`);
    return response.json();
  }
}
