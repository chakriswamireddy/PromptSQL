import { SideNav } from "@/components/side-nav";

export default function TenantLayout({
  children,
  params,
}: {
  children: React.ReactNode;
  params: { tenantSlug: string };
}) {
  return (
    <div className="flex h-screen overflow-hidden bg-background">
      <SideNav tenantSlug={params.tenantSlug} />
      <main className="flex-1 overflow-y-auto p-6">{children}</main>
    </div>
  );
}
