# syntax=docker/dockerfile:1

# Build stage: static binaries, no cgo.
FROM golang:1.27.2 AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY cmd ./cmd
COPY internal ./internal
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ ./cmd/...

# Runtime stage: distroless static image, runs as the non-root user (uid 65532).
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/api /out/worker /out/migrate-v1 /app/
ARG COMMIT=unknown
ENV APP_COMMIT=$COMMIT
USER nonroot:nonroot
EXPOSE 8080
# The worker runs from the same image: set the command to /app/worker.
CMD ["/app/api"]
