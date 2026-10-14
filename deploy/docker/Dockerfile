# syntax=docker/dockerfile:1

# ---- build ----
FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS build
WORKDIR /src

# Dependencies first for layer caching.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
ARG VERSION=dev
ARG TARGETOS TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" \
      -o /out/minibase ./cmd/minibase

# ---- runtime ----
# Distroless static: no shell, no package manager, runs as non-root.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/minibase /minibase
ENV MINIBASE_HTTP_ADDR=:8090
EXPOSE 8090
USER nonroot:nonroot
HEALTHCHECK --interval=10s --timeout=4s --start-period=10s --retries=3 \
  CMD ["/minibase", "healthcheck"]
ENTRYPOINT ["/minibase"]
CMD ["serve"]
