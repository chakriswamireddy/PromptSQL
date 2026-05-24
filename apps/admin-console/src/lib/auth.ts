/**
 * Auth utilities — thin wrappers around the api-gateway auth endpoints.
 * Tokens are managed server-side (HttpOnly cookies); this module only
 * calls the endpoints that set/clear those cookies.
 */

import { api } from "./api-client";
import type { TokenResponse } from "./types";

export interface LoginPayload {
  tenantSlug: string;
  email: string;
  password: string;
  totpCode?: string;
}

export async function login(payload: LoginPayload): Promise<TokenResponse> {
  return api.post<TokenResponse>("/v1/auth/login", payload);
}

export async function logout(): Promise<void> {
  await api.post("/v1/auth/logout", {});
}

export async function logoutEverywhere(): Promise<void> {
  await api.post("/v1/auth/logout-everywhere", {});
}

export async function refreshToken(): Promise<TokenResponse> {
  return api.post<TokenResponse>("/v1/auth/refresh", {});
}

export function getTenantSlugFromPath(): string | null {
  if (typeof window === "undefined") return null;
  const match = window.location.pathname.match(/^\/t\/([^/]+)/);
  return match ? match[1] : null;
}
