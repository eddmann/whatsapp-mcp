# Multi-stage build for minimal final image with CGO support
FROM --platform=$TARGETPLATFORM golang:1.24-bookworm AS builder

# Install build dependencies for CGO and SQLite
RUN apt-get update && apt-get install -y \
    gcc \
    g++ \
    make \
    && rm -rf /var/lib/apt/lists/*

# Set working directory
WORKDIR /app

# Copy go module files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY cmd/ ./cmd/
COPY internal/ ./internal/

# Build the binary with CGO enabled and SQLite FTS5 support
RUN CGO_ENABLED=1 go build -tags "sqlite_fts5" -o whatsapp-mcp ./cmd/whatsapp-mcp

# Final stage - minimal runtime image
FROM --platform=$TARGETPLATFORM debian:bookworm-slim

# Install runtime dependencies (ffmpeg for audio conversion)
RUN apt-get update && apt-get install -y \
    ca-certificates \
    ffmpeg \
    && rm -rf /var/lib/apt/lists/*

# Set working directory
WORKDIR /app

# Copy binary from builder
COPY --from=builder /app/whatsapp-mcp /app/whatsapp-mcp

# Set environment variables
ENV DB_DIR=/app/store \
    LOG_LEVEL=INFO \
    FFMPEG_PATH=ffmpeg

# Create directory for database and media storage
RUN mkdir -p /app/store

# The MCP server runs via stdio, so no port exposure needed

# Health check (optional - checks if binary is functional)
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD test -f /app/whatsapp-mcp || exit 1

# Run the MCP server
ENTRYPOINT ["/app/whatsapp-mcp"]
