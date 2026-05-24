"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import {
  ShieldCheck,
  FileText,
  Users,
  UserCircle2,
  Database,
  Activity,
  FlaskConical,
} from "lucide-react";
import { cn } from "@/lib/utils";

interface NavItem {
  label: string;
  href: string;
  icon: React.ReactNode;
}

function navItems(slug: string): NavItem[] {
  const base = `/t/${slug}`;
  return [
    { label: "Policies", href: `${base}/policies`, icon: <FileText className="h-4 w-4" /> },
    { label: "Simulator", href: `${base}/simulator`, icon: <FlaskConical className="h-4 w-4" /> },
    { label: "Users", href: `${base}/users`, icon: <Users className="h-4 w-4" /> },
    { label: "Roles", href: `${base}/roles`, icon: <UserCircle2 className="h-4 w-4" /> },
    { label: "Data Sources", href: `${base}/data-sources`, icon: <Database className="h-4 w-4" /> },
    { label: "Audit Trail", href: `${base}/audit`, icon: <Activity className="h-4 w-4" /> },
  ];
}

export function SideNav({ tenantSlug }: { tenantSlug: string }) {
  const pathname = usePathname();

  return (
    <aside className="w-56 flex-shrink-0 border-r bg-muted/20 flex flex-col">
      <div className="flex items-center gap-2 px-4 py-5 border-b">
        <ShieldCheck className="h-5 w-5 text-primary" />
        <span className="font-semibold text-sm truncate">{tenantSlug}</span>
      </div>

      <nav className="flex-1 px-2 py-4 space-y-0.5" aria-label="Main navigation">
        {navItems(tenantSlug).map((item) => {
          const active =
            pathname === item.href || pathname.startsWith(item.href + "/");
          return (
            <Link
              key={item.href}
              href={item.href}
              className={cn(
                "flex items-center gap-3 rounded-md px-3 py-2 text-sm transition-colors",
                active
                  ? "bg-primary text-primary-foreground"
                  : "text-muted-foreground hover:bg-accent hover:text-accent-foreground"
              )}
              aria-current={active ? "page" : undefined}
            >
              {item.icon}
              {item.label}
            </Link>
          );
        })}
      </nav>

      <div className="border-t px-2 py-3 text-xs text-muted-foreground text-center">
        Governance Platform v0.4
      </div>
    </aside>
  );
}
