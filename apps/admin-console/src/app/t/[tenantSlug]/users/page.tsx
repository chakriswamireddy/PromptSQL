"use client";

import { useState } from "react";
import { useUsers, useSuspendUser } from "@/hooks/use-users";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Card, CardContent, CardHeader } from "@/components/ui/card";
import { formatDate } from "@/lib/utils";
import { Search } from "lucide-react";
import type { User } from "@/lib/types";

export default function UsersPage({ params }: { params: { tenantSlug: string } }) {
  const { tenantSlug } = params;
  const { data, isLoading } = useUsers(tenantSlug);
  const suspend = useSuspendUser(tenantSlug);
  const [search, setSearch] = useState("");

  const users = (data?.items ?? []).filter(
    (u: User) =>
      !search ||
      u.email.toLowerCase().includes(search.toLowerCase()) ||
      u.name.toLowerCase().includes(search.toLowerCase())
  );

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold">Users</h1>
        <p className="text-muted-foreground text-sm">{data?.total ?? 0} total</p>
      </div>

      <div className="relative max-w-sm">
        <Search className="absolute left-3 top-2.5 h-4 w-4 text-muted-foreground" />
        <Input
          placeholder="Search users…"
          className="pl-9"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
        />
      </div>

      {isLoading && <p className="text-muted-foreground">Loading…</p>}

      <div className="space-y-2">
        {users.map((u: User) => (
          <Card key={u.id}>
            <CardHeader className="py-3 px-4">
              <div className="flex items-center justify-between gap-4">
                <div className="min-w-0">
                  <div className="flex items-center gap-2">
                    <span className="font-medium truncate">{u.name}</span>
                    <span className="text-muted-foreground text-xs">{u.email}</span>
                    <Badge
                      variant={
                        u.status === "active"
                          ? "success"
                          : u.status === "suspended"
                          ? "warning"
                          : "secondary"
                      }
                    >
                      {u.status}
                    </Badge>
                    {u.mfaEnabled && (
                      <Badge variant="outline" className="text-xs">MFA</Badge>
                    )}
                  </div>
                  <p className="text-xs text-muted-foreground mt-0.5">
                    Roles: {u.roles.join(", ") || "—"} ·{" "}
                    {u.lastLoginAt ? `Last login: ${formatDate(u.lastLoginAt)}` : "Never logged in"}
                  </p>
                </div>
                <div className="flex gap-2 shrink-0">
                  {u.status === "active" && (
                    <Button
                      size="sm"
                      variant="outline"
                      onClick={() => suspend.mutate(u.id)}
                      disabled={suspend.isPending}
                    >
                      Suspend
                    </Button>
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
