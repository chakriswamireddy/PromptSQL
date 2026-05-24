"use client";

import { useState } from "react";
import { useRouter } from "next/navigation";
import { PolicyEditor } from "@/components/policy-editor";
import { useCreatePolicy } from "@/hooks/use-policies";
import { ApiError } from "@/lib/api-client";
import { POLICY_TEMPLATE } from "@/lib/policy-schema";
import { Card, CardContent } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { ArrowLeft } from "lucide-react";
import Link from "next/link";

export default function NewPolicyPage({ params }: { params: { tenantSlug: string } }) {
  const { tenantSlug } = params;
  const router = useRouter();
  const [value, setValue] = useState(POLICY_TEMPLATE);
  const [error, setError] = useState<string | null>(null);
  const [isValid, setIsValid] = useState(false);

  const create = useCreatePolicy(tenantSlug);

  async function handleSave() {
    setError(null);
    try {
      const parsed = JSON.parse(value);
      const policy = await create.mutateAsync(parsed);
      router.push(`/t/${tenantSlug}/policies/${policy.id}`);
    } catch (err) {
      if (err instanceof ApiError) {
        setError(err.message);
      } else if (err instanceof SyntaxError) {
        setError("Invalid JSON — fix syntax errors before saving.");
      } else {
        setError("Failed to create policy.");
      }
    }
  }

  return (
    <div className="space-y-4 h-[calc(100vh-5rem)] flex flex-col">
      <div className="flex items-center gap-3 shrink-0">
        <Link href={`/t/${tenantSlug}/policies`}>
          <Button variant="ghost" size="sm">
            <ArrowLeft className="h-4 w-4 mr-1" /> Policies
          </Button>
        </Link>
        <h1 className="text-xl font-bold">New policy</h1>
      </div>

      {error && (
        <Card className="shrink-0 border-destructive">
          <CardContent className="py-3 text-destructive text-sm">{error}</CardContent>
        </Card>
      )}

      <div className="flex-1 min-h-0">
        <PolicyEditor
          value={value}
          onChange={setValue}
          onValidate={(v) => setIsValid(v)}
          onSaveDraft={handleSave}
          isSaving={create.isPending}
          status="draft"
        />
      </div>
    </div>
  );
}
