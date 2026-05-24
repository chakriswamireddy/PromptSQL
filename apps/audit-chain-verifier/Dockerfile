FROM golang:1.22-alpine AS builder
WORKDIR /build

COPY go.work go.work.sum ./
COPY pkg/ pkg/
COPY apps/audit-chain-verifier/ apps/audit-chain-verifier/

RUN cd apps/audit-chain-verifier && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /audit-chain-verifier .

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /audit-chain-verifier /audit-chain-verifier
USER nonroot:nonroot
EXPOSE 9104
ENTRYPOINT ["/audit-chain-verifier"]
