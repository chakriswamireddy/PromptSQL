import type { FastifyInstance } from 'fastify';

export async function buildHealthRouter(app: FastifyInstance) {
  app.get('/healthz', async () => ({ status: 'ok' }));
  app.get('/readyz', async () => ({ status: 'ready' }));
}
