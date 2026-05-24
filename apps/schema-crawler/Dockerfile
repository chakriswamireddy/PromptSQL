# syntax=docker/dockerfile:1.7
FROM golang:1.22-bookworm AS builder
WORKDIR /workspace
COPY go.work go.work.sum ./
COPY pkg/ pkg/
COPY apps/schema-crawler/ apps/schema-crawler/
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/schema-crawler ./apps/schema-crawler

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /out/schema-crawler /schema-crawler
USER nonroot:nonroot
EXPOSE 8082 9095
ENTRYPOINT ["/schema-crawler"]
