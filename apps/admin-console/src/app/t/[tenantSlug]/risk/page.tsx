"use client";

import { useParams } from "next/navigation";
import { useEffect, useState } from "react";
import {
  AlertTriangle,
  RefreshCw,
  ShieldAlert,
  TrendingUp,
} from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";

interface RiskEvent {
  id: string;
  user_id: string;
  kind: "spike" | "decay" | "override" | "warmup_end";
  score_before?: number;
  score_after?: number;
  payload: Record<string, unknown>;
  created_at: string;
}

function scoreBadge(score?: number) {
  if (score === undefined) return <Badge variant="outline">—</Badge>;
  if (score >= 86) return <Badge className="bg-red-600 text-white">{score}</Badge>;
  if (score >= 71) return <Badge className="bg-orange-500 text-white">{score}</Badge>;
  if (score >= 41) return <Badge className="bg-yellow-500 text-black">{score}</Badge>;
  return <Badge className="bg-green-600 text-white">{score}</Badge>;
}

function kindLabel(kind: string) {
  const map: Record<string, string> = {
    spike: "Risk Spike",
    decay: "Decay",
    override: "Manual Override",
    warmup_end: "Warm-up Complete",
  };
  return map[kind] ?? kind;
}

export default function RiskPage() {
  const { tenantSlug } = useParams<{ tenantSlug: string }>();
  const [events, setEvents] = useState<RiskEvent[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const fetchEvents = async () => {
    setLoading(true);
    setError(null);
    try {
      const res = await fetch(`/api/t/${tenantSlug}/risk/events`);
      if (!res.ok) throw new Error(await res.text());
      const data = await res.json();
      setEvents(data.items ?? []);
    } catch (e) {
      setError(e instanceof Error ? e.message : "Failed to load risk events");
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    fetchEvents();
  }, [tenantSlug]);

  return (
    <div className="flex-1 space-y-6 p-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold tracking-tight flex items-center gap-2">
            <ShieldAlert className="h-6 w-6 text-orange-500" />
            Risk & Anomaly Detection
          </h1>
          <p className="text-sm text-muted-foreground mt-1">
            Behavioral risk scores for users based on 90-day statistical baselines.
          </p>
        </div>
        <Button variant="outline" size="sm" onClick={fetchEvents} disabled={loading}>
          <RefreshCw className={`h-4 w-4 mr-2 ${loading ? "animate-spin" : ""}`} />
          Refresh
        </Button>
      </div>

      {/* Score legend */}
      <div className="flex gap-3 flex-wrap">
        <div className="flex items-center gap-1.5 text-sm">
          <span className="inline-block w-3 h-3 rounded-full bg-green-600" />
          Low (0–40)
        </div>
        <div className="flex items-center gap-1.5 text-sm">
          <span className="inline-block w-3 h-3 rounded-full bg-yellow-500" />
          Medium (41–70)
        </div>
        <div className="flex items-center gap-1.5 text-sm">
          <span className="inline-block w-3 h-3 rounded-full bg-orange-500" />
          High (71–85)
        </div>
        <div className="flex items-center gap-1.5 text-sm">
          <span className="inline-block w-3 h-3 rounded-full bg-red-600" />
          Critical (86–100)
        </div>
      </div>

      {/* Events table */}
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <TrendingUp className="h-5 w-5" />
            Recent Risk Events
          </CardTitle>
          <CardDescription>
            Score spikes, manual overrides, and warm-up completions. Last 100 events.
          </CardDescription>
        </CardHeader>
        <CardContent>
          {error && (
            <div className="flex items-center gap-2 text-destructive text-sm mb-4">
              <AlertTriangle className="h-4 w-4" />
              {error}
            </div>
          )}
          {loading ? (
            <div className="text-sm text-muted-foreground py-8 text-center">
              Loading risk events…
            </div>
          ) : events.length === 0 ? (
            <div className="text-sm text-muted-foreground py-8 text-center">
              No risk events yet. Events appear after users complete warm-up (30 days).
            </div>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>User ID</TableHead>
                  <TableHead>Event</TableHead>
                  <TableHead>Score Before</TableHead>
                  <TableHead>Score After</TableHead>
                  <TableHead>Time</TableHead>
                  <TableHead>Detail</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {events.map((ev) => (
                  <TableRow key={ev.id}>
                    <TableCell className="font-mono text-xs max-w-[160px] truncate">
                      {ev.user_id}
                    </TableCell>
                    <TableCell>
                      <Badge variant={ev.kind === "spike" ? "destructive" : "secondary"}>
                        {kindLabel(ev.kind)}
                      </Badge>
                    </TableCell>
                    <TableCell>{scoreBadge(ev.score_before)}</TableCell>
                    <TableCell>{scoreBadge(ev.score_after)}</TableCell>
                    <TableCell className="text-xs text-muted-foreground">
                      {new Date(ev.created_at).toLocaleString()}
                    </TableCell>
                    <TableCell className="text-xs text-muted-foreground max-w-[200px] truncate">
                      {ev.payload?.reason as string ?? "—"}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
