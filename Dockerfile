# Build stage
FROM golang:alpine AS builder

# Allow Go to download the required toolchain version
ENV GOTOOLCHAIN=auto

# Required for direct VCS module resolution fallback.
RUN apk add --no-cache git

WORKDIR /build

# Copy go module files
COPY go.mod go.sum ./

# Download Go dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the application (pure Go, no CGO needed for modernc.org/sqlite)
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /audible-plex-downloader ./cmd/server

# Runtime stage
FROM alpine:3.19

RUN apk add --no-cache ffmpeg ca-certificates tzdata su-exec

COPY --from=builder /audible-plex-downloader /usr/local/bin/audible-plex-downloader
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh

RUN chmod +x /usr/local/bin/docker-entrypoint.sh

RUN mkdir -p /config /audiobooks /downloads

EXPOSE 8080

VOLUME ["/config", "/audiobooks", "/downloads"]

ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
