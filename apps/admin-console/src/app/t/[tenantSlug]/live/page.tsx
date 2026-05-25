"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Card } from "@/components/ui/card";

interface LiveEvent {
  event_id: string;
  event_type: "access" | "policy";
  tenant_id: string;
  user_id?: string;
  resource?: string;
  decision?: string;
  risk_score?: number;
  trace_id?: string;
  event_time: string;
  detail?: Record<string, unknown>;
}

const DECISION_COLORS: Record<string, string> = {
  allow: "bg-emerald-500",
  deny: "bg-red-500",
  mask: "bg-amber-500",
};

function decisionBadgeClass(decision?: string): string {
  return DECISION_COLORS[decision ?? ""] ?? "bg-slate-500";
}

const WS_URL =
  process.env.NEXT_PUBLIC_LIVE_FEED_WS_URL ?? "ws://localhost:8080/v1/live-feed";

export default function LiveActivityPage({
  params,
}: {
  params: { tenantSlug: string };
}) {
  const [events, setEvents] = useState<LiveEvent[]>([]);
  const [paused, setPaused] = useState(false);
  const [connected, setConnected] = useState(false);
  const [filterUser, setFilterUser] = useState("");
  const [filterResource, setFilterResource] = useState("");
  const [filterDecision, setFilterDecision] = useState("");
  const [selected, setSelected] = useState<LiveEvent | null>(null);

  const wsRef = useRef<WebSocket | null>(null);
  const pausedRef = useRef(false);
  const pendingRef = useRef<LiveEvent[]>([]);

  pausedRef.current = paused;

  const connect = useCallback(() => {
    const token = sessionStorage.getItem("access_token") ?? "";
    const qs = new URLSearchParams({ token });
    if (filterUser) qs.set("user_id", filterUser);
    if (filterResource) qs.set("resource", filterResource);
    if (filterDecision) qs.set("decision", filterDecision);

    const ws = new WebSocket(`${WS_URL}?${qs}`);

    ws.onopen = () => setConnected(true);
    ws.onclose = () => {
      setConnected(false);
      // Reconnect after 3 s.
      setTimeout(connect, 3000);
    };
    ws.onerror = () => ws.close();

    ws.onmessage = (msg: MessageEvent<string>) => {
      try {
        const ev: LiveEvent = JSON.parse(msg.data);
        if (pausedRef.current) {
          pendingRef.current.push(ev);
          return;
        }
        setEvents((prev) => [ev, ...prev].slice(0, 500));
      } catch {
        // ignore malformed
      }
    };

    wsRef.current = ws;
  }, [filterUser, filterResource, filterDecision]);

  useEffect(() => {
    connect();
    return () => wsRef.current?.close();
  }, [connect]);

  function handlePauseResume() {
    if (paused) {
      // Flush pending.
      setEvents((prev) => [...pendingRef.current, ...prev].slice(0, 500));
      pendingRef.current = [];
    }
    setPaused((p) => !p);
  }

  return (
    <div className="flex h-full gap-4 p-4">
      {/* Event list */}
      <div className="flex-1 flex flex-col gap-3 overflow-hidden">
        <div className="flex items-center justify-between">
          <h1 className="text-xl font-semibold">Live Activity</h1>
          <div className="flex items-center gap-2">
            <span
              className={`inline-block w-2 h-2 rounded-full ${connected ? "bg-emerald-500" : "bg-red-500"}`}
            />
            <span className="text-sm text-muted-foreground">
              {connected ? "Connected" : "Reconnecting…"}
            </span>
            <Button size="sm" variant="outline" onClick={handlePauseResume}>
              {paused ? "Resume" : "Pause"}
            </Button>
          </div>
        </div>

        {/* Filters */}
        <div className="flex gap-2">
          <Input
            placeholder="User ID"
            value={filterUser}
            onChange={(e) => setFilterUser(e.target.value)}
            className="h-8 text-xs w-40"
          />
          <Input
            placeholder="Resource prefix"
            value={filterResource}
            onChange={(e) => setFilterResource(e.target.value)}
            className="h-8 text-xs w-48"
          />
          <Input
            placeholder="Decision (allow/deny)"
            value={filterDecision}
            onChange={(e) => setFilterDecision(e.target.value)}
            className="h-8 text-xs w-44"
          />
          <Button
            size="sm"
            variant="secondary"
            onClick={() => {
              wsRef.current?.close();
            }}
          >
            Apply Filters
          </Button>
        </div>

        {/* Scrolling event list */}
        <div className="flex-1 overflow-y-auto space-y-1 text-sm">
          {events.length === 0 && (
            <p className="text-muted-foreground text-center py-12">
              Waiting for events…
            </p>
          )}
          {events.map((ev) => (
            <button
              key={ev.event_id}
              onClick={() => setSelected(ev)}
              className="w-full text-left rounded-md border px-3 py-2 hover:bg-accent transition-colors grid grid-cols-[auto_1fr_auto_auto] gap-3 items-center"
            >
              <span className="text-xs text-muted-foreground whitespace-nowrap">
                {new Date(ev.event_time).toLocaleTimeString()}
              </span>
              <span className="truncate">
                {ev.user_id
                  ? `${ev.user_id} → ${ev.resource ?? "?"}`
                  : ev.event_type}
              </span>
              {ev.decision && (
                <Badge
                  className={`${decisionBadgeClass(ev.decision)} text-white text-xs`}
                >
                  {ev.decision}
                </Badge>
              )}
              {ev.risk_score != null && ev.risk_score > 0 && (
                <span className="text-xs text-amber-600">
                  ⚠ {ev.risk_score.toFixed(2)}
                </span>
              )}
            </button>
          ))}
        </div>
      </div>

      {/* Detail drawer */}
      {selected && (
        <Card className="w-96 flex-shrink-0 p-4 overflow-y-auto text-xs space-y-3">
          <div className="flex items-center justify-between">
            <h2 className="font-semibold text-base">Event Detail</h2>
            <Button
              size="sm"
              variant="ghost"
              onClick={() => setSelected(null)}
            >
              ✕
            </Button>
          </div>
          <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1">
            <dt className="text-muted-foreground">Event ID</dt>
            <dd className="font-mono truncate">{selected.event_id}</dd>
            <dt className="text-muted-foreground">Type</dt>
            <dd>{selected.event_type}</dd>
            <dt className="text-muted-foreground">Time</dt>
            <dd>{new Date(selected.event_time).toISOString()}</dd>
            <dt className="text-muted-foreground">User</dt>
            <dd className="font-mono truncate">{selected.user_id ?? "—"}</dd>
            <dt className="text-muted-foreground">Resource</dt>
            <dd className="font-mono truncate">{selected.resource ?? "—"}</dd>
            <dt className="text-muted-foreground">Decision</dt>
            <dd>
              {selected.decision ? (
                <Badge
                  className={`${decisionBadgeClass(selected.decision)} text-white`}
                >
                  {selected.decision}
                </Badge>
              ) : (
                "—"
              )}
            </dd>
            <dt className="text-muted-foreground">Risk Score</dt>
            <dd>{selected.risk_score?.toFixed(4) ?? "—"}</dd>
            <dt className="text-muted-foreground">Trace ID</dt>
            <dd className="font-mono truncate">{selected.trace_id ?? "—"}</dd>
          </dl>
          {selected.detail && (
            <div>
              <p className="text-muted-foreground mb-1">Raw Detail</p>
              <pre className="bg-muted rounded p-2 overflow-x-auto text-xs">
                {JSON.stringify(selected.detail, null, 2)}
              </pre>
            </div>
          )}
        </Card>
      )}
    </div>
  );
}
