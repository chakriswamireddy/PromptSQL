"use client";

import { useQuery } from "@tanstack/react-query";
import { api } from "@/lib/api-client";
import type { PolicyAuditListResponse, AccessAuditListResponse } from "@/lib/types";

export function usePolicyAudit(
  tenantSlug: string,
  filters?: { userId?: string; policyId?: string; cursor?: string }
) {
  const params = new URLSearchParams();
  if (filters?.userId) params.set("userId", filters.userId);
  if (filters?.policyId) params.set("policyId", filters.policyId);
  if (filters?.cursor) params.set("cursor", filters.cursor);
  const qs = params.toString() ? `?${params}` : "";

  return useQuery({
    queryKey: [tenantSlug, "audit", "policy", filters],
    queryFn: () =>
      api.get<PolicyAuditListResponse>(`/v1/admin/${tenantSlug}/audit/policies${qs}`),
  });
}

export function useAccessAudit(
  tenantSlug: string,
  filters?: { userId?: string; resource?: string; decision?: string; cursor?: string }
) {
  const params = new URLSearchParams();
  if (filters?.userId) params.set("userId", filters.userId);
  if (filters?.resource) params.set("resource", filters.resource);
  if (filters?.decision) params.set("decision", filters.decision);
  if (filters?.cursor) params.set("cursor", filters.cursor);
  const qs = params.toString() ? `?${params}` : "";

  return useQuery({
    queryKey: [tenantSlug, "audit", "access", filters],
    queryFn: () =>
      api.get<AccessAuditListResponse>(`/v1/admin/${tenantSlug}/audit/access${qs}`),
  });
}
