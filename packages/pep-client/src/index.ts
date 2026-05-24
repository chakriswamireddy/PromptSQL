import { z } from 'zod';

// ─── Event types ─────────────────────────────────────────────────────────────

export const PepNodeEventSchema = z.object({
  type: z.literal('node'),
  node: z.string(),
  status: z.enum(['running', 'done', 'error']),
  latency_ms: z.number().optional(),
  error: z.string().optional(),
  partial: z.object({
    validated_sql:  z.string().optional(),
    total_cost_usd: z.number().optional(),
    explain_result: z.unknown().optional(),
  }).optional(),
});
export type PepNodeEvent = z.infer<typeof PepNodeEventSchema>;

export const PepResultColumnSchema = z.object({
  name:   z.string(),
  type:   z.string().optional(),
  masked: z.boolean().default(false),
});

export const PepResultSchema = z.object({
  columns:    z.array(PepResultColumnSchema),
  rows:       z.array(z.record(z.unknown())),
  total_rows: z.number(),
  truncated:  z.boolean(),
  citation: z.object({
    snapshot_version:  z.string(),
    tables_accessed:   z.array(z.string()),
    provider:          z.string().optional(),
    model:             z.string().optional(),
  }),
});
export type PepResult = z.infer<typeof PepResultSchema>;

export const PepDoneEventSchema = z.object({
  type:              z.literal('done'),
  session_id:        z.string().uuid(),
  status:            z.enum(['done', 'error', 'cached']),
  validated_sql:     z.string().optional(),
  result:            PepResultSchema.optional(),
  validation_errors: z.array(z.object({ code: z.string(), message: z.string(), hint: z.string().optional() })).optional(),
  explain_result:    z.object({ total_cost: z.number(), plan_rows: z.number(), rejected: z.boolean() }).optional(),
  total_cost_usd:    z.number().optional(),
  error:             z.string().optional(),
});
export type PepDoneEvent = z.infer<typeof PepDoneEventSchema>;

export const PepCachedEventSchema = z.object({
  type:       z.literal('cached'),
  session_id: z.string().uuid(),
  status:     z.string(),
});

export type PepEvent = PepNodeEvent | PepDoneEvent | z.infer<typeof PepCachedEventSchema>;

// ─── Saved question ───────────────────────────────────────────────────────────

export interface SavedQuestion {
  id: string;
  name: string;
  description?: string;
  prompt: string;
  data_source_id: string;
  run_count: number;
  last_run_at?: string;
  is_published: boolean;
  created_at: string;
}

// ─── PepClient ───────────────────────────────────────────────────────────────

export class PepClient {
  constructor(
    private readonly baseUrl: string,
    private readonly authToken: string,
    private readonly tenantId: string,
    private readonly userId: string,
    private readonly dbToken: string,
  ) {}

  /**
   * Ask a natural-language question about a data source.
   * Returns an async generator of PepEvents; last event is always type='done'.
   */
  async *ask(opts: {
    prompt: string;
    dataSourceId: string;
    idempotencyKey: string;
  }): AsyncGenerator<PepEvent> {
    const response = await fetch(`${this.baseUrl}/v1/ai/pep/ask`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'Authorization': `Bearer ${this.authToken}`,
        'X-Tenant-Id': this.tenantId,
        'X-User-Id': this.userId,
        'X-DB-Token': this.dbToken,
      },
      body: JSON.stringify({
        prompt:         opts.prompt,
        data_source_id: opts.dataSourceId,
        idempotency_key: opts.idempotencyKey,
      }),
    });

    if (!response.ok) {
      const err = await response.text();
      throw new Error(`PEP /ask failed ${response.status}: ${err}`);
    }

    // Handle non-SSE (cached) response
    const ct = response.headers.get('content-type') ?? '';
    if (!ct.includes('text/event-stream')) {
      const json = await response.json();
      yield json as PepEvent;
      return;
    }

    // Parse SSE stream
    const reader = response.body!.getReader();
    const decoder = new TextDecoder();
    let buffer = '';

    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      buffer += decoder.decode(value, { stream: true });
      const parts = buffer.split('\n\n');
      buffer = parts.pop() ?? '';
      for (const part of parts) {
        const line = part.replace(/^data: /, '').trim();
        if (!line) continue;
        try {
          yield JSON.parse(line) as PepEvent;
        } catch { /* skip malformed */ }
      }
    }
  }

  /** Submit thumbs-up / thumbs-down feedback for a session. */
  async submitFeedback(opts: {
    sessionId: string;
    thumbsUp: boolean;
    comment?: string;
  }): Promise<void> {
    const resp = await fetch(`${this.baseUrl}/v1/ai/pep/feedback`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'Authorization': `Bearer ${this.authToken}`,
        'X-Tenant-Id': this.tenantId,
        'X-User-Id': this.userId,
      },
      body: JSON.stringify({ session_id: opts.sessionId, thumbs_up: opts.thumbsUp, comment: opts.comment }),
    });
    if (!resp.ok) throw new Error(`feedback failed: ${resp.status}`);
  }

  /** Save a successful query as a named question. */
  async saveQuestion(opts: {
    sessionId: string;
    name: string;
    description?: string;
  }): Promise<{ id: string; name: string }> {
    const resp = await fetch(`${this.baseUrl}/v1/ai/pep/saved-questions`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'Authorization': `Bearer ${this.authToken}`,
        'X-Tenant-Id': this.tenantId,
        'X-User-Id': this.userId,
      },
      body: JSON.stringify({ session_id: opts.sessionId, name: opts.name, description: opts.description }),
    });
    if (!resp.ok) throw new Error(`saveQuestion failed: ${resp.status}`);
    return resp.json();
  }

  /** List saved questions (own + published). */
  async listSavedQuestions(): Promise<SavedQuestion[]> {
    const resp = await fetch(`${this.baseUrl}/v1/ai/pep/saved-questions`, {
      headers: {
        'Authorization': `Bearer ${this.authToken}`,
        'X-Tenant-Id': this.tenantId,
        'X-User-Id': this.userId,
      },
    });
    if (!resp.ok) throw new Error(`listSavedQuestions failed: ${resp.status}`);
    const body = await resp.json();
    return body.items;
  }

  /** Run a saved question (re-uses cached SQL if snapshot unchanged). */
  async runSavedQuestion(savedQuestionId: string): Promise<{ session_id: string; sql_text: string }> {
    const resp = await fetch(`${this.baseUrl}/v1/ai/pep/saved-questions/${savedQuestionId}/run`, {
      method: 'POST',
      headers: {
        'Authorization': `Bearer ${this.authToken}`,
        'X-Tenant-Id': this.tenantId,
        'X-User-Id': this.userId,
        'X-DB-Token': this.dbToken,
      },
      body: JSON.stringify({}),
    });
    if (!resp.ok) throw new Error(`runSavedQuestion failed: ${resp.status}`);
    return resp.json();
  }
}
