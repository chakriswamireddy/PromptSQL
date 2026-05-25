FROM golang:1.22-alpine AS builder
WORKDIR /src
COPY . ./apps/anomaly-detector/
COPY ../../pkg ./pkg/
RUN cd apps/anomaly-detector && go build -o /anomaly-detector .

FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /anomaly-detector /anomaly-detector
USER 65534:65534
ENTRYPOINT ["/anomaly-detector"]
