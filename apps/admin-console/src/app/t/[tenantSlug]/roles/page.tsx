"use client";

import { useQuery } from "@tanstack/react-query";
import { api } from "@/lib/api-client";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader } from "@/components/ui/card";
import type { RoleListResponse } from "@/lib/types";

export default function RolesPage({ params }: { params: { tenantSlug: string } }) {
  const { tenantSlug } = params;
  const { data, isLoading } = useQuery({
    queryKey: [tenantSlug, "roles"],
    queryFn: () => api.get<RoleListResponse>(`/v1/admin/${tenantSlug}/roles`),
  });

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold">Roles</h1>
        <p className="text-muted-foreground text-sm">{data?.items.length ?? 0} roles</p>
      </div>

      {isLoading && <p className="text-muted-foreground">Loading…</p>}

      <div className="space-y-2">
        {(data?.items ?? []).map((r) => (
          <Card key={r.id}>
            <CardHeader className="py-3 px-4">
              <div className="flex items-center justify-between">
                <div>
                  <div className="flex items-center gap-2">
                    <span className="font-medium">{r.name}</span>
                    {r.parentRoleId && (
                      <Badge variant="secondary" className="text-xs">inherits</Badge>
                    )}
                  </div>
                  {r.description && (
                    <p className="text-xs text-muted-foreground mt-0.5">{r.description}</p>
                  )}
                </div>
                <div className="flex flex-wrap gap-1 max-w-xs justify-end">
                  {r.permissions.slice(0, 6).map((p) => (
                    <Badge key={p} variant="outline" className="text-xs">{p}</Badge>
                  ))}
                  {r.permissions.length > 6 && (
                    <Badge variant="secondary" className="text-xs">+{r.permissions.length - 6}</Badge>
                  )}
                </div>
              </div>
            </CardHeader>
          </Card>
        ))}
      </div>
    </div>
  );
}
