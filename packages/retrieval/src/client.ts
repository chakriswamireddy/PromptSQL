import {
  AllowedSnapshot,
  AllowedSnapshotSchema,
  DocsRequest,
  DocsResponse,
  DocsResponseSchema,
  ExplainRequest,
  ProviderRoute,
  ProviderRouteSchema,
  RouteRequest,
  SessionHeaders,
  SnapshotRequest,
} from './types';

export class RetrievalError extends Error {
  constructor(public readonly code: string, message: string) {
    super(message);
    this.name = 'RetrievalError';
  }
}

export class NoPrivateProviderError extends RetrievalError {
  constructor() {
    super('no_private_provider', 'No private provider configured for restricted content');
  }
}

// RetrievalClient is the HTTP SDK for the retrieval-service.
// Used by the admin-console Inspector page and the ai-orchestrator Python service
// (via language-specific wrappers sharing the same REST contract).
export class RetrievalClient {
  private readonly baseURL: string;

  constructor(baseURL: string) {
    // Trim trailing slash.
    this.baseURL = baseURL.replace(/\/$/, '');
  }

  private buildHeaders(session: SessionHeaders): Record<string, string> {
    const headers: Record<string, string> = {
      'Content-Type': 'application/json',
      'X-Tenant-ID': session.tenantId,
      'X-User-ID': session.userId,
      'X-User-Roles': session.userRoles.join(','),
    };
    if (session.subjectDepartment) {
      headers['X-Subject-Department'] = session.subjectDepartment;
    }
    return headers;
  }

  private async post<T>(path: string, session: SessionHeaders, body: unknown): Promise<T> {
    const res = await fetch(`${this.baseURL}${path}`, {
      method: 'POST',
      headers: this.buildHeaders(session),
      body: JSON.stringify(body),
    });

    if (res.status === 503) {
      const err = await res.json().catch(() => ({ code: 'unknown' }));
      if (err.code === 'no_private_provider') {
        throw new NoPrivateProviderError();
      }
    }

    if (!res.ok) {
      const err = await res.json().catch(() => ({ code: 'unknown' }));
      throw new RetrievalError(err.code ?? 'unknown', `HTTP ${res.status}: ${JSON.stringify(err)}`);
    }

    return res.json() as Promise<T>;
  }

  // buildAllowedSnapshot fetches the permission-filtered schema view for a user.
  // Implements the algorithm from Phase 8 §8.1.
  async buildAllowedSnapshot(session: SessionHeaders, req: SnapshotRequest): Promise<AllowedSnapshot> {
    const raw = await this.post<unknown>('/v1/retrieval/snapshot', session, req);
    return AllowedSnapshotSchema.parse(raw);
  }

  // retrieveDocs performs RAG retrieval with per-chunk ACL enforcement and injection defenses.
  async retrieveDocs(session: SessionHeaders, req: DocsRequest): Promise<DocsResponse> {
    const raw = await this.post<unknown>('/v1/retrieval/docs', session, req);
    return DocsResponseSchema.parse(raw);
  }

  // routeRequest determines the appropriate LLM provider for given content classifications.
  async routeRequest(session: SessionHeaders, req: RouteRequest): Promise<ProviderRoute> {
    const raw = await this.post<unknown>('/v1/retrieval/route', session, req);
    return ProviderRouteSchema.parse(raw);
  }

  // explain is an admin-only debug endpoint that returns snapshot + docs + route for a user+query.
  async explain(session: SessionHeaders, req: ExplainRequest): Promise<unknown> {
    return this.post<unknown>('/v1/retrieval/explain', session, req);
  }
}
