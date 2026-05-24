"use client";

import { SimulatorPanel } from "@/components/simulator/panel";

export default function SimulatorPage({ params }: { params: { tenantSlug: string } }) {
  const { tenantSlug } = params;
  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold">Simulator</h1>
        <p className="text-muted-foreground text-sm mt-1">
          Test policy decisions against real subjects or saved personas before deploying.
        </p>
      </div>
      <SimulatorPanel tenantSlug={tenantSlug} />
    </div>
  );
}
