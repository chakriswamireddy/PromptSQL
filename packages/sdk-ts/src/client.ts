export const SDK_VERSION = "1.0.0";

export interface ClientOptions {
  baseURL: string;
  token?: string;
  timeout?: number;
  fetch?: typeof globalThis.fetch;
}

export interface APIError {
  code: string;
  message: string;
  request_id?: string;
}

export class GovernanceError extends Error {
  constructor(
    public readonly code: string,
    message: string,
    public readonly requestId?: string,
    public readonly status?: number
  ) {
    super(`[${code}] ${message}${requestId ? ` (request_id: ${requestId})` : ""}`);
    this.name = "GovernanceError";
  }
}

export class BaseClient {
  protected _token: string | undefined;
  protected readonly baseURL: string;
  protected readonly _fetch: typeof globalThis.fetch;
  protected readonly timeout: number;

  constructor(opts: ClientOptions) {
    this.baseURL = opts.baseURL.replace(/\/$/, "");
    this._token = opts.token;
    this._fetch = opts.fetch ?? globalThis.fetch;
    this.timeout = opts.timeout ?? 30_000;
  }

  setToken(token: string): void {
    this._token = token;
  }

  protected async request<T>(
    method: string,
    path: string,
    body?: unknown
  ): Promise<T> {
    const headers: Record<string, string> = {
      "Content-Type": "application/json",
      Accept: "application/json",
      "User-Agent": `governance-sdk-ts/${SDK_VERSION}`,
    };
    if (this._token) {
      headers["Authorization"] = `Bearer ${this._token}`;
    }

    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), this.timeout);

    let resp: Response;
    try {
      resp = await this._fetch(this.baseURL + path, {
        method,
        headers,
        body: body !== undefined ? JSON.stringify(body) : undefined,
        signal: controller.signal,
      });
    } finally {
      clearTimeout(timer);
    }

    const text = await resp.text();

    if (!resp.ok) {
      let apiErr: Partial<APIError> = {};
      try {
        apiErr = JSON.parse(text);
      } catch {
        // not JSON
      }
      throw new GovernanceError(
        apiErr.code ?? "http_error",
        apiErr.message ?? text,
        apiErr.request_id,
        resp.status
      );
    }

    if (!text) return undefined as T;
    return JSON.parse(text) as T;
  }
}
