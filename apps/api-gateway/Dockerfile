# syntax=docker/dockerfile:1.7
FROM golang:1.22-bookworm AS builder
WORKDIR /workspace
COPY go.work go.work.sum ./
COPY pkg/ pkg/
COPY apps/api-gateway/ apps/api-gateway/
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/api-gateway ./apps/api-gateway

# Final: distroless nonroot — no shell, no package manager
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /out/api-gateway /api-gateway
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/api-gateway"]
