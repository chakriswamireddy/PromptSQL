"use client";

import { useState } from "react";
import { usePolicyAudit, useAccessAudit } from "@/hooks/use-audit";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader } from "@/components/ui/card";
import { formatDate } from "@/lib/utils";
import type { PolicyAuditEvent, AccessAuditEvent } from "@/lib/types";

export default function AuditPage({ params }: { params: { tenantSlug: string } }) {
  const { tenantSlug } = params;
  const [tab, setTab] = useState<"policy" | "access">("policy");

  const { data: policyData, isLoading: loadingPolicy } = usePolicyAudit(tenantSlug);
  const { data: accessData, isLoading: loadingAccess } = useAccessAudit(tenantSlug);

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold">Audit Trail</h1>
        <p className="text-muted-foreground text-sm">Tamper-evident record of all policy changes and data access.</p>
      </div>

      <div className="flex gap-2 border-b pb-2">
        <Button
          variant={tab === "policy" ? "default" : "ghost"}
          size="sm"
          onClick={() => setTab("policy")}
        >
          Policy audit
        </Button>
        <Button
          variant={tab === "access" ? "default" : "ghost"}
          size="sm"
          onClick={() => setTab("access")}
        >
          Access audit
        </Button>
      </div>

      {tab === "policy" && (
        <PolicyAuditTab events={policyData?.items ?? []} loading={loadingPolicy} />
      )}
      {tab === "access" && (
        <AccessAuditTab events={accessData?.items ?? []} loading={loadingAccess} />
      )}
    </div>
  );
}

function PolicyAuditTab({
  events,
  loading,
}: {
  events: PolicyAuditEvent[];
  loading: boolean;
}) {
  if (loading) return <p className="text-muted-foreground">Loading…</p>;
  if (events.length === 0) return <p className="text-muted-foreground">No policy events yet.</p>;

  return (
    <div className="space-y-2">
      {events.map((e) => (
        <Card key={e.id}>
          <CardHeader className="py-3 px-4">
            <div className="flex items-start justify-between gap-4">
              <div className="min-w-0">
                <div className="flex items-center gap-2">
                  <Badge variant="outline" className="text-xs font-mono">{e.action}</Badge>
                  <span className="text-sm font-medium truncate">
                    Policy {e.policyId.slice(0, 8)}…
                  </span>
                </div>
                <p className="text-xs text-muted-foreground mt-0.5">
                  by {e.actorEmail} · {formatDate(e.createdAt)}
                </p>
                <p className="text-xs text-muted-foreground font-mono">
                  hash: {e.rowHash.slice(0, 16)}…
                </p>
              </div>
              <span className="text-xs text-muted-foreground shrink-0">
                req: {e.requestId.slice(0, 8)}…
              </span>
            </div>
          </CardHeader>
        </Card>
      ))}
    </div>
  );
}

function AccessAuditTab({
  events,
  loading,
}: {
  events: AccessAuditEvent[];
  loading: boolean;
}) {
  if (loading) return <p className="text-muted-foreground">Loading…</p>;
  if (events.length === 0) return <p className="text-muted-foreground">No access events yet.</p>;

  return (
    <div className="space-y-2">
      {events.map((e) => (
        <Card key={e.id}>
          <CardHeader className="py-3 px-4">
            <div className="flex items-start justify-between gap-4">
              <div className="min-w-0">
                <div className="flex items-center gap-2">
                  <Badge
                    variant={e.decision === "permit" ? "success" : "destructive"}
                    className="text-xs"
                  >
                    {e.decision}
                  </Badge>
                  <span className="text-sm">{e.action} {e.resource}</span>
                </div>
                <p className="text-xs text-muted-foreground mt-0.5">
                  {e.userEmail} · {e.durationMs}ms · {formatDate(e.createdAt)}
                </p>
                <p className="text-xs text-muted-foreground">{e.reason}</p>
              </div>
              {e.riskScore != null && (
                <Badge
                  variant={e.riskScore > 70 ? "destructive" : e.riskScore > 40 ? "warning" : "secondary"}
                  className="shrink-0"
                >
                  risk {e.riskScore}
                </Badge>
              )}
            </div>
          </CardHeader>
        </Card>
      ))}
    </div>
  );
}
