# ── Stage 1: build ──────────────────────────────────────────────────────────
FROM golang:1.24-alpine AS builder

WORKDIR /src

# Download dependencies separately so Docker can cache this layer.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build \
      -trimpath \
      -ldflags="-w -s" \
      -o /k8s-api \
      .

# ── Stage 2: runtime ────────────────────────────────────────────────────────
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /k8s-api /k8s-api

EXPOSE 8080

# Health probe uses the binary's built-in /health endpoint
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD ["/k8s-api", "--healthcheck"]

USER nonroot:nonroot

ENTRYPOINT ["/k8s-api"]
