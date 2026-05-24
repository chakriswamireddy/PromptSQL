"use client";

import { useState } from "react";
import Link from "next/link";
import { usePolicies } from "@/hooks/use-policies";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Plus, Search } from "lucide-react";
import { formatDate } from "@/lib/utils";
import type { Policy, PolicyStatus } from "@/lib/types";

function statusVariant(s: PolicyStatus) {
  return s === "active"
    ? "success"
    : s === "pending_review"
    ? "warning"
    : s === "archived"
    ? "secondary"
    : "outline";
}

export default function PoliciesPage({ params }: { params: { tenantSlug: string } }) {
  const { tenantSlug } = params;
  const [statusFilter, setStatusFilter] = useState<string>("");
  const [search, setSearch] = useState("");

  const { data, isLoading, isError } = usePolicies(tenantSlug, {
    status: statusFilter || undefined,
  });

  const policies = (data?.items ?? []).filter(
    (p: Policy) =>
      !search ||
      p.name.toLowerCase().includes(search.toLowerCase()) ||
      p.action.toLowerCase().includes(search.toLowerCase())
  );

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold">Policies</h1>
          <p className="text-muted-foreground text-sm mt-1">
            {data?.total ?? 0} total
          </p>
        </div>
        <Link href={`/t/${tenantSlug}/policies/new`}>
          <Button>
            <Plus className="h-4 w-4 mr-2" /> New policy
          </Button>
        </Link>
      </div>

      <div className="flex gap-2 flex-wrap">
        <div className="relative flex-1 min-w-[200px]">
          <Search className="absolute left-3 top-2.5 h-4 w-4 text-muted-foreground" />
          <Input
            placeholder="Filter by name or action…"
            className="pl-9"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
          />
        </div>
        {(["", "draft", "pending_review", "active", "archived"] as const).map((s) => (
          <Button
            key={s}
            variant={statusFilter === s ? "default" : "outline"}
            size="sm"
            onClick={() => setStatusFilter(s)}
          >
            {s === "" ? "All" : s.replace("_", " ")}
          </Button>
        ))}
      </div>

      {isLoading && <p className="text-muted-foreground">Loading…</p>}
      {isError && (
        <p className="text-destructive">Failed to load policies. Try refreshing.</p>
      )}

      {!isLoading && policies.length === 0 && (
        <Card>
          <CardContent className="py-12 text-center text-muted-foreground">
            No policies match. <Link href={`/t/${tenantSlug}/policies/new`} className="text-primary underline">Create one</Link>.
          </CardContent>
        </Card>
      )}

      <div className="space-y-2">
        {policies.map((p: Policy) => (
          <Card key={p.id} className="hover:shadow-sm transition-shadow">
            <CardHeader className="py-3 px-4">
              <div className="flex items-center justify-between gap-4">
                <div className="flex-1 min-w-0">
                  <div className="flex items-center gap-2">
                    <Link
                      href={`/t/${tenantSlug}/policies/${p.id}`}
                      className="font-medium hover:underline truncate"
                    >
                      {p.name}
                    </Link>
                    <Badge variant={statusVariant(p.status)}>
                      {p.status.replace("_", " ")}
                    </Badge>
                    <Badge variant={p.effect === "deny" ? "destructive" : "outline"}>
                      {p.effect}
                    </Badge>
                  </div>
                  <p className="text-xs text-muted-foreground mt-0.5">
                    {p.action} · v{p.version} · {formatDate(p.updatedAt)}
                  </p>
                </div>
                <div className="flex gap-2 shrink-0">
                  <Link href={`/t/${tenantSlug}/policies/${p.id}`}>
                    <Button size="sm" variant="outline">View</Button>
                  </Link>
                  {p.status === "draft" && (
                    <Link href={`/t/${tenantSlug}/policies/${p.id}/edit`}>
                      <Button size="sm" variant="outline">Edit</Button>
                    </Link>
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
