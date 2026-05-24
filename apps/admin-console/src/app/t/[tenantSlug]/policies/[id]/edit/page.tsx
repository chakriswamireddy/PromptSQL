"use client";

import { useState, useEffect } from "react";
import { useRouter } from "next/navigation";
import Link from "next/link";
import { usePolicy, useUpdatePolicy, useSubmitPolicy } from "@/hooks/use-policies";
import { PolicyEditor } from "@/components/policy-editor";
import { SimulatorPanel } from "@/components/simulator/panel";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { ApiError } from "@/lib/api-client";
import { ArrowLeft } from "lucide-react";

export default function EditPolicyPage({
  params,
}: {
  params: { tenantSlug: string; id: string };
}) {
  const { tenantSlug, id } = params;
  const router = useRouter();

  const { data: policy, isLoading } = usePolicy(tenantSlug, id);
  const update = useUpdatePolicy(tenantSlug, id);
  const submit = useSubmitPolicy(tenantSlug, id);

  const [value, setValue] = useState("");
  const [isValid, setIsValid] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [showSimulator, setShowSimulator] = useState(false);

  useEffect(() => {
    if (policy) {
      setValue(
        JSON.stringify(
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
          },
          null,
          2
        )
      );
    }
  }, [policy]);

  async function handleSave() {
    setError(null);
    try {
      const parsed = JSON.parse(value);
      await update.mutateAsync({ draft: parsed, etag: policy?.etag });
    } catch (err) {
      if (err instanceof ApiError) {
        if (err.status === 409)
          setError("Conflict: another admin edited this draft. Reload and reapply changes.");
        else setError(err.message);
      } else {
        setError("Save failed.");
      }
    }
  }

  async function handleSubmit() {
    setError(null);
    try {
      // Save first, then submit.
      const parsed = JSON.parse(value);
      await update.mutateAsync({ draft: parsed, etag: policy?.etag });
      await submit.mutateAsync();
      router.push(`/t/${tenantSlug}/policies/${id}`);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Submit failed.");
    }
  }

  if (isLoading) return <p className="text-muted-foreground">Loading…</p>;
  if (!policy) return <p className="text-destructive">Policy not found.</p>;
  if (policy.status !== "draft") {
    return (
      <div className="text-center py-16 text-muted-foreground">
        Only draft policies can be edited.{" "}
        <Link href={`/t/${tenantSlug}/policies/${id}`} className="text-primary underline">
          View policy
        </Link>
      </div>
    );
  }

  return (
    <div className="space-y-4 h-[calc(100vh-5rem)] flex flex-col">
      <div className="shrink-0 flex items-center gap-3">
        <Link href={`/t/${tenantSlug}/policies/${id}`}>
          <Button variant="ghost" size="sm">
            <ArrowLeft className="h-4 w-4 mr-1" /> Back
          </Button>
        </Link>
        <h1 className="text-xl font-bold">Edit draft — {policy.name}</h1>
        <Button size="sm" variant="outline" onClick={() => setShowSimulator(!showSimulator)}>
          {showSimulator ? "Hide simulator" : "Simulate"}
        </Button>
      </div>

      {error && (
        <Card className="shrink-0 border-destructive">
          <CardContent className="py-3 text-destructive text-sm">{error}</CardContent>
        </Card>
      )}

      {showSimulator && (
        <div className="shrink-0">
          <SimulatorPanel tenantSlug={tenantSlug} draftPolicyId={id} />
        </div>
      )}

      <div className="flex-1 min-h-0">
        <PolicyEditor
          value={value}
          onChange={setValue}
          onValidate={(v) => setIsValid(v)}
          onSaveDraft={handleSave}
          onSubmitForReview={handleSubmit}
          onSimulate={() => setShowSimulator(true)}
          isSaving={update.isPending}
          isSubmitting={submit.isPending}
          status={policy.status}
        />
      </div>
    </div>
  );
}
