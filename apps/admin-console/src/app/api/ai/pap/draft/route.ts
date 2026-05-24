import { type NextRequest } from 'next/server';

const ORCHESTRATOR_URL = process.env.AI_ORCHESTRATOR_URL ?? 'http://ai-orchestrator:8084';

export async function POST(req: NextRequest) {
  const body = await req.text();

  const upstream = await fetch(`${ORCHESTRATOR_URL}/v1/ai/pap/draft`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'X-Tenant-Id': req.headers.get('x-tenant-id') ?? '',
      'X-User-Id':   req.headers.get('x-user-id') ?? '',
      Authorization: req.headers.get('authorization') ?? '',
    },
    body,
  });

  // Pass SSE stream or JSON through transparently
  return new Response(upstream.body, {
    status: upstream.status,
    headers: {
      'Content-Type': upstream.headers.get('content-type') ?? 'application/json',
      'Cache-Control': 'no-cache',
      Connection: 'keep-alive',
    },
  });
}
