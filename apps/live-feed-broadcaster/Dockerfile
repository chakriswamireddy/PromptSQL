FROM golang:1.22-alpine AS builder
WORKDIR /workspace
COPY go.work go.work.sum ./
COPY apps/live-feed-broadcaster/ apps/live-feed-broadcaster/
COPY pkg/ pkg/
RUN go build -o /live-feed-broadcaster ./apps/live-feed-broadcaster

FROM gcr.io/distroless/static-debian12
COPY --from=builder /live-feed-broadcaster /live-feed-broadcaster
ENTRYPOINT ["/live-feed-broadcaster"]
