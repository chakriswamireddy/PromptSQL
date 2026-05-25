FROM golang:1.22-alpine AS builder
WORKDIR /workspace
COPY go.work go.work.sum ./
COPY apps/webhook-fanout/ apps/webhook-fanout/
COPY pkg/ pkg/
RUN go build -o /webhook-fanout ./apps/webhook-fanout

FROM gcr.io/distroless/static-debian12
COPY --from=builder /webhook-fanout /webhook-fanout
ENTRYPOINT ["/webhook-fanout"]
