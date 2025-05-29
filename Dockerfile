# Builder stage
FROM golang:1.23-alpine AS builder

WORKDIR /app

# Install build dependencies including gcc and SQLite
RUN apk add --no-cache gcc musl-dev sqlite-dev

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Enable CGO and build
ENV CGO_ENABLED=1
RUN cd cmd/api && go build -o ../../rclone_uploader

# Final stage
FROM alpine:latest

# Install runtime dependencies
RUN apk add --no-cache sqlite-dev sqlite-libs ca-certificates rclone

# Create the app directory and data directory first
RUN mkdir -p /app/data /app/internal && \
    adduser -D -u 1000 appuser && \
    chown -R appuser:appuser /app && \
    chmod 755 /app/data /app/internal

USER appuser
WORKDIR /app
COPY --from=builder /app/rclone_uploader .
COPY --from=builder /app/internal/static/index.html ./internal/static/index.html

# Use PORT from environment with default
EXPOSE ${PORT:-8050}

CMD ["./rclone_uploader"]
