import { redirect } from "next/navigation";

export default function TenantRoot({ params }: { params: { tenantSlug: string } }) {
  redirect(`/t/${params.tenantSlug}/policies`);
}
