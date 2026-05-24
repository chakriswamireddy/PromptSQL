import { NodeSDK } from "@opentelemetry/sdk-node";
import { OTLPTraceExporter } from "@opentelemetry/exporter-trace-otlp-grpc";
import { PrometheusExporter } from "@opentelemetry/exporter-prometheus";
import { Resource } from "@opentelemetry/resources";
import {
  SEMRESATTRS_SERVICE_NAME,
  SEMRESATTRS_SERVICE_VERSION,
  SEMRESATTRS_DEPLOYMENT_ENVIRONMENT,
} from "@opentelemetry/semantic-conventions";
import { trace, metrics } from "@opentelemetry/api";

export interface TelemetryConfig {
  serviceName: string;
  serviceVersion: string;
  environment: string;
  otlpEndpoint: string;
  prometheusPort?: number;
}

let sdk: NodeSDK | null = null;

/**
 * Initialise OTel traces and metrics. Call once at process startup, before
 * any other import that might use instrumentation.
 */
export function initTelemetry(cfg: TelemetryConfig): void {
  const resource = new Resource({
    [SEMRESATTRS_SERVICE_NAME]: cfg.serviceName,
    [SEMRESATTRS_SERVICE_VERSION]: cfg.serviceVersion,
    [SEMRESATTRS_DEPLOYMENT_ENVIRONMENT]: cfg.environment,
    // Reserved for future tenancy injection.
    "tenant.id": "",
  });

  const traceExporter = new OTLPTraceExporter({ url: cfg.otlpEndpoint });
  const metricReader = new PrometheusExporter({ port: cfg.prometheusPort ?? 9464 });

  sdk = new NodeSDK({ resource, traceExporter, metricReader });
  sdk.start();

  process.on("SIGTERM", async () => {
    await sdk?.shutdown();
  });
}

export { trace, metrics };
