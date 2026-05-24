"use client";

import { useQuery } from "@tanstack/react-query";
import { api } from "@/lib/api-client";
import { Badge } from "@/components/ui/badge";
import { Card, CardHeader } from "@/components/ui/card";
import { formatDate } from "@/lib/utils";
import type { DataSourceListResponse } from "@/lib/types";

export default function DataSourcesPage({ params }: { params: { tenantSlug: string } }) {
  const { tenantSlug } = params;
  const { data, isLoading } = useQuery({
    queryKey: [tenantSlug, "data-sources"],
    queryFn: () => api.get<DataSourceListResponse>(`/v1/admin/${tenantSlug}/data-sources`),
  });

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold">Data Sources</h1>
        <p className="text-muted-foreground text-sm">
          Managed databases crawled and governed by this platform.
        </p>
      </div>

      {isLoading && <p className="text-muted-foreground">Loading…</p>}

      <div className="space-y-2">
        {(data?.items ?? []).map((ds) => (
          <Card key={ds.id}>
            <CardHeader className="py-3 px-4">
              <div className="flex items-center justify-between gap-4">
                <div>
                  <div className="flex items-center gap-2">
                    <span className="font-medium">{ds.name}</span>
                    <Badge variant="outline" className="text-xs">{ds.kind}</Badge>
                    <Badge
                      variant={
                        ds.status === "connected"
                          ? "success"
                          : ds.status === "error"
                          ? "destructive"
                          : "secondary"
                      }
                      className="text-xs"
                    >
                      {ds.status}
                    </Badge>
                  </div>
                  <p className="text-xs text-muted-foreground mt-0.5">
                    {ds.host}:{ds.port}/{ds.database} · added {formatDate(ds.createdAt)}
                  </p>
                </div>
              </div>
            </CardHeader>
          </Card>
        ))}

        {!isLoading && (data?.items ?? []).length === 0 && (
          <p className="text-muted-foreground text-sm">
            No data sources registered yet. Connect a database to start governing it.
          </p>
        )}
      </div>
    </div>
  );
}
