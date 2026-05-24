import { type NextRequest } from 'next/server';

// BFF proxy — forwards the PEP /ask SSE stream from the orchestrator to the browser.
// Adds auth headers from the session cookie before forwarding.

const ORCHESTRATOR_BASE = process.env.AI_ORCHESTRATOR_ADDR ?? 'http://ai-orchestrator:8084';

export async function POST(req: NextRequest) {
  const body = await req.json();

  // Session headers injected by Next.js middleware (or auth layer)
  const tenantId = req.headers.get('x-tenant-id') ?? '';
  const userId   = req.headers.get('x-user-id')   ?? '';
  const dbToken  = req.headers.get('x-db-token')  ?? '';
  const authToken = req.headers.get('authorization') ?? '';

  if (!tenantId || !userId) {
    return new Response(JSON.stringify({ code: 'unauthenticated' }), { status: 401 });
  }

  const upstream = await fetch(`${ORCHESTRATOR_BASE}/v1/ai/pep/ask`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'Authorization': authToken,
      'X-Tenant-Id': tenantId,
      'X-User-Id': userId,
      'X-DB-Token': dbToken,
    },
    body: JSON.stringify(body),
  });

  if (!upstream.ok || !upstream.body) {
    const err = await upstream.text();
    return new Response(err, { status: upstream.status });
  }

  // Pipe the SSE stream through
  return new Response(upstream.body, {
    status: 200,
    headers: {
      'Content-Type': 'text/event-stream',
      'Cache-Control': 'no-cache',
      'Connection': 'keep-alive',
    },
  });
}
