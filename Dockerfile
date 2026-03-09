# Build stage
FROM golang:1.22-alpine AS builder

WORKDIR /build

# Copy both repos (structure created by CI or build script)
# go-audible should be at ../go-audible or ./go-audible in build context
COPY go-audible/ ./go-audible/
COPY audible-plex-downloader/ ./

# Download Go dependencies
RUN go mod download

# Build the application (pure Go, no CGO needed for modernc.org/sqlite)
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /audible-plex-downloader ./cmd/server

# Runtime stage
FROM alpine:3.19

RUN apk add --no-cache ffmpeg ca-certificates tzdata

COPY --from=builder /audible-plex-downloader /usr/local/bin/audible-plex-downloader

RUN mkdir -p /config /audiobooks /downloads

EXPOSE 8080

VOLUME ["/config", "/audiobooks", "/downloads"]

ENTRYPOINT ["audible-plex-downloader"]
