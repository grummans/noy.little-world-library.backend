# syntax=docker/dockerfile:1

# ---- Build stage -----------------------------------------------------------
FROM golang:1.27-alpine AS build

WORKDIR /src

# Dependencies first so the module layer is cached while source changes.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# CGO off: pgx and minio-go are pure Go, and a static binary runs on bare alpine.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/migrate ./cmd/migrate

# ---- Runtime stage ---------------------------------------------------------
FROM alpine:3.23 AS runtime

# ca-certificates: VNPay and S3 over TLS. tzdata: readable log timestamps.
RUN apk add --no-cache ca-certificates tzdata \
 && adduser -D -u 10001 app

COPY --from=build /out/api /out/migrate /usr/local/bin/

USER app
EXPOSE 8080

# /healthz pings PostgreSQL and the bucket, so this covers the whole dependency chain.
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
  CMD ["wget", "-q", "-O", "/dev/null", "http://127.0.0.1:8080/healthz"]

ENTRYPOINT ["/usr/local/bin/api"]
