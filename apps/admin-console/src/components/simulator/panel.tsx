"use client";

import { useState } from "react";
import { useSimulate, useSimulateDiff } from "@/hooks/use-policies";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Loader2, CheckCircle2, XCircle, AlertTriangle } from "lucide-react";
import type { SimulateRequest, SimulateResult, DiffReport } from "@/lib/types";
import { formatDate } from "@/lib/utils";

interface SimulatorPanelProps {
  tenantSlug: string;
  draftPolicyId?: string;
  dataSourceId?: string;
}

export function SimulatorPanel({
  tenantSlug,
  draftPolicyId,
  dataSourceId = "",
}: SimulatorPanelProps) {
  const [mode, setMode] = useState<"spot" | "diff">("spot");
  const simulate = useSimulate(tenantSlug);
  const simulateDiff = useSimulateDiff(tenantSlug);

  // Spot check form state.
  const [userId, setUserId] = useState("");
  const [action, setAction] = useState("read");
  const [resource, setResource] = useState("");
  const [dsId, setDsId] = useState(dataSourceId);
  const [sampleSize, setSampleSize] = useState(20);

  async function runSpotCheck() {
    const req: SimulateRequest = {
      useDraft: draftPolicyId,
      subject: { userId: userId || undefined },
      action,
      resource,
      dataSourceId: dsId,
    };
    await simulate.mutateAsync(req);
  }

  async function runDiff() {
    if (!draftPolicyId) return;
    await simulateDiff.mutateAsync({ draftPolicyId, sampleSize });
  }

  return (
    <Card className="border-dashed">
      <CardHeader className="py-3 px-4 flex-row items-center justify-between space-y-0">
        <CardTitle className="text-sm">Simulator</CardTitle>
        <div className="flex gap-1">
          <Button
            size="sm"
            variant={mode === "spot" ? "default" : "outline"}
            onClick={() => setMode("spot")}
          >
            Spot check
          </Button>
          {draftPolicyId && (
            <Button
              size="sm"
              variant={mode === "diff" ? "default" : "outline"}
              onClick={() => setMode("diff")}
            >
              Diff
            </Button>
          )}
        </div>
      </CardHeader>

      <CardContent className="px-4 pb-4 space-y-4">
        {mode === "spot" && (
          <SpotCheckForm
            userId={userId}
            setUserId={setUserId}
            action={action}
            setAction={setAction}
            resource={resource}
            setResource={setResource}
            dsId={dsId}
            setDsId={setDsId}
            onRun={runSpotCheck}
            isPending={simulate.isPending}
            result={simulate.data}
            error={simulate.error?.message}
          />
        )}

        {mode === "diff" && draftPolicyId && (
          <DiffForm
            sampleSize={sampleSize}
            setSampleSize={setSampleSize}
            onRun={runDiff}
            isPending={simulateDiff.isPending}
            report={simulateDiff.data}
            error={simulateDiff.error?.message}
          />
        )}
      </CardContent>
    </Card>
  );
}

// ── Spot Check ──────────────────────────────────────────────────────────────

function SpotCheckForm({
  userId, setUserId,
  action, setAction,
  resource, setResource,
  dsId, setDsId,
  onRun, isPending, result, error,
}: {
  userId: string; setUserId: (v: string) => void;
  action: string; setAction: (v: string) => void;
  resource: string; setResource: (v: string) => void;
  dsId: string; setDsId: (v: string) => void;
  onRun: () => void; isPending: boolean;
  result?: SimulateResult; error?: string;
}) {
  return (
    <div className="space-y-3">
      <div className="grid grid-cols-2 gap-2">
        <div>
          <label className="text-xs font-medium">User ID (or leave blank for guest)</label>
          <Input
            placeholder="uuid"
            value={userId}
            onChange={(e) => setUserId(e.target.value)}
            className="h-8 text-sm"
          />
        </div>
        <div>
          <label className="text-xs font-medium">Action</label>
          <Input
            placeholder="read"
            value={action}
            onChange={(e) => setAction(e.target.value)}
            className="h-8 text-sm"
          />
        </div>
        <div>
          <label className="text-xs font-medium">Resource (table)</label>
          <Input
            placeholder="orders"
            value={resource}
            onChange={(e) => setResource(e.target.value)}
            className="h-8 text-sm"
          />
        </div>
        <div>
          <label className="text-xs font-medium">Data Source ID</label>
          <Input
            placeholder="uuid"
            value={dsId}
            onChange={(e) => setDsId(e.target.value)}
            className="h-8 text-sm"
          />
        </div>
      </div>

      <Button size="sm" onClick={onRun} disabled={isPending || !resource}>
        {isPending && <Loader2 className="h-3 w-3 mr-1 animate-spin" />}
        Run check
      </Button>

      {error && <p className="text-xs text-destructive">{error}</p>}

      {result && <SpotResult result={result} />}
    </div>
  );
}

function SpotResult({ result }: { result: SimulateResult }) {
  const isPermit = result.effect === "PERMIT";
  return (
    <div className="rounded-md border p-3 space-y-2 text-sm">
      <div className="flex items-center gap-2 font-semibold">
        {isPermit ? (
          <CheckCircle2 className="h-4 w-4 text-green-500" />
        ) : (
          <XCircle className="h-4 w-4 text-destructive" />
        )}
        <span className={isPermit ? "text-green-700" : "text-destructive"}>
          {result.effect}
        </span>
        <span className="font-normal text-muted-foreground text-xs">— {result.reason}</span>
      </div>

      {result.matchedPolicies.length > 0 && (
        <div>
          <span className="text-xs text-muted-foreground">Matched policies: </span>
          {result.matchedPolicies.map((p) => (
            <Badge key={p.id} variant="secondary" className="mr-1 text-xs">
              {p.name} ({p.effect})
            </Badge>
          ))}
        </div>
      )}

      {result.allowedColumns.length > 0 && (
        <div className="text-xs">
          <span className="text-muted-foreground">Allowed columns: </span>
          {result.allowedColumns.join(", ")}
        </div>
      )}
      {result.deniedColumns.length > 0 && (
        <div className="text-xs">
          <span className="text-muted-foreground">Denied columns: </span>
          <span className="text-destructive">{result.deniedColumns.join(", ")}</span>
        </div>
      )}
      {Object.keys(result.columnMasks).length > 0 && (
        <div className="text-xs">
          <span className="text-muted-foreground">Masked: </span>
          {Object.entries(result.columnMasks)
            .map(([col, fn]) => `${col} → ${fn}`)
            .join(", ")}
        </div>
      )}
      {result.obligations.length > 0 && (
        <div className="text-xs">
          <span className="text-muted-foreground">Obligations: </span>
          {result.obligations.map((o) => o.kind).join(", ")}
        </div>
      )}
    </div>
  );
}

// ── Diff ────────────────────────────────────────────────────────────────────

function DiffForm({
  sampleSize, setSampleSize,
  onRun, isPending, report, error,
}: {
  sampleSize: number; setSampleSize: (n: number) => void;
  onRun: () => void; isPending: boolean;
  report?: DiffReport; error?: string;
}) {
  return (
    <div className="space-y-3">
      <div className="flex items-end gap-2">
        <div>
          <label className="text-xs font-medium">Sample users per role</label>
          <Input
            type="number"
            min={1}
            max={200}
            value={sampleSize}
            onChange={(e) => setSampleSize(Number(e.target.value))}
            className="h-8 w-24 text-sm"
          />
        </div>
        <Button size="sm" onClick={onRun} disabled={isPending}>
          {isPending && <Loader2 className="h-3 w-3 mr-1 animate-spin" />}
          Run diff
        </Button>
      </div>

      {error && <p className="text-xs text-destructive">{error}</p>}
      {report && <DiffResult report={report} />}
    </div>
  );
}

function severityVariant(s: DiffReport["summary"]["severity"]) {
  return s === "critical" ? "destructive"
    : s === "high" ? "destructive"
    : s === "medium" ? "warning"
    : "secondary";
}

function DiffResult({ report }: { report: DiffReport }) {
  const s = report.summary;
  return (
    <div className="rounded-md border p-3 space-y-3 text-sm">
      <div className="flex items-center gap-2">
        <AlertTriangle className="h-4 w-4 text-yellow-500" />
        <span className="font-semibold">Blast radius</span>
        <Badge variant={severityVariant(s.severity)}>{s.severity}</Badge>
        <span className="text-xs text-muted-foreground">
          ~{s.affectedUsersEstimate} users affected
        </span>
      </div>

      <div className="grid grid-cols-2 gap-2 text-xs">
        {s.newlyPermittedColumns.length > 0 && (
          <div>
            <span className="text-muted-foreground">+Columns visible: </span>
            <span className="text-green-700">{s.newlyPermittedColumns.join(", ")}</span>
          </div>
        )}
        {s.newlyDeniedColumns.length > 0 && (
          <div>
            <span className="text-muted-foreground">−Columns hidden: </span>
            <span className="text-destructive">{s.newlyDeniedColumns.join(", ")}</span>
          </div>
        )}
        {s.newlyBlockedRows > 0 && (
          <div className="text-muted-foreground">
            +{s.newlyBlockedRows} row conditions now block
          </div>
        )}
        {s.newlyPermittedRows > 0 && (
          <div className="text-muted-foreground">
            +{s.newlyPermittedRows} rows newly visible
          </div>
        )}
      </div>

      {report.perRoleDiff.length > 0 && (
        <div className="space-y-1">
          <p className="text-xs font-medium text-muted-foreground">Per-role summary</p>
          {report.perRoleDiff.map((r) => (
            <div key={r.role} className="flex items-center gap-2 text-xs">
              <Badge variant="outline" className="text-xs">{r.role}</Badge>
              {r.permitTodeny > 0 && (
                <span className="text-destructive">{r.permitTodeny} permit→deny</span>
              )}
              {r.denyToPermit > 0 && (
                <span className="text-green-700">{r.denyToPermit} deny→permit</span>
              )}
            </div>
          ))}
        </div>
      )}

      <p className="text-xs text-muted-foreground">
        Report generated {formatDate(report.createdAt)} · hash {report.draftHash.slice(0, 8)}…
      </p>
    </div>
  );
}
