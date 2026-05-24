import { type NextRequest } from 'next/server';

const ORCHESTRATOR_BASE = process.env.AI_ORCHESTRATOR_ADDR ?? 'http://ai-orchestrator:8084';

export async function POST(req: NextRequest) {
  const body      = await req.json();
  const tenantId  = req.headers.get('x-tenant-id') ?? '';
  const userId    = req.headers.get('x-user-id')   ?? '';
  const authToken = req.headers.get('authorization') ?? '';

  if (!tenantId || !userId) {
    return new Response(JSON.stringify({ code: 'unauthenticated' }), { status: 401 });
  }

  const upstream = await fetch(`${ORCHESTRATOR_BASE}/v1/ai/pep/feedback`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'Authorization': authToken,
      'X-Tenant-Id': tenantId,
      'X-User-Id': userId,
    },
    body: JSON.stringify(body),
  });
  const data = await upstream.text();
  return new Response(data, { status: upstream.status, headers: { 'Content-Type': 'application/json' } });
}
