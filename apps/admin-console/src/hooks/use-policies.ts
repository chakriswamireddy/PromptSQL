"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { v4 as uuidv4 } from "uuid";
import { api } from "@/lib/api-client";
import type {
  Policy,
  PolicyDraft,
  PolicyListResponse,
  DiffReport,
  SimulateRequest,
  SimulateResult,
} from "@/lib/types";

function policyKeys(tenantSlug: string) {
  return {
    all: [tenantSlug, "policies"] as const,
    list: (filters?: Record<string, string>) =>
      [tenantSlug, "policies", "list", filters] as const,
    detail: (id: string) => [tenantSlug, "policies", id] as const,
  };
}

export function usePolicies(
  tenantSlug: string,
  filters?: { status?: string; action?: string; cursor?: string }
) {
  const params = new URLSearchParams();
  if (filters?.status) params.set("status", filters.status);
  if (filters?.action) params.set("action", filters.action);
  if (filters?.cursor) params.set("cursor", filters.cursor);
  const qs = params.toString() ? `?${params}` : "";

  return useQuery({
    queryKey: policyKeys(tenantSlug).list(filters as Record<string, string>),
    queryFn: () =>
      api.get<PolicyListResponse>(`/v1/admin/${tenantSlug}/policies${qs}`),
  });
}

export function usePolicy(tenantSlug: string, id: string) {
  return useQuery({
    queryKey: policyKeys(tenantSlug).detail(id),
    queryFn: () => api.get<Policy>(`/v1/admin/${tenantSlug}/policies/${id}`),
    enabled: Boolean(id),
  });
}

export function useCreatePolicy(tenantSlug: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (draft: Omit<PolicyDraft, "id">) =>
      api.post<Policy>(
        `/v1/admin/${tenantSlug}/policies`,
        draft,
        uuidv4()
      ),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: policyKeys(tenantSlug).all });
    },
  });
}

export function useUpdatePolicy(tenantSlug: string, id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ draft, etag }: { draft: Partial<PolicyDraft>; etag?: string }) =>
      api.put<Policy>(`/v1/admin/${tenantSlug}/policies/${id}`, draft, etag),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: policyKeys(tenantSlug).detail(id) });
      qc.invalidateQueries({ queryKey: policyKeys(tenantSlug).all });
    },
  });
}

export function useSubmitPolicy(tenantSlug: string, id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () =>
      api.post<Policy>(
        `/v1/admin/${tenantSlug}/policies/${id}/submit`,
        {},
        uuidv4()
      ),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: policyKeys(tenantSlug).detail(id) });
      qc.invalidateQueries({ queryKey: policyKeys(tenantSlug).all });
    },
  });
}

export function useApprovePolicy(tenantSlug: string, id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () =>
      api.post<Policy>(
        `/v1/admin/${tenantSlug}/policies/${id}/approve`,
        {},
        uuidv4()
      ),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: policyKeys(tenantSlug).all });
    },
  });
}

export function useArchivePolicy(tenantSlug: string, id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () =>
      api.post<Policy>(
        `/v1/admin/${tenantSlug}/policies/${id}/archive`,
        {},
        uuidv4()
      ),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: policyKeys(tenantSlug).all });
    },
  });
}

export function useSimulate(tenantSlug: string) {
  return useMutation({
    mutationFn: (req: SimulateRequest) =>
      api.post<SimulateResult>(
        `/v1/admin/${tenantSlug}/policies/simulate`,
        req
      ),
  });
}

export function useSimulateDiff(tenantSlug: string) {
  return useMutation({
    mutationFn: (req: { draftPolicyId: string; sampleSize?: number }) =>
      api.post<DiffReport>(
        `/v1/admin/${tenantSlug}/policies/simulate/diff`,
        req
      ),
  });
}
