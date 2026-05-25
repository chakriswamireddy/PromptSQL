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
import { Textarea } from "@/components/ui/textarea";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Siren, CheckCircle, XCircle, Clock, AlertTriangle } from "lucide-react";

interface Session {
  id: string;
  principal_id: string;
  initiator_id: string;
  reason: string;
  status: "pending_approval" | "active" | "terminated" | "expired";
  approvers: string[];
  max_duration_sec: number;
  started_at?: string;
  expires_at?: string;
  created_at: string;
}

const statusConfig = {
  pending_approval: { label: "Pending Approval", variant: "secondary" as const, icon: Clock },
  active:           { label: "Active",            variant: "destructive" as const, icon: AlertTriangle },
  terminated:       { label: "Terminated",        variant: "outline" as const,    icon: XCircle },
  expired:          { label: "Expired",           variant: "outline" as const,    icon: XCircle },
} as const;

export default function BreakGlassPage() {
  const params = useParams<{ tenantSlug: string }>();
  const tenantSlug = params.tenantSlug;

  const [sessions, setSessions] = useState<Session[]>([]);
  const [loading, setLoading] = useState(true);
  const [requestOpen, setRequestOpen] = useState(false);
  const [form, setForm] = useState({
    principal_id: "",
    reason: "",
    max_duration_sec: "3600",
    scope: '{"all_opted_in": true}',
  });

  const fetchSessions = async () => {
    try {
      const res = await fetch(`/api/v1/admin/${tenantSlug}/breakglass/sessions`);
      if (res.ok) {
        const data = await res.json();
        setSessions(data.items ?? []);
      }
    } catch {
      // silently degrade
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    fetchSessions();
    const interval = setInterval(fetchSessions, 10_000);
    return () => clearInterval(interval);
  }, [tenantSlug]);

  const handleRequest = async () => {
    const body = {
      principal_id: form.principal_id,
      reason: form.reason,
      max_duration_sec: parseInt(form.max_duration_sec, 10),
      scope: JSON.parse(form.scope),
    };
    const res = await fetch(`/api/v1/admin/${tenantSlug}/breakglass/request`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
    if (res.ok) {
      setRequestOpen(false);
      fetchSessions();
    }
  };

  const handleApprove = async (sessionId: string) => {
    await fetch(`/api/v1/admin/${tenantSlug}/breakglass/${sessionId}/approve`, {
      method: "POST",
    });
    fetchSessions();
  };

  const handleTerminate = async (sessionId: string) => {
    if (!confirm("Terminate this break-glass session?")) return;
    await fetch(`/api/v1/admin/${tenantSlug}/breakglass/${sessionId}/terminate`, {
      method: "POST",
    });
    fetchSessions();
  };

  const formatDuration = (sec: number) => {
    if (sec >= 3600) return `${sec / 3600}h`;
    if (sec >= 60) return `${Math.floor(sec / 60)}m`;
    return `${sec}s`;
  };

  return (
    <div className="p-6 space-y-6">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2">
          <Siren className="h-6 w-6 text-destructive" />
          <div>
            <h1 className="text-2xl font-bold">Break-Glass Sessions</h1>
            <p className="text-sm text-muted-foreground">
              Emergency access bypass — dual approval required, all actions audited.
            </p>
          </div>
        </div>

        <Dialog open={requestOpen} onOpenChange={setRequestOpen}>
          <DialogTrigger asChild>
            <Button variant="destructive">Request Break-Glass</Button>
          </DialogTrigger>
          <DialogContent className="sm:max-w-lg">
            <DialogHeader>
              <DialogTitle>Request Break-Glass Access</DialogTitle>
              <DialogDescription>
                Two approvers are required. You cannot approve your own request.
                Maximum duration is 1 hour.
              </DialogDescription>
            </DialogHeader>
            <div className="space-y-4 py-2">
              <div className="space-y-1">
                <Label htmlFor="principal_id">Target User ID</Label>
                <Input
                  id="principal_id"
                  placeholder="uuid of the user needing elevated access"
                  value={form.principal_id}
                  onChange={(e) => setForm({ ...form, principal_id: e.target.value })}
                />
              </div>
              <div className="space-y-1">
                <Label htmlFor="reason">Reason (min 10 chars)</Label>
                <Textarea
                  id="reason"
                  placeholder="Incident response — production data access needed to diagnose..."
                  value={form.reason}
                  onChange={(e) => setForm({ ...form, reason: e.target.value })}
                  rows={3}
                />
              </div>
              <div className="space-y-1">
                <Label htmlFor="duration">Duration (seconds, max 3600)</Label>
                <Input
                  id="duration"
                  type="number"
                  min={60}
                  max={3600}
                  value={form.max_duration_sec}
                  onChange={(e) => setForm({ ...form, max_duration_sec: e.target.value })}
                />
              </div>
              <div className="space-y-1">
                <Label htmlFor="scope">Scope (JSON)</Label>
                <Textarea
                  id="scope"
                  value={form.scope}
                  onChange={(e) => setForm({ ...form, scope: e.target.value })}
                  rows={2}
                  className="font-mono text-xs"
                />
              </div>
            </div>
            <DialogFooter>
              <Button variant="outline" onClick={() => setRequestOpen(false)}>Cancel</Button>
              <Button variant="destructive" onClick={handleRequest}>Submit Request</Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      </div>

      {loading ? (
        <p className="text-muted-foreground text-sm">Loading sessions…</p>
      ) : sessions.length === 0 ? (
        <Card>
          <CardContent className="py-12 text-center text-muted-foreground">
            <CheckCircle className="h-8 w-8 mx-auto mb-2 text-green-500" />
            No active or pending break-glass sessions.
          </CardContent>
        </Card>
      ) : (
        <div className="space-y-3">
          {sessions.map((sess) => {
            const cfg = statusConfig[sess.status] ?? statusConfig.expired;
            const StatusIcon = cfg.icon;
            return (
              <Card key={sess.id} className="border-l-4 border-l-destructive/50">
                <CardHeader className="pb-2">
                  <div className="flex items-center justify-between">
                    <CardTitle className="text-sm font-mono">{sess.id.slice(0, 8)}…</CardTitle>
                    <Badge variant={cfg.variant} className="flex items-center gap-1">
                      <StatusIcon className="h-3 w-3" />
                      {cfg.label}
                    </Badge>
                  </div>
                  <CardDescription className="text-xs">
                    Principal: <code className="font-mono">{sess.principal_id}</code> ·
                    Duration: {formatDuration(sess.max_duration_sec)} ·
                    Approvals: {sess.approvers?.length ?? 0}/2
                  </CardDescription>
                </CardHeader>
                <CardContent>
                  <p className="text-sm text-muted-foreground mb-3">{sess.reason}</p>
                  {sess.expires_at && (
                    <p className="text-xs text-muted-foreground mb-3">
                      Expires: {new Date(sess.expires_at).toLocaleString()}
                    </p>
                  )}
                  <div className="flex gap-2">
                    {sess.status === "pending_approval" && (
                      <Button size="sm" onClick={() => handleApprove(sess.id)}>
                        Approve
                      </Button>
                    )}
                    {(sess.status === "pending_approval" || sess.status === "active") && (
                      <Button size="sm" variant="outline" onClick={() => handleTerminate(sess.id)}>
                        Terminate
                      </Button>
                    )}
                  </div>
                </CardContent>
              </Card>
            );
          })}
        </div>
      )}
    </div>
  );
}
