import { z } from 'zod';

const ConfigSchema = z.object({
  HTTP_ADDR: z.string().default(':8084'),
  DATABASE_URL: z.string(),
  REDIS_URL: z.string(),
  KAFKA_BROKERS: z.string(),
  KAFKA_TOPIC_SYSTEM: z.string().default('platform.system'),
  AUDIT_HMAC_KEY: z.string(),

  UNLEASH_URL: z.string().default('http://unleash:4242/api'),
  UNLEASH_API_TOKEN: z.string().default('default:development.unleash-insecure-api-token'),

  OTEL_EXPORTER_OTLP_ENDPOINT: z.string().default('http://otel-collector:4317'),
  OTEL_SAMPLING_RATE: z.coerce.number().default(1.0),
  VERSION: z.string().default('0.1.0'),
  DEPLOYMENT_ENVIRONMENT: z.string().default('development'),

  // LLM providers
  ANTHROPIC_API_KEY: z.string().default(''),
  OPENAI_API_KEY: z.string().default(''),

  // Per-tenant defaults (overridable at runtime)
  DEFAULT_DRAFTER_PROVIDER: z.enum(['anthropic', 'openai']).default('anthropic'),
  DEFAULT_DRAFTER_MODEL: z.string().default('claude-sonnet-4-6'),
  DEFAULT_INTENT_MODEL: z.string().default('claude-haiku-4-5-20251001'),
  DEFAULT_EXPLAINER_MODEL: z.string().default('claude-haiku-4-5-20251001'),
  FALLBACK_DRAFTER_PROVIDER: z.enum(['anthropic', 'openai']).default('openai'),
  FALLBACK_DRAFTER_MODEL: z.string().default('gpt-4o'),

  // PDP gRPC
  PDP_ADDR: z.string().default('pdp:9090'),

  // Internal service addresses (PEP graph)
  RETRIEVAL_SERVICE_ADDR: z.string().default('http://retrieval-service:8083'),
  PROXY_HOST: z.string().default('proxy'),
  PROXY_PORT: z.coerce.number().default(5433),
  API_GATEWAY_ADDR: z.string().default('http://api-gateway:8080'),

  // Graph limits — PAP
  GRAPH_WALL_CLOCK_BUDGET_MS: z.coerce.number().default(30_000),
  DRAFT_CACHE_TTL_SEC: z.coerce.number().default(3600),
  EXPLAINER_CACHE_TTL_SEC: z.coerce.number().default(7200),

  // Graph limits — PEP
  PEP_WALL_CLOCK_BUDGET_MS: z.coerce.number().default(30_000),
  PEP_MAX_ROWS_DEFAULT: z.coerce.number().default(1000),
  PEP_MAX_COST_DEFAULT: z.coerce.number().default(100_000),   // EXPLAIN cost units
  PEP_MAX_PLAN_ROWS_DEFAULT: z.coerce.number().default(1_000_000),
  PEP_RESULT_CACHE_TTL_SEC: z.coerce.number().default(30),
  PEP_DRAFTER_MAX_RETRIES: z.coerce.number().default(2),
  PEP_DEFAULT_SQL_MODEL: z.string().default('claude-sonnet-4-6'),
});

export type Config = z.infer<typeof ConfigSchema>;

export function loadConfig(): Config {
  return ConfigSchema.parse(process.env);
}
