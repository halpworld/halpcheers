# Stage 1: Build the static binary
FROM golang:1.24-alpine AS builder

WORKDIR /src/server

# Copy dependency specifications first to leverage build cache
COPY server/go.mod server/go.sum ./
RUN go mod download

# Copy server source code
COPY server/ ./

# Build static binary without CGO
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/halpd ./cmd/halpd

# Stage 2: Minimal non-root distroless container without a shell
FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app

COPY --from=builder /out/halpd /app/halpd

USER nonroot:nonroot

VOLUME ["/data"]

EXPOSE 8080

ENTRYPOINT ["/app/halpd"]
