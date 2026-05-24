FROM golang:1.22-alpine AS builder
WORKDIR /workspace

# Copy workspace files for replace directives.
COPY go.work go.work.sum ./
COPY pkg/ pkg/
COPY packages/policy-engine/ packages/policy-engine/
COPY apps/retrieval-service/ apps/retrieval-service/

WORKDIR /workspace/apps/retrieval-service
RUN go build -trimpath -ldflags="-s -w" -o /retrieval-service .

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /retrieval-service /retrieval-service
EXPOSE 8083
ENTRYPOINT ["/retrieval-service"]
