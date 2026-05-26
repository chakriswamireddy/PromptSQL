"use client";

import { useEffect, useState } from "react";

interface AccessReview {
  id: string;
  review_period: string;
  generated_at: string;
  due_at: string;
  total_entries: number;
  certified_count: number;
  revoked_count: number;
  status: "pending" | "in_progress" | "completed" | "overdue";
}

interface EvidenceItem {
  id: string;
  control_id: string;
  framework: string;
  evidence_type: string;
  collected_at: string;
  expires_at?: string;
  status: "valid" | "stale" | "missing" | "exempt";
  evidence_ref: string;
}

interface GDPRRequest {
  id: string;
  subject_email: string;
  request_type: string;
  status: string;
  submitted_at: string;
  due_at: string;
}

const STATUS_COLORS: Record<string, string> = {
  valid:       "bg-green-100 text-green-700",
  stale:       "bg-yellow-100 text-yellow-700",
  missing:     "bg-red-100 text-red-700",
  exempt:      "bg-gray-100 text-gray-500",
  completed:   "bg-green-100 text-green-700",
  pending:     "bg-yellow-100 text-yellow-700",
  in_progress: "bg-blue-100 text-blue-700",
  overdue:     "bg-red-100 text-red-700",
  processing:  "bg-blue-100 text-blue-700",
  rejected:    "bg-red-100 text-red-700",
};

export default function CompliancePage() {
  const [tab, setTab] = useState<"reviews" | "evidence" | "gdpr">("reviews");
  const [reviews, setReviews] = useState<AccessReview[]>([]);
  const [evidence, setEvidence] = useState<EvidenceItem[]>([]);
  const [gdprRequests, setGDPRRequests] = useState<GDPRRequest[]>([]);
  const [loading, setLoading] = useState(false);

  const tenantId = "current"; // resolved from session on BFF

  useEffect(() => {
    setLoading(true);
    const paths: Record<typeof tab, string> = {
      reviews:  `/api/admin/${tenantId}/access-reviews`,
      evidence: `/api/admin/${tenantId}/compliance/evidence`,
      gdpr:     `/api/admin/${tenantId}/gdpr/requests`,
    };
    fetch(paths[tab])
      .then((r) => r.json())
      .then((d) => {
        if (tab === "reviews") setReviews(d.reviews ?? []);
        if (tab === "evidence") setEvidence(d.evidence ?? []);
        if (tab === "gdpr") setGDPRRequests(d.requests ?? []);
      })
      .catch(console.error)
      .finally(() => setLoading(false));
  }, [tab]);

  const generateReview = async () => {
    await fetch(`/api/admin/${tenantId}/access-reviews/generate`, { method: "POST" });
    setTab("reviews");
  };

  const exportSIEM = (format: "cef" | "json") => {
    const from = new Date(Date.now() - 86400_000).toISOString();
    const to = new Date().toISOString();
    window.open(`/api/admin/${tenantId}/audit/export/siem?format=${format}&from=${from}&to=${to}`);
  };

  const tabs = [
    { key: "reviews" as const, label: "Access Reviews" },
    { key: "evidence" as const, label: "Compliance Evidence" },
    { key: "gdpr" as const, label: "GDPR Requests" },
  ];

  return (
    <div className="p-6 space-y-6 max-w-6xl">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold text-gray-900">Compliance</h1>
          <p className="mt-1 text-sm text-gray-500">SOC 2 evidence, access reviews, and GDPR requests.</p>
        </div>
        <div className="flex gap-2">
          <button onClick={generateReview} className="rounded-md bg-indigo-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-indigo-700">
            Generate Access Review
          </button>
          <button onClick={() => exportSIEM("json")} className="rounded-md border border-gray-300 px-3 py-1.5 text-sm font-medium text-gray-700 hover:bg-gray-50">
            Export SIEM (JSON)
          </button>
          <button onClick={() => exportSIEM("cef")} className="rounded-md border border-gray-300 px-3 py-1.5 text-sm font-medium text-gray-700 hover:bg-gray-50">
            Export SIEM (CEF)
          </button>
        </div>
      </div>

      {/* Tabs */}
      <div className="border-b border-gray-200">
        <nav className="flex gap-4">
          {tabs.map(({ key, label }) => (
            <button
              key={key}
              onClick={() => setTab(key)}
              className={`pb-2 text-sm font-medium border-b-2 transition-colors ${
                tab === key
                  ? "border-indigo-600 text-indigo-600"
                  : "border-transparent text-gray-500 hover:text-gray-700"
              }`}
            >
              {label}
            </button>
          ))}
        </nav>
      </div>

      {loading && <p className="text-sm text-gray-400">Loading…</p>}

      {/* Access Reviews */}
      {tab === "reviews" && !loading && (
        <div className="overflow-x-auto">
          <table className="min-w-full text-sm border border-gray-200 rounded-lg overflow-hidden">
            <thead className="bg-gray-50">
              <tr>
                {["Period", "Generated", "Due", "Total", "Certified", "Revoked", "Status", ""].map((h) => (
                  <th key={h} className="px-4 py-2 text-left font-medium text-gray-600">{h}</th>
                ))}
              </tr>
            </thead>
            <tbody className="bg-white divide-y divide-gray-100">
              {reviews.map((r) => (
                <tr key={r.id} className="hover:bg-gray-50">
                  <td className="px-4 py-2 font-medium">{r.review_period}</td>
                  <td className="px-4 py-2 text-gray-500">{new Date(r.generated_at).toLocaleDateString()}</td>
                  <td className="px-4 py-2 text-gray-500">{new Date(r.due_at).toLocaleDateString()}</td>
                  <td className="px-4 py-2">{r.total_entries}</td>
                  <td className="px-4 py-2 text-green-600">{r.certified_count}</td>
                  <td className="px-4 py-2 text-red-600">{r.revoked_count}</td>
                  <td className="px-4 py-2">
                    <span className={`text-xs px-2 py-0.5 rounded-full font-medium ${STATUS_COLORS[r.status] ?? "bg-gray-100 text-gray-600"}`}>
                      {r.status}
                    </span>
                  </td>
                  <td className="px-4 py-2">
                    <a href={`/compliance/reviews/${r.id}`} className="text-indigo-600 hover:underline text-xs">Review</a>
                  </td>
                </tr>
              ))}
              {reviews.length === 0 && (
                <tr><td colSpan={8} className="px-4 py-6 text-center text-gray-400">No access reviews yet. Click "Generate Access Review" to create one.</td></tr>
              )}
            </tbody>
          </table>
        </div>
      )}

      {/* Compliance Evidence */}
      {tab === "evidence" && !loading && (
        <div className="overflow-x-auto">
          <table className="min-w-full text-sm border border-gray-200 rounded-lg overflow-hidden">
            <thead className="bg-gray-50">
              <tr>
                {["Framework", "Control", "Type", "Collected", "Expires", "Ref", "Status"].map((h) => (
                  <th key={h} className="px-4 py-2 text-left font-medium text-gray-600">{h}</th>
                ))}
              </tr>
            </thead>
            <tbody className="bg-white divide-y divide-gray-100">
              {evidence.map((e) => (
                <tr key={e.id} className="hover:bg-gray-50">
                  <td className="px-4 py-2 font-medium">{e.framework}</td>
                  <td className="px-4 py-2 font-mono text-xs">{e.control_id}</td>
                  <td className="px-4 py-2 text-gray-600">{e.evidence_type.replace(/_/g, " ")}</td>
                  <td className="px-4 py-2 text-gray-500">{new Date(e.collected_at).toLocaleDateString()}</td>
                  <td className="px-4 py-2 text-gray-500">{e.expires_at ? new Date(e.expires_at).toLocaleDateString() : "—"}</td>
                  <td className="px-4 py-2 text-xs font-mono text-gray-500 truncate max-w-xs">{e.evidence_ref}</td>
                  <td className="px-4 py-2">
                    <span className={`text-xs px-2 py-0.5 rounded-full font-medium ${STATUS_COLORS[e.status] ?? "bg-gray-100"}`}>
                      {e.status}
                    </span>
                  </td>
                </tr>
              ))}
              {evidence.length === 0 && (
                <tr><td colSpan={7} className="px-4 py-6 text-center text-gray-400">No evidence collected yet.</td></tr>
              )}
            </tbody>
          </table>
        </div>
      )}

      {/* GDPR Requests */}
      {tab === "gdpr" && !loading && (
        <div className="space-y-4">
          <div className="overflow-x-auto">
            <table className="min-w-full text-sm border border-gray-200 rounded-lg overflow-hidden">
              <thead className="bg-gray-50">
                <tr>
                  {["Subject", "Type", "Submitted", "Due", "Status"].map((h) => (
                    <th key={h} className="px-4 py-2 text-left font-medium text-gray-600">{h}</th>
                  ))}
                </tr>
              </thead>
              <tbody className="bg-white divide-y divide-gray-100">
                {gdprRequests.map((r) => (
                  <tr key={r.id} className="hover:bg-gray-50">
                    <td className="px-4 py-2 font-medium">{r.subject_email}</td>
                    <td className="px-4 py-2 text-gray-600">{r.request_type}</td>
                    <td className="px-4 py-2 text-gray-500">{new Date(r.submitted_at).toLocaleDateString()}</td>
                    <td className={`px-4 py-2 ${new Date(r.due_at) < new Date() && r.status !== "completed" ? "text-red-600 font-medium" : "text-gray-500"}`}>
                      {new Date(r.due_at).toLocaleDateString()}
                    </td>
                    <td className="px-4 py-2">
                      <span className={`text-xs px-2 py-0.5 rounded-full font-medium ${STATUS_COLORS[r.status] ?? "bg-gray-100"}`}>
                        {r.status}
                      </span>
                    </td>
                  </tr>
                ))}
                {gdprRequests.length === 0 && (
                  <tr><td colSpan={5} className="px-4 py-6 text-center text-gray-400">No GDPR requests.</td></tr>
                )}
              </tbody>
            </table>
          </div>
          <p className="text-xs text-gray-400">GDPR Subject Access Requests must be fulfilled within 30 days (Art. 12).</p>
        </div>
      )}
    </div>
  );
}
