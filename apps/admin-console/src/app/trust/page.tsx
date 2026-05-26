"use client";

import { useEffect, useState } from "react";

interface Subprocessor {
  id: string;
  name: string;
  purpose: string;
  location: string;
  data_types: string[];
  dpa_url?: string;
}

interface Certification {
  name: string;
  status: string;
  description: string;
  badge: string;
}

const CERTIFICATIONS: Certification[] = [
  { name: "SOC 2 Type II", status: "In Audit", description: "Security, Availability, and Confidentiality trust service criteria. Evidence window: 6 months.", badge: "🔄" },
  { name: "ISO 27001", status: "In Progress", description: "Information Security Management System — parallel track with SOC 2.", badge: "🔄" },
  { name: "HIPAA Ready", status: "Available", description: "HIPAA-ready configuration available for healthcare tenants. BAA on request.", badge: "✅" },
  { name: "GDPR", status: "Compliant", description: "Data processing agreements, subject access requests, and residency controls.", badge: "✅" },
];

export default function TrustPage() {
  const [subprocessors, setSubprocessors] = useState<Subprocessor[]>([]);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    fetch("/api/trust/subprocessors")
      .then((r) => r.json())
      .then((d) => setSubprocessors(d.subprocessors ?? []))
      .catch(console.error)
      .finally(() => setLoading(false));
  }, []);

  return (
    <div className="p-6 space-y-8 max-w-5xl">
      <div>
        <h1 className="text-2xl font-bold text-gray-900">Trust Center</h1>
        <p className="mt-1 text-sm text-gray-500">
          Security certifications, sub-processors, and compliance posture.
        </p>
      </div>

      {/* Certifications */}
      <section>
        <h2 className="text-lg font-semibold text-gray-800 mb-3">Certifications & Compliance</h2>
        <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
          {CERTIFICATIONS.map((cert) => (
            <div key={cert.name} className="rounded-lg border border-gray-200 bg-white p-4 shadow-sm">
              <div className="flex items-center gap-2 mb-1">
                <span className="text-xl">{cert.badge}</span>
                <span className="font-semibold text-gray-900">{cert.name}</span>
                <span className={`ml-auto text-xs px-2 py-0.5 rounded-full font-medium ${
                  cert.status === "Compliant" || cert.status === "Available"
                    ? "bg-green-100 text-green-700"
                    : "bg-yellow-100 text-yellow-700"
                }`}>
                  {cert.status}
                </span>
              </div>
              <p className="text-sm text-gray-600">{cert.description}</p>
            </div>
          ))}
        </div>
      </section>

      {/* Security FAQ */}
      <section>
        <h2 className="text-lg font-semibold text-gray-800 mb-3">Security FAQ</h2>
        <div className="space-y-3">
          {[
            { q: "Where is data stored?", a: "All data is stored in AWS (us-east-1 by default, eu-west-1 for EU-residency tenants). No data leaves your designated region." },
            { q: "How are secrets managed?", a: "All secrets are managed by HashiCorp Vault with dynamic credentials, automatic rotation, and per-service leases." },
            { q: "Is data encrypted at rest?", a: "Yes. All PostgreSQL, S3, and ElastiCache data is encrypted using AES-256. HIPAA tenants use customer-managed KMS keys." },
            { q: "How is audit data protected?", a: "Audit events are written to an append-only ClickHouse cluster and S3 WORM Object Lock (Compliance mode). Hash-chains prevent tampering." },
            { q: "What is the vulnerability disclosure policy?", a: "Critical findings are addressed within 24 hours. High within 7 days. Medium within 30 days. Report via security@platform.io." },
          ].map(({ q, a }) => (
            <details key={q} className="border border-gray-200 rounded-lg bg-white px-4 py-3">
              <summary className="font-medium text-gray-800 cursor-pointer">{q}</summary>
              <p className="mt-2 text-sm text-gray-600">{a}</p>
            </details>
          ))}
        </div>
      </section>

      {/* Sub-processors */}
      <section>
        <h2 className="text-lg font-semibold text-gray-800 mb-3">Sub-Processors</h2>
        <p className="text-sm text-gray-500 mb-3">
          We will provide 30 days notice of any changes to this list via email and the changelog.
        </p>
        {loading ? (
          <p className="text-sm text-gray-400">Loading…</p>
        ) : (
          <div className="overflow-x-auto">
            <table className="min-w-full text-sm border border-gray-200 rounded-lg overflow-hidden">
              <thead className="bg-gray-50">
                <tr>
                  {["Name", "Purpose", "Location", "Data Types"].map((h) => (
                    <th key={h} className="px-4 py-2 text-left font-medium text-gray-600">{h}</th>
                  ))}
                </tr>
              </thead>
              <tbody className="bg-white divide-y divide-gray-100">
                {subprocessors.map((sp) => (
                  <tr key={sp.id} className="hover:bg-gray-50">
                    <td className="px-4 py-2 font-medium text-gray-900">
                      {sp.dpa_url ? (
                        <a href={sp.dpa_url} target="_blank" rel="noreferrer" className="underline text-blue-600">{sp.name}</a>
                      ) : sp.name}
                    </td>
                    <td className="px-4 py-2 text-gray-600">{sp.purpose}</td>
                    <td className="px-4 py-2 text-gray-600">{sp.location}</td>
                    <td className="px-4 py-2 text-gray-600">{sp.data_types.join(", ")}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>

      {/* Contact */}
      <section className="rounded-lg border border-blue-100 bg-blue-50 p-4">
        <h2 className="text-sm font-semibold text-blue-800">Security Contact</h2>
        <p className="mt-1 text-sm text-blue-700">
          For security disclosures, pentest reports, or compliance inquiries:{" "}
          <a href="mailto:security@platform.io" className="underline">security@platform.io</a>
        </p>
      </section>
    </div>
  );
}
