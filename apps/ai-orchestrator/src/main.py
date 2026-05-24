"""AI Orchestrator service scaffold — Phase 9/10 implement the LangGraph graphs."""
from __future__ import annotations

import os
import signal
import sys

import structlog
import uvicorn
from fastapi import FastAPI
from opentelemetry import trace
from opentelemetry.exporter.otlp.proto.grpc.trace_exporter import OTLPSpanExporter
from opentelemetry.instrumentation.fastapi import FastAPIInstrumentor
from opentelemetry.sdk.resources import Resource, SERVICE_NAME, SERVICE_VERSION
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import BatchSpanProcessor
from UnleashClient import UnleashClient

from .config import Settings

log = structlog.get_logger()

SERVICE = "ai-orchestrator"
FEATURE_FLAG = "ai-orchestrator"


def _init_telemetry(settings: Settings) -> None:
    resource = Resource.create({
        SERVICE_NAME: SERVICE,
        SERVICE_VERSION: settings.version,
        "deployment.environment": settings.environment,
    })
    tp = TracerProvider(resource=resource)
    tp.add_span_processor(
        BatchSpanProcessor(OTLPSpanExporter(endpoint=settings.otlp_endpoint, insecure=True))
    )
    trace.set_tracer_provider(tp)


def _init_feature_flags(settings: Settings) -> UnleashClient:
    client = UnleashClient(
        url=settings.unleash_url,
        app_name=SERVICE,
        custom_headers={"Authorization": settings.unleash_token},
        environment=settings.environment,
    )
    client.initialize_client()
    return client


def create_app() -> FastAPI:
    settings = Settings()
    _init_telemetry(settings)

    ff = _init_feature_flags(settings)
    if not ff.is_enabled(FEATURE_FLAG):
        log.info("feature_flag_disabled", flag=FEATURE_FLAG)
        sys.exit(0)

    app = FastAPI(title="AI Orchestrator", version=settings.version, docs_url=None)
    FastAPIInstrumentor.instrument_app(app)

    @app.get("/healthz", include_in_schema=False)
    async def liveness() -> dict:
        return {"status": "ok"}

    @app.get("/readyz", include_in_schema=False)
    async def readiness() -> dict:
        return {"status": "ok"}

    @app.get("/v1/", include_in_schema=False)
    async def placeholder() -> dict:
        return {"code": "not_implemented", "message": "Phase 9/10 implement AI graphs"}

    return app


app = create_app()

if __name__ == "__main__":
    settings = Settings()

    def _handle_sigterm(*_) -> None:
        log.info("shutdown_signal_received")
        sys.exit(0)

    signal.signal(signal.SIGTERM, _handle_sigterm)

    uvicorn.run(
        "main:app",
        host="0.0.0.0",
        port=int(os.getenv("HTTP_PORT", "8083")),
        log_config=None,
    )
