"use client";

import { useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { apiClient } from "@/lib/api-client";

interface WebhookSubscription {
  id: string;
  name: string;
  url: string;
  event_types: string[];
  is_active: boolean;
  failure_count: number;
  last_delivery_at: string | null;
  created_at: string;
}

interface WebhookDelivery {
  id: string;
  event_id: string;
  event_type: string;
  attempt: number;
  status: "pending" | "delivered" | "failed" | "dlq";
  status_code: number | null;
  duration_ms: number | null;
  attempted_at: string | null;
}

const EVENT_TYPES = [
  "access.decision",
  "policy.changed",
  "risk.spike",
  "schema.drift",
  "breakglass.activated",
  "system.event",
];

function StatusBadge({ status }: { status: string }) {
  const colors: Record<string, string> = {
    delivered: "bg-emerald-500",
    failed: "bg-red-500",
    dlq: "bg-purple-600",
    pending: "bg-amber-500",
  };
  return (
    <Badge className={`${colors[status] ?? "bg-slate-500"} text-white text-xs`}>
      {status}
    </Badge>
  );
}

function HealthBadge({ active, failures }: { active: boolean; failures: number }) {
  if (!active) return <Badge variant="outline">Inactive</Badge>;
  if (failures > 10) return <Badge className="bg-amber-500 text-white">Degraded</Badge>;
  return <Badge className="bg-emerald-500 text-white">Healthy</Badge>;
}

export default function WebhooksPage({
  params,
}: {
  params: { tenantSlug: string };
}) {
  const qc = useQueryClient();
  const [showCreate, setShowCreate] = useState(false);
  const [selectedSub, setSelectedSub] = useState<string | null>(null);
  const [newSub, setNewSub] = useState({
    name: "",
    url: "",
    event_types: [] as string[],
  });
  const [createdSecret, setCreatedSecret] = useState<string | null>(null);

  const subsQ = useQuery<WebhookSubscription[]>({
    queryKey: ["webhooks", params.tenantSlug],
    queryFn: () => apiClient(`/v1/webhooks`).then((r) => r.json()),
  });

  const deliveriesQ = useQuery<WebhookDelivery[]>({
    queryKey: ["webhook-deliveries", selectedSub],
    queryFn: () =>
      apiClient(`/v1/webhooks/${selectedSub}/deliveries`).then((r) => r.json()),
    enabled: !!selectedSub,
  });

  const createMut = useMutation({
    mutationFn: () =>
      apiClient(`/v1/webhooks`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(newSub),
      }).then((r) => r.json()),
    onSuccess: (data: { id: string; secret: string }) => {
      setCreatedSecret(data.secret);
      setShowCreate(false);
      qc.invalidateQueries({ queryKey: ["webhooks"] });
    },
  });

  const deactivateMut = useMutation({
    mutationFn: (id: string) =>
      apiClient(`/v1/webhooks/${id}`, { method: "DELETE" }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["webhooks"] }),
  });

  const rotateMut = useMutation({
    mutationFn: (id: string) =>
      apiClient(`/v1/webhooks/${id}/rotate-secret`, { method: "POST" }).then(
        (r) => r.json()
      ),
    onSuccess: (data: { secret: string }) => setCreatedSecret(data.secret),
  });

  const testMut = useMutation({
    mutationFn: (id: string) =>
      apiClient(`/v1/webhooks/${id}/test`, { method: "POST" }),
  });

  const subs = subsQ.data ?? [];
  const selected = subs.find((s) => s.id === selectedSub) ?? null;

  return (
    <div className="p-6 flex gap-6 h-full overflow-hidden">
      {/* Left: subscription list */}
      <div className="w-80 flex-shrink-0 flex flex-col gap-3">
        <div className="flex items-center justify-between">
          <h1 className="text-xl font-semibold">Webhooks</h1>
          <Button size="sm" onClick={() => setShowCreate(true)}>
            + New
          </Button>
        </div>

        {subsQ.isLoading && (
          <p className="text-muted-foreground text-sm">Loading…</p>
        )}

        <div className="flex-1 overflow-y-auto space-y-2">
          {subs.map((sub) => (
            <button
              key={sub.id}
              onClick={() => setSelectedSub(sub.id)}
              className={`w-full text-left rounded-md border px-3 py-3 transition-colors ${
                selectedSub === sub.id ? "border-primary bg-accent" : "hover:bg-accent"
              }`}
            >
              <div className="flex items-center justify-between mb-1">
                <span className="font-medium text-sm truncate">{sub.name}</span>
                <HealthBadge active={sub.is_active} failures={sub.failure_count} />
              </div>
              <p className="text-xs text-muted-foreground truncate">{sub.url}</p>
              <div className="flex gap-1 mt-2 flex-wrap">
                {sub.event_types.map((et) => (
                  <Badge key={et} variant="outline" className="text-xs">
                    {et}
                  </Badge>
                ))}
              </div>
            </button>
          ))}
        </div>
      </div>

      {/* Right: detail panel */}
      {selected && (
        <div className="flex-1 overflow-y-auto space-y-4">
          <div className="flex items-center justify-between">
            <div>
              <h2 className="text-lg font-semibold">{selected.name}</h2>
              <p className="text-sm text-muted-foreground font-mono">{selected.url}</p>
            </div>
            <div className="flex gap-2">
              <Button
                size="sm"
                variant="outline"
                onClick={() => testMut.mutate(selected.id)}
                disabled={testMut.isPending}
              >
                Send Test
              </Button>
              <Button
                size="sm"
                variant="outline"
                onClick={() => rotateMut.mutate(selected.id)}
                disabled={rotateMut.isPending}
              >
                Rotate Secret
              </Button>
              <Button
                size="sm"
                variant="destructive"
                onClick={() => deactivateMut.mutate(selected.id)}
                disabled={deactivateMut.isPending}
              >
                Deactivate
              </Button>
            </div>
          </div>

          <div className="grid grid-cols-3 gap-3">
            <Card className="p-3 text-center">
              <p className="text-2xl font-bold">{selected.failure_count}</p>
              <p className="text-xs text-muted-foreground">Failures</p>
            </Card>
            <Card className="p-3 text-center">
              <p className="text-sm font-mono truncate">
                {selected.last_delivery_at
                  ? new Date(selected.last_delivery_at).toLocaleString()
                  : "Never"}
              </p>
              <p className="text-xs text-muted-foreground">Last Delivery</p>
            </Card>
            <Card className="p-3 text-center">
              <HealthBadge
                active={selected.is_active}
                failures={selected.failure_count}
              />
              <p className="text-xs text-muted-foreground mt-1">Status</p>
            </Card>
          </div>

          {/* Delivery log */}
          <div>
            <h3 className="font-semibold mb-2">Delivery Log</h3>
            {deliveriesQ.isLoading && (
              <p className="text-muted-foreground text-sm">Loading…</p>
            )}
            <div className="space-y-1">
              {(deliveriesQ.data ?? []).map((d) => (
                <div
                  key={d.id}
                  className="flex items-center gap-3 rounded border px-3 py-2 text-sm"
                >
                  <StatusBadge status={d.status} />
                  <span className="text-muted-foreground text-xs whitespace-nowrap">
                    {d.attempted_at
                      ? new Date(d.attempted_at).toLocaleString()
                      : "—"}
                  </span>
                  <span className="flex-1 font-mono text-xs truncate">
                    {d.event_type} / {d.event_id.slice(0, 8)}…
                  </span>
                  <span className="text-xs text-muted-foreground">
                    #{d.attempt}
                    {d.status_code ? ` · HTTP ${d.status_code}` : ""}
                    {d.duration_ms ? ` · ${d.duration_ms}ms` : ""}
                  </span>
                </div>
              ))}
            </div>
          </div>
        </div>
      )}

      {/* Create modal */}
      {showCreate && (
        <div className="fixed inset-0 bg-black/50 flex items-center justify-center z-50">
          <Card className="w-[480px] p-6 space-y-4">
            <h2 className="text-lg font-semibold">New Webhook Subscription</h2>
            <div className="space-y-3">
              <div>
                <label className="text-sm font-medium">Name</label>
                <Input
                  value={newSub.name}
                  onChange={(e) => setNewSub((s) => ({ ...s, name: e.target.value }))}
                  placeholder="My webhook"
                />
              </div>
              <div>
                <label className="text-sm font-medium">Endpoint URL (HTTPS)</label>
                <Input
                  value={newSub.url}
                  onChange={(e) => setNewSub((s) => ({ ...s, url: e.target.value }))}
                  placeholder="https://example.com/webhook"
                />
              </div>
              <div>
                <label className="text-sm font-medium mb-1 block">Event Types</label>
                <div className="flex flex-wrap gap-2">
                  {EVENT_TYPES.map((et) => (
                    <label key={et} className="flex items-center gap-1 text-sm cursor-pointer">
                      <input
                        type="checkbox"
                        checked={newSub.event_types.includes(et)}
                        onChange={(e) =>
                          setNewSub((s) => ({
                            ...s,
                            event_types: e.target.checked
                              ? [...s.event_types, et]
                              : s.event_types.filter((x) => x !== et),
                          }))
                        }
                      />
                      {et}
                    </label>
                  ))}
                </div>
              </div>
            </div>
            <div className="flex gap-2 justify-end">
              <Button variant="outline" onClick={() => setShowCreate(false)}>
                Cancel
              </Button>
              <Button
                onClick={() => createMut.mutate()}
                disabled={
                  createMut.isPending ||
                  !newSub.name ||
                  !newSub.url.startsWith("https://") ||
                  newSub.event_types.length === 0
                }
              >
                Create
              </Button>
            </div>
          </Card>
        </div>
      )}

      {/* Secret reveal (once on create/rotate) */}
      {createdSecret && (
        <div className="fixed inset-0 bg-black/50 flex items-center justify-center z-50">
          <Card className="w-[480px] p-6 space-y-4">
            <h2 className="text-lg font-semibold">Webhook Secret</h2>
            <p className="text-sm text-muted-foreground">
              This secret is shown <strong>once only</strong>. Copy it now and
              store it securely. Use it to verify{" "}
              <code>X-Janus-Signature</code> headers.
            </p>
            <pre className="bg-muted rounded p-3 font-mono text-sm break-all">
              {createdSecret}
            </pre>
            <Button className="w-full" onClick={() => setCreatedSecret(null)}>
              I&apos;ve saved the secret
            </Button>
          </Card>
        </div>
      )}
    </div>
  );
}
