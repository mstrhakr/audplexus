# Build stage
FROM golang:alpine AS builder

ARG BUILD_RELEASE_VERSION=0.0.0
ARG BUILD_COMMIT_REF=
ARG BUILD_TIMESTAMP=
ARG BUILD_CHANNEL=dev

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
RUN CGO_ENABLED=0 go build -ldflags="-s -w -X github.com/mstrhakr/audplexus/internal/web.buildDeploymentMode=docker -X github.com/mstrhakr/audplexus/internal/web.buildReleaseVersion=${BUILD_RELEASE_VERSION} -X github.com/mstrhakr/audplexus/internal/web.buildCommitRef=${BUILD_COMMIT_REF} -X github.com/mstrhakr/audplexus/internal/web.buildTimestamp=${BUILD_TIMESTAMP} -X github.com/mstrhakr/audplexus/internal/web.buildChannel=${BUILD_CHANNEL}" -o /audplexus ./cmd/server

# Runtime stage
FROM alpine:3.24

RUN apk add --no-cache ffmpeg ca-certificates tzdata su-exec

COPY --from=builder /audplexus /usr/local/bin/audplexus
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh

RUN chmod +x /usr/local/bin/docker-entrypoint.sh

RUN mkdir -p /config /audiobooks /downloads

EXPOSE 8080

VOLUME ["/config", "/audiobooks", "/downloads"]

ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]

