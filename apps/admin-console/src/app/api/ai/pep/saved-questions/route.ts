import { type NextRequest } from 'next/server';

const ORCHESTRATOR_BASE = process.env.AI_ORCHESTRATOR_ADDR ?? 'http://ai-orchestrator:8084';

function baseHeaders(req: NextRequest): Record<string, string> {
  return {
    'Authorization': req.headers.get('authorization') ?? '',
    'X-Tenant-Id':   req.headers.get('x-tenant-id') ?? '',
    'X-User-Id':     req.headers.get('x-user-id') ?? '',
  };
}

export async function GET(req: NextRequest) {
  const upstream = await fetch(`${ORCHESTRATOR_BASE}/v1/ai/pep/saved-questions`, {
    headers: baseHeaders(req),
  });
  const data = await upstream.text();
  return new Response(data, { status: upstream.status, headers: { 'Content-Type': 'application/json' } });
}

export async function POST(req: NextRequest) {
  const body = await req.json();
  const upstream = await fetch(`${ORCHESTRATOR_BASE}/v1/ai/pep/saved-questions`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', ...baseHeaders(req) },
    body: JSON.stringify(body),
  });
  const data = await upstream.text();
  return new Response(data, { status: upstream.status, headers: { 'Content-Type': 'application/json' } });
}
