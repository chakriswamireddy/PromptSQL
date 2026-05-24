import { loadConfig } from './config';

// OTel must initialize before any other import
const cfg = loadConfig();
import { initTelemetry } from './telemetry';
const sdk = initTelemetry({
  endpoint: cfg.OTEL_EXPORTER_OTLP_ENDPOINT,
  samplingRate: cfg.OTEL_SAMPLING_RATE,
  version: cfg.VERSION,
  environment: cfg.DEPLOYMENT_ENVIRONMENT,
});

import Fastify from 'fastify';
import cors from '@fastify/cors';
import { Pool } from 'pg';
import { createClient } from 'redis';
import { initialize as initUnleash } from 'unleash-client';
import { registry } from './metrics';
import { buildPapRouter } from './routes/pap';
import { buildPepRouter } from './routes/pep';
import { buildHealthRouter } from './routes/health';

async function main() {
  // ─── Feature flag check ──────────────────────────────────────────────────
  const unleash = initUnleash({
    url: cfg.UNLEASH_URL,
    appName: 'ai-orchestrator',
    customHeaders: { Authorization: cfg.UNLEASH_API_TOKEN },
  });
  await new Promise<void>((res) => unleash.on('synchronized', res));

  const papEnabled = unleash.isEnabled('ai-pap-graph');
  const pepEnabled = unleash.isEnabled('ai-pep-graph');

  if (!papEnabled && !pepEnabled) {
    console.log('Both ai-pap-graph and ai-pep-graph are disabled — exiting cleanly.');
    process.exit(0);
  }

  // ─── Database pool ───────────────────────────────────────────────────────
  const db = new Pool({ connectionString: cfg.DATABASE_URL, max: 10 });
  await db.query('SELECT 1');

  // ─── Redis ───────────────────────────────────────────────────────────────
  const redis = createClient({ url: cfg.REDIS_URL });
  await redis.connect().catch((err: Error) => {
    console.warn('Redis unavailable — caching disabled:', err.message);
  });

  // ─── HTTP server ─────────────────────────────────────────────────────────
  const app = Fastify({ logger: true, requestIdLogLabel: 'request_id' });
  await app.register(cors, { origin: false });

  // Prometheus metrics endpoint
  app.get('/metrics', async (_req, reply) => {
    reply.header('Content-Type', registry.contentType);
    return registry.metrics();
  });

  await app.register(buildHealthRouter);
  await app.register(buildPapRouter, { db, redis, cfg, unleash });
  await app.register(buildPepRouter, { db, redis, cfg, unleash });

  // ─── Start ───────────────────────────────────────────────────────────────
  const port = parseInt(cfg.HTTP_ADDR.replace(':', ''), 10) || 8084;
  await app.listen({ port, host: '0.0.0.0' });
  console.log(`ai-orchestrator listening on :${port}`);

  // ─── Graceful shutdown ───────────────────────────────────────────────────
  async function shutdown(signal: string) {
    console.log(`Received ${signal} — draining`);
    await app.close();
    await db.end();
    await redis.quit().catch(() => {});
    await sdk.shutdown();
    process.exit(0);
  }
  process.on('SIGTERM', () => shutdown('SIGTERM'));
  process.on('SIGINT',  () => shutdown('SIGINT'));
}

main().catch((err) => {
  console.error('Fatal startup error:', err);
  process.exit(1);
});
