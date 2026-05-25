# syntax=docker/dockerfile:1.7
FROM golang:1.22-bookworm AS builder
WORKDIR /workspace
COPY go.work go.work.sum ./
COPY pkg/ pkg/
COPY apps/native-policy-syncer/ apps/native-policy-syncer/
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/native-policy-syncer ./apps/native-policy-syncer

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /out/native-policy-syncer /native-policy-syncer
USER nonroot:nonroot
EXPOSE 8085
ENTRYPOINT ["/native-policy-syncer"]
