"use client";

import { useState, useEffect } from "react";
import { useParams } from "next/navigation";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Separator } from "@/components/ui/separator";
import { ListTree, CheckCircle, Zap } from "lucide-react";

interface PlaybookTier {
  min: number;
  max: number;
  action: string;
  params: Record<string, unknown>;
}

interface Playbook {
  id: string;
  version: number;
  tiers: PlaybookTier[];
  active: boolean;
  pause_auto_response: boolean;
  created_by: string;
  created_at: string;
  activated_at?: string;
}

const actionBadgeVariant = (action: string) => {
  switch (action) {
    case "block":    return "destructive";
    case "step_up":  return "default";
    case "mask":     return "secondary";
    case "tag":      return "outline";
    default:         return "outline";
  }
};

const DEFAULT_TIERS: PlaybookTier[] = [
  { min: 0,  max: 40,  action: "normal",   params: {} },
  { min: 41, max: 70,  action: "tag",      params: {} },
  { min: 71, max: 85,  action: "step_up",  params: { mfa_window_sec: 300 } },
  { min: 86, max: 95,  action: "mask",     params: {} },
  { min: 96, max: 100, action: "block",    params: {} },
];

export default function PlaybooksPage() {
  const params = useParams<{ tenantSlug: string }>();
  const tenantSlug = params.tenantSlug;

  const [playbooks, setPlaybooks] = useState<Playbook[]>([]);
  const [loading, setLoading] = useState(true);
  const [createOpen, setCreateOpen] = useState(false);
  const [tiers, setTiers] = useState<string>(JSON.stringify(DEFAULT_TIERS, null, 2));

  // Simulator state
  const [simScore, setSimScore] = useState("75");
  const [simResult, setSimResult] = useState<{ action: string; tier: string } | null>(null);

  const fetchPlaybooks = async () => {
    try {
      const res = await fetch(`/api/v1/admin/${tenantSlug}/playbooks`);
      if (res.ok) {
        const data = await res.json();
        setPlaybooks(data.items ?? []);
      }
    } catch {
      // silently degrade
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    fetchPlaybooks();
  }, [tenantSlug]);

  const handleCreate = async () => {
    let parsedTiers;
    try {
      parsedTiers = JSON.parse(tiers);
    } catch {
      alert("Invalid JSON for tiers");
      return;
    }
    const res = await fetch(`/api/v1/admin/${tenantSlug}/playbooks`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ tiers: parsedTiers }),
    });
    if (res.ok) {
      setCreateOpen(false);
      fetchPlaybooks();
    }
  };

  const handleActivate = async (id: string) => {
    if (!confirm("Activate this playbook? The current active version will be deactivated.")) return;
    await fetch(`/api/v1/admin/${tenantSlug}/playbooks/${id}/activate`, { method: "POST" });
    fetchPlaybooks();
  };

  const handleSimulate = async () => {
    const score = parseInt(simScore, 10);
    if (isNaN(score) || score < 0 || score > 100) return;
    const res = await fetch(`/api/v1/admin/${tenantSlug}/playbooks/simulate`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ score }),
    });
    if (res.ok) {
      const data = await res.json();
      setSimResult(data);
    }
  };

  return (
    <div className="p-6 space-y-6">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2">
          <ListTree className="h-6 w-6 text-primary" />
          <div>
            <h1 className="text-2xl font-bold">Risk Playbooks</h1>
            <p className="text-sm text-muted-foreground">
              Configure per-tenant auto-response tiers. Only one playbook can be active at a time.
            </p>
          </div>
        </div>

        <Dialog open={createOpen} onOpenChange={setCreateOpen}>
          <DialogTrigger asChild>
            <Button>New Playbook</Button>
          </DialogTrigger>
          <DialogContent className="sm:max-w-2xl">
            <DialogHeader>
              <DialogTitle>Create Playbook</DialogTitle>
              <DialogDescription>
                Define risk tiers as JSON. Each tier must have min, max, action, and params.
              </DialogDescription>
            </DialogHeader>
            <div className="space-y-4 py-2">
              <Label>Tiers (JSON)</Label>
              <textarea
                className="w-full font-mono text-xs rounded border bg-muted/30 p-3 h-64 resize-y"
                value={tiers}
                onChange={(e) => setTiers(e.target.value)}
              />
            </div>
            <DialogFooter>
              <Button variant="outline" onClick={() => setCreateOpen(false)}>Cancel</Button>
              <Button onClick={handleCreate}>Create</Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      </div>

      {/* Simulator */}
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base">
            <Zap className="h-4 w-4" />
            Playbook Simulator
          </CardTitle>
          <CardDescription>
            Preview what action would fire for a given risk score using the active playbook.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <div className="flex items-center gap-3">
            <div className="flex items-center gap-2">
              <Label>Risk score (0–100)</Label>
              <Input
                type="number"
                min={0}
                max={100}
                value={simScore}
                onChange={(e) => setSimScore(e.target.value)}
                className="w-24"
              />
            </div>
            <Button size="sm" onClick={handleSimulate}>Simulate</Button>
            {simResult && (
              <div className="flex items-center gap-2 text-sm">
                <span className="text-muted-foreground">→</span>
                <Badge variant={actionBadgeVariant(simResult.action) as any}>
                  {simResult.action}
                </Badge>
                <span className="text-muted-foreground">(tier: {simResult.tier})</span>
              </div>
            )}
          </div>
        </CardContent>
      </Card>

      <Separator />

      {loading ? (
        <p className="text-muted-foreground text-sm">Loading playbooks…</p>
      ) : playbooks.length === 0 ? (
        <Card>
          <CardContent className="py-12 text-center text-muted-foreground">
            No playbooks configured. Platform defaults are in effect.
          </CardContent>
        </Card>
      ) : (
        <div className="space-y-4">
          {playbooks.map((pb) => (
            <Card key={pb.id} className={pb.active ? "ring-2 ring-primary" : ""}>
              <CardHeader className="pb-2">
                <div className="flex items-center justify-between">
                  <CardTitle className="text-sm">
                    Version {pb.version}
                    {pb.active && (
                      <Badge className="ml-2" variant="default">
                        <CheckCircle className="h-3 w-3 mr-1" />
                        Active
                      </Badge>
                    )}
                  </CardTitle>
                  {!pb.active && (
                    <Button size="sm" variant="outline" onClick={() => handleActivate(pb.id)}>
                      Activate
                    </Button>
                  )}
                </div>
                <CardDescription className="text-xs">
                  Created: {new Date(pb.created_at).toLocaleString()}
                  {pb.activated_at && ` · Activated: ${new Date(pb.activated_at).toLocaleString()}`}
                </CardDescription>
              </CardHeader>
              <CardContent>
                <div className="flex flex-wrap gap-2">
                  {pb.tiers.map((tier, i) => (
                    <div
                      key={i}
                      className="flex items-center gap-1.5 rounded-md border px-2 py-1 text-xs"
                    >
                      <span className="text-muted-foreground">{tier.min}–{tier.max}</span>
                      <Badge variant={actionBadgeVariant(tier.action) as any} className="text-xs">
                        {tier.action}
                      </Badge>
                    </div>
                  ))}
                </div>
              </CardContent>
            </Card>
          ))}
        </div>
      )}
    </div>
  );
}
