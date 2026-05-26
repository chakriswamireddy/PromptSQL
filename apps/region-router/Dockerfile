FROM golang:1.22-alpine AS builder
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /region-router .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /region-router /region-router
EXPOSE 8080 9093
ENTRYPOINT ["/region-router"]
