# syntax=docker/dockerfile:1.7
FROM python:3.12-slim-bookworm AS builder
WORKDIR /app
COPY pyproject.toml ./
RUN pip install --no-cache-dir build && python -m build --wheel

FROM python:3.12-slim-bookworm
WORKDIR /app
COPY --from=builder /app/dist/*.whl ./
RUN pip install --no-cache-dir *.whl && rm *.whl
COPY src/ src/
USER nobody:nogroup
EXPOSE 8083
ENTRYPOINT ["python", "-m", "src.main"]
