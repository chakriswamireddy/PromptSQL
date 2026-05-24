"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api-client";
import type { User, UserListResponse } from "@/lib/types";

export function useUsers(tenantSlug: string, cursor?: string) {
  const qs = cursor ? `?cursor=${cursor}` : "";
  return useQuery({
    queryKey: [tenantSlug, "users", cursor],
    queryFn: () => api.get<UserListResponse>(`/v1/admin/${tenantSlug}/users${qs}`),
  });
}

export function useUser(tenantSlug: string, id: string) {
  return useQuery({
    queryKey: [tenantSlug, "users", id],
    queryFn: () => api.get<User>(`/v1/admin/${tenantSlug}/users/${id}`),
    enabled: Boolean(id),
  });
}

export function useSuspendUser(tenantSlug: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (userId: string) =>
      api.post(`/v1/admin/${tenantSlug}/users/${userId}/suspend`, {}),
    onSuccess: () => qc.invalidateQueries({ queryKey: [tenantSlug, "users"] }),
  });
}

export function useAssignRoles(tenantSlug: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ userId, roles }: { userId: string; roles: string[] }) =>
      api.put(`/v1/admin/${tenantSlug}/users/${userId}/roles`, { roles }),
    onSuccess: (_d, vars) =>
      qc.invalidateQueries({ queryKey: [tenantSlug, "users", vars.userId] }),
  });
}
