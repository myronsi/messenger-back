# syntax=docker/dockerfile:1

# Build stage: static binaries, no cgo.
FROM golang:1.27.2 AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY cmd ./cmd
COPY internal ./internal
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X github.com/myronsi/messenger-back/internal/version.Backend=${VERSION}" -o /out/ ./cmd/...

# Static ffmpeg and ffprobe, which measure voice messages (duration and waveform).
FROM mwader/static-ffmpeg:8.0.1 AS ffmpeg

# Runtime stage: distroless static image, runs as the non-root user (uid 65532).
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=ffmpeg /ffmpeg /ffprobe /usr/local/bin/
COPY --from=build /out/api /out/worker /out/migrate-v1 /app/
ARG COMMIT=unknown
ENV APP_COMMIT=$COMMIT
USER nonroot:nonroot
EXPOSE 8080
# The worker runs from the same image: set the command to /app/worker.
CMD ["/app/api"]
