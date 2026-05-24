# syntax=docker/dockerfile:1.7
FROM golang:1.22-bookworm AS builder
WORKDIR /workspace
COPY go.work go.work.sum ./
COPY pkg/ pkg/
COPY apps/proxy/ apps/proxy/
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/proxy ./apps/proxy

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /out/proxy /proxy
USER nonroot:nonroot
EXPOSE 5433 9094
ENTRYPOINT ["/proxy"]
