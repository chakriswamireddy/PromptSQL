FROM golang:1.22-alpine AS builder
WORKDIR /workspace

# Copy workspace files for module resolution.
COPY go.work go.work.sum ./
COPY pkg/ pkg/
COPY apps/auto-responder/ apps/auto-responder/

RUN go build -o /auto-responder ./apps/auto-responder

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /auto-responder /auto-responder
USER nonroot:nonroot
EXPOSE 8095 9095
ENTRYPOINT ["/auto-responder"]
