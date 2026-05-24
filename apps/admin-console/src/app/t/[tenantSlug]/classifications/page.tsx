"use client";

import { useState, useMemo } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api-client";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader } from "@/components/ui/card";
import { formatDate } from "@/lib/utils";

type Column = {
  id: string;
  schemaName: string;
  tableName: string;
  columnName: string;
  dataType: string;
  quarantine: boolean;
  classifiedBy: string;
  classification?: string;
  tags?: string[];
  sampleValues?: string[];
  lastCrawledAt?: string;
};

type ColumnsResponse = { items: Column[]; count: number };

const CLASSIFICATIONS = ["public", "internal", "confidential", "restricted"] as const;
type Classification = (typeof CLASSIFICATIONS)[number];

const classificationVariant: Record<Classification, "secondary" | "outline" | "warning" | "destructive"> = {
  public: "secondary",
  internal: "outline",
  confidential: "warning",
  restricted: "destructive",
};

export default function ClassificationsPage({ params }: { params: { tenantSlug: string } }) {
  const { tenantSlug } = params;
  const queryClient = useQueryClient();

  const [dataSourceID, setDataSourceID] = useState("");
  const [filterQuarantine, setFilterQuarantine] = useState(false);
  const [filterClass, setFilterClass] = useState<string>("");
  const [filterTable, setFilterTable] = useState("");
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [bulkClass, setBulkClass] = useState<Classification>("internal");

  const { data, isLoading } = useQuery({
    queryKey: [tenantSlug, "catalog-columns", dataSourceID],
    queryFn: () =>
      api.get<ColumnsResponse>(
        `/api/v1/catalog/columns?tenant_id=${tenantSlug}&data_source_id=${dataSourceID}`
      ),
    enabled: dataSourceID.length > 0,
  });

  const classifyMutation = useMutation({
    mutationFn: ({ columnID, classification, tags }: { columnID: string; classification: string; tags: string[] }) =>
      api.post("/api/v1/catalog/classify", {
        tenant_id: tenantSlug,
        column_id: columnID,
        classification,
        tags,
      }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: [tenantSlug, "catalog-columns"] }),
  });

  const bulkClassifyMutation = useMutation({
    mutationFn: (columnIDs: string[]) =>
      api.post("/api/v1/catalog/bulk-classify", {
        tenant_id: tenantSlug,
        column_ids: columnIDs,
        classification: bulkClass,
        tags: [],
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: [tenantSlug, "catalog-columns"] });
      setSelected(new Set());
    },
  });

  const triggerCrawlMutation = useMutation({
    mutationFn: () =>
      api.post("/api/v1/catalog/crawl", { tenant_id: tenantSlug, data_source_id: dataSourceID }),
  });

  const columns: Column[] = data?.items ?? [];

  const filtered = useMemo(() => {
    return columns.filter((c) => {
      if (filterQuarantine && !c.quarantine) return false;
      if (filterClass && c.classification !== filterClass) return false;
      if (filterTable && !c.tableName.includes(filterTable)) return false;
      return true;
    });
  }, [columns, filterQuarantine, filterClass, filterTable]);

  const counts = useMemo(() => {
    const total = columns.length;
    const quarantined = columns.filter((c) => c.quarantine).length;
    const unclassified = columns.filter((c) => !c.classification).length;
    const byClass: Record<string, number> = {};
    for (const c of columns) {
      if (c.classification) byClass[c.classification] = (byClass[c.classification] ?? 0) + 1;
    }
    return { total, quarantined, unclassified, byClass };
  }, [columns]);

  const toggleSelect = (id: string) => {
    setSelected((prev) => {
      const next = new Set(prev);
      next.has(id) ? next.delete(id) : next.add(id);
      return next;
    });
  };

  const selectAll = () => setSelected(new Set(filtered.map((c) => c.id)));
  const clearAll = () => setSelected(new Set());

  return (
    <div className="space-y-6">
      <div className="flex items-start justify-between">
        <div>
          <h1 className="text-2xl font-bold">Schema Classifications</h1>
          <p className="text-muted-foreground text-sm">
            Classify columns to control AI visibility and access governance.
          </p>
        </div>
        <Button
          variant="outline"
          size="sm"
          disabled={!dataSourceID || triggerCrawlMutation.isPending}
          onClick={() => triggerCrawlMutation.mutate()}
        >
          {triggerCrawlMutation.isPending ? "Crawling…" : "Refresh Now"}
        </Button>
      </div>

      {/* Data source selector */}
      <div className="flex items-center gap-2">
        <input
          className="border rounded px-3 py-1.5 text-sm w-80"
          placeholder="Data Source ID (UUID)"
          value={dataSourceID}
          onChange={(e) => setDataSourceID(e.target.value)}
        />
      </div>

      {/* Summary counts */}
      {dataSourceID && !isLoading && (
        <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
          <StatCard label="Total columns" value={counts.total} />
          <StatCard label="Quarantined" value={counts.quarantined} danger={counts.quarantined > 0} />
          <StatCard label="Unclassified" value={counts.unclassified} danger={counts.unclassified > 0} />
          {Object.entries(counts.byClass).map(([cls, n]) => (
            <StatCard key={cls} label={cls} value={n} />
          ))}
        </div>
      )}

      {/* Filters */}
      <div className="flex flex-wrap items-center gap-2">
        <label className="flex items-center gap-1.5 text-sm">
          <input
            type="checkbox"
            checked={filterQuarantine}
            onChange={(e) => setFilterQuarantine(e.target.checked)}
          />
          Quarantined only
        </label>
        <select
          className="border rounded px-2 py-1 text-sm"
          value={filterClass}
          onChange={(e) => setFilterClass(e.target.value)}
        >
          <option value="">All classifications</option>
          {CLASSIFICATIONS.map((c) => (
            <option key={c} value={c}>{c}</option>
          ))}
        </select>
        <input
          className="border rounded px-2 py-1 text-sm"
          placeholder="Filter by table name"
          value={filterTable}
          onChange={(e) => setFilterTable(e.target.value)}
        />
      </div>

      {/* Bulk actions */}
      {selected.size > 0 && (
        <div className="flex items-center gap-3 p-3 bg-muted rounded-lg">
          <span className="text-sm font-medium">{selected.size} selected</span>
          <select
            className="border rounded px-2 py-1 text-sm"
            value={bulkClass}
            onChange={(e) => setBulkClass(e.target.value as Classification)}
          >
            {CLASSIFICATIONS.map((c) => (
              <option key={c} value={c}>{c}</option>
            ))}
          </select>
          <Button
            size="sm"
            disabled={bulkClassifyMutation.isPending}
            onClick={() => bulkClassifyMutation.mutate(Array.from(selected))}
          >
            {bulkClassifyMutation.isPending ? "Applying…" : `Classify as ${bulkClass}`}
          </Button>
          <Button variant="ghost" size="sm" onClick={clearAll}>
            Clear
          </Button>
        </div>
      )}

      {/* Select all / clear */}
      {filtered.length > 0 && (
        <div className="flex items-center gap-2">
          <Button variant="ghost" size="sm" onClick={selectAll}>
            Select all {filtered.length}
          </Button>
          {selected.size > 0 && (
            <Button variant="ghost" size="sm" onClick={clearAll}>
              Clear selection
            </Button>
          )}
        </div>
      )}

      {/* Column list */}
      {isLoading && <p className="text-muted-foreground">Loading columns…</p>}

      {!isLoading && !dataSourceID && (
        <p className="text-muted-foreground text-sm">Enter a Data Source ID to view schema columns.</p>
      )}

      <div className="space-y-2">
        {filtered.map((col) => (
          <Card
            key={col.id}
            className={selected.has(col.id) ? "ring-2 ring-primary" : ""}
            onClick={() => toggleSelect(col.id)}
            style={{ cursor: "pointer" }}
          >
            <CardHeader className="py-3 px-4">
              <div className="flex items-start justify-between gap-4">
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-2 flex-wrap">
                    {col.quarantine && (
                      <Badge variant="destructive" className="text-xs">quarantined</Badge>
                    )}
                    <span className="font-mono text-sm font-medium">
                      {col.schemaName}.{col.tableName}.{col.columnName}
                    </span>
                    <Badge variant="outline" className="text-xs font-mono">{col.dataType}</Badge>
                    {col.classification && (
                      <Badge
                        variant={classificationVariant[col.classification as Classification] ?? "outline"}
                        className="text-xs"
                      >
                        {col.classification}
                      </Badge>
                    )}
                    {col.tags?.map((tag) => (
                      <Badge key={tag} variant="secondary" className="text-xs">{tag}</Badge>
                    ))}
                  </div>
                  {col.sampleValues && col.sampleValues.length > 0 && (
                    <p className="text-xs text-muted-foreground mt-1 font-mono truncate">
                      samples: {col.sampleValues.slice(0, 5).join(", ")}
                    </p>
                  )}
                  {col.lastCrawledAt && (
                    <p className="text-xs text-muted-foreground mt-0.5">
                      crawled {formatDate(col.lastCrawledAt)} · by {col.classifiedBy}
                    </p>
                  )}
                </div>

                {/* Inline classify buttons */}
                <div
                  className="flex gap-1 shrink-0"
                  onClick={(e) => e.stopPropagation()}
                >
                  {CLASSIFICATIONS.map((cls) => (
                    <Button
                      key={cls}
                      variant={col.classification === cls ? "default" : "ghost"}
                      size="sm"
                      className="text-xs h-7 px-2"
                      disabled={classifyMutation.isPending}
                      onClick={() =>
                        classifyMutation.mutate({ columnID: col.id, classification: cls, tags: [] })
                      }
                    >
                      {cls}
                    </Button>
                  ))}
                </div>
              </div>
            </CardHeader>
          </Card>
        ))}

        {!isLoading && dataSourceID && filtered.length === 0 && (
          <p className="text-muted-foreground text-sm">
            No columns match the current filters. Try adjusting filters or trigger a crawl.
          </p>
        )}
      </div>
    </div>
  );
}

function StatCard({ label, value, danger }: { label: string; value: number; danger?: boolean }) {
  return (
    <Card>
      <CardContent className="pt-4 pb-3 px-4">
        <div className={`text-2xl font-bold ${danger && value > 0 ? "text-destructive" : ""}`}>
          {value}
        </div>
        <div className="text-xs text-muted-foreground mt-0.5">{label}</div>
      </CardContent>
    </Card>
  );
}
