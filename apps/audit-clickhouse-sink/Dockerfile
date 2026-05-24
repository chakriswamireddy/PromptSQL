FROM golang:1.22-alpine AS builder
WORKDIR /build

COPY go.work go.work.sum ./
COPY pkg/ pkg/
COPY apps/audit-clickhouse-sink/ apps/audit-clickhouse-sink/

RUN cd apps/audit-clickhouse-sink && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /audit-clickhouse-sink .

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /audit-clickhouse-sink /audit-clickhouse-sink
USER nonroot:nonroot
EXPOSE 9102
ENTRYPOINT ["/audit-clickhouse-sink"]
