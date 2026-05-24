"use client";

import { useState } from "react";
import Link from "next/link";
import { usePolicy, useApprovePolicy, useArchivePolicy, useSubmitPolicy } from "@/hooks/use-policies";
import { PolicyEditor } from "@/components/policy-editor";
import { SimulatorPanel } from "@/components/simulator/panel";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { ApiError } from "@/lib/api-client";
import { formatDate } from "@/lib/utils";
import { ArrowLeft, GitBranch } from "lucide-react";
import type { PolicyStatus } from "@/lib/types";

function statusVariant(s: PolicyStatus) {
  const m: Record<PolicyStatus, "success" | "warning" | "secondary" | "outline" | "destructive" | "default"> = {
    active: "success",
    pending_review: "warning",
    archived: "secondary",
    draft: "outline",
  };
  return m[s] ?? "outline";
}

export default function PolicyDetailPage({
  params,
}: {
  params: { tenantSlug: string; id: string };
}) {
  const { tenantSlug, id } = params;
  const { data: policy, isLoading, isError } = usePolicy(tenantSlug, id);

  const submit = useSubmitPolicy(tenantSlug, id);
  const approve = useApprovePolicy(tenantSlug, id);
  const archive = useArchivePolicy(tenantSlug, id);

  const [actionError, setActionError] = useState<string | null>(null);
  const [showSimulator, setShowSimulator] = useState(false);

  async function runAction(fn: () => Promise<unknown>, label: string) {
    setActionError(null);
    try {
      await fn();
    } catch (err) {
      setActionError(err instanceof ApiError ? err.message : `${label} failed.`);
    }
  }

  if (isLoading) return <p className="text-muted-foreground">Loading…</p>;
  if (isError || !policy) return <p className="text-destructive">Policy not found.</p>;

  const editorValue = JSON.stringify(
    {
      name: policy.name,
      effect: policy.effect,
      action: policy.action,
      subjectMatch: policy.subjectMatch,
      resourceMatch: policy.resourceMatch,
      conditions: policy.conditions,
      allowedColumns: policy.allowedColumns,
      deniedColumns: policy.deniedColumns,
      columnMasks: policy.columnMasks,
      obligations: policy.obligations,
      effectiveFrom: policy.effectiveFrom,
      effectiveTo: policy.effectiveTo,
    },
    null,
    2
  );

  return (
    <div className="space-y-4 h-[calc(100vh-5rem)] flex flex-col">
      {/* Header */}
      <div className="shrink-0 space-y-3">
        <div className="flex items-center gap-3">
          <Link href={`/t/${tenantSlug}/policies`}>
            <Button variant="ghost" size="sm">
              <ArrowLeft className="h-4 w-4 mr-1" /> Policies
            </Button>
          </Link>
          <h1 className="text-xl font-bold truncate">{policy.name}</h1>
          <Badge variant={statusVariant(policy.status)}>
            {policy.status.replace("_", " ")}
          </Badge>
          <Badge variant={policy.effect === "deny" ? "destructive" : "outline"}>
            {policy.effect}
          </Badge>
          <Badge variant="secondary" className="text-xs">
            v{policy.version}
          </Badge>
        </div>

        <div className="flex items-center gap-3 text-xs text-muted-foreground">
          <span>Action: {policy.action}</span>
          <span>·</span>
          <span>Updated: {formatDate(policy.updatedAt)}</span>
          {policy.approvedBy && (
            <>
              <span>·</span>
              <span>Approved {formatDate(policy.updatedAt)}</span>
            </>
          )}
        </div>

        {/* Action buttons */}
        <div className="flex gap-2 flex-wrap">
          <Button
            size="sm"
            variant="outline"
            onClick={() => setShowSimulator(!showSimulator)}
          >
            {showSimulator ? "Hide simulator" : "Simulate"}
          </Button>

          {policy.status === "draft" && (
            <Link href={`/t/${tenantSlug}/policies/${id}/edit`}>
              <Button size="sm" variant="outline">Edit draft</Button>
            </Link>
          )}

          {policy.status === "draft" && (
            <Button
              size="sm"
              onClick={() => runAction(() => submit.mutateAsync(), "Submit")}
              disabled={submit.isPending}
            >
              Submit for review
            </Button>
          )}

          {policy.status === "pending_review" && (
            <Button
              size="sm"
              onClick={() => runAction(() => approve.mutateAsync(), "Approve")}
              disabled={approve.isPending}
            >
              Approve & activate
            </Button>
          )}

          {(policy.status === "active" || policy.status === "pending_review") && (
            <Button
              size="sm"
              variant="destructive"
              onClick={() => runAction(() => archive.mutateAsync(), "Archive")}
              disabled={archive.isPending}
            >
              Archive
            </Button>
          )}

          {/* Version history link */}
          <Button size="sm" variant="ghost" asChild>
            <Link href={`/t/${tenantSlug}/policies?name=${encodeURIComponent(policy.name)}`}>
              <GitBranch className="h-3 w-3 mr-1" />
              Version history
            </Link>
          </Button>
        </div>

        {actionError && (
          <p className="text-sm text-destructive bg-destructive/10 rounded px-3 py-2">
            {actionError}
          </p>
        )}
      </div>

      {/* Simulator panel */}
      {showSimulator && (
        <div className="shrink-0">
          <SimulatorPanel tenantSlug={tenantSlug} draftPolicyId={id} />
        </div>
      )}

      {/* Editor (read-only for non-draft) */}
      <div className="flex-1 min-h-0">
        <PolicyEditor
          value={editorValue}
          readOnly={policy.status !== "draft"}
          status={policy.status}
        />
      </div>
    </div>
  );
}
