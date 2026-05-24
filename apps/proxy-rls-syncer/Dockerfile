# syntax=docker/dockerfile:1.7
FROM golang:1.22-bookworm AS builder
WORKDIR /workspace
COPY go.work go.work.sum ./
COPY pkg/ pkg/
COPY apps/proxy-rls-syncer/ apps/proxy-rls-syncer/
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/proxy-rls-syncer ./apps/proxy-rls-syncer

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /out/proxy-rls-syncer /proxy-rls-syncer
USER nonroot:nonroot
EXPOSE 9097
ENTRYPOINT ["/proxy-rls-syncer"]
