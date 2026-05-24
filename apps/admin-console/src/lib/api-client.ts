/**
 * Typed API client for the api-gateway BFF.
 * All requests include CSRF token and forward the auth cookie automatically
 * (same-origin, HttpOnly, SameSite=Strict).
 */

const BASE = process.env.NEXT_PUBLIC_API_URL ?? "http://localhost:8080";

export class ApiError extends Error {
  constructor(
    public readonly status: number,
    public readonly code: string,
    message: string,
    public readonly requestId?: string
  ) {
    super(message);
    this.name = "ApiError";
  }
}

async function request<T>(
  path: string,
  init: RequestInit = {},
  idempotencyKey?: string
): Promise<T> {
  const headers: Record<string, string> = {
    "Content-Type": "application/json",
    ...(init.headers as Record<string, string>),
  };

  // Double-submit CSRF cookie (cookie is set server-side on page load).
  if (typeof document !== "undefined") {
    const csrf = document.cookie
      .split("; ")
      .find((c) => c.startsWith("csrf_token="))
      ?.split("=")[1];
    if (csrf) headers["X-CSRF-Token"] = csrf;
  }

  if (idempotencyKey) headers["Idempotency-Key"] = idempotencyKey;

  const res = await fetch(`${BASE}${path}`, {
    ...init,
    credentials: "include",
    headers,
  });

  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    throw new ApiError(
      res.status,
      body.code ?? "unknown_error",
      body.message ?? `HTTP ${res.status}`,
      body.requestId
    );
  }

  if (res.status === 204) return undefined as T;
  return res.json() as Promise<T>;
}

export const api = {
  get: <T>(path: string) => request<T>(path, { method: "GET" }),

  post: <T>(path: string, body: unknown, idempotencyKey?: string) =>
    request<T>(path, { method: "POST", body: JSON.stringify(body) }, idempotencyKey),

  put: <T>(path: string, body: unknown, etag?: string) =>
    request<T>(
      path,
      {
        method: "PUT",
        body: JSON.stringify(body),
        headers: etag ? { "If-Match": etag } : {},
      }
    ),

  delete: <T>(path: string) => request<T>(path, { method: "DELETE" }),
};
