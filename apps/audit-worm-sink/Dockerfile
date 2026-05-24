FROM golang:1.22-alpine AS builder
WORKDIR /build

COPY go.work go.work.sum ./
COPY pkg/ pkg/
COPY apps/audit-worm-sink/ apps/audit-worm-sink/

RUN cd apps/audit-worm-sink && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /audit-worm-sink .

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /audit-worm-sink /audit-worm-sink
USER nonroot:nonroot
EXPOSE 9103
ENTRYPOINT ["/audit-worm-sink"]
