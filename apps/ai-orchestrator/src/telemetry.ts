import { NodeSDK } from '@opentelemetry/sdk-node';
import { OTLPTraceExporter } from '@opentelemetry/exporter-trace-otlp-grpc';
import { OTLPMetricExporter } from '@opentelemetry/exporter-metrics-otlp-grpc';
import { Resource } from '@opentelemetry/resources';
import { SEMRESATTRS_SERVICE_NAME, SEMRESATTRS_SERVICE_VERSION, SEMRESATTRS_DEPLOYMENT_ENVIRONMENT } from '@opentelemetry/semantic-conventions';
import { PeriodicExportingMetricReader } from '@opentelemetry/sdk-metrics';

export function initTelemetry(opts: {
  endpoint: string;
  samplingRate: number;
  version: string;
  environment: string;
}): NodeSDK {
  const resource = new Resource({
    [SEMRESATTRS_SERVICE_NAME]: 'ai-orchestrator',
    [SEMRESATTRS_SERVICE_VERSION]: opts.version,
    [SEMRESATTRS_DEPLOYMENT_ENVIRONMENT]: opts.environment,
  });

  const sdk = new NodeSDK({
    resource,
    traceExporter: new OTLPTraceExporter({ url: opts.endpoint }),
    metricReader: new PeriodicExportingMetricReader({
      exporter: new OTLPMetricExporter({ url: opts.endpoint }),
      exportIntervalMillis: 15_000,
    }),
  });

  sdk.start();
  return sdk;
}
