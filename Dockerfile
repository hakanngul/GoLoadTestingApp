# syntax=docker/dockerfile:1.7
#
# goloadtest: one image for both roles.
#   master: docker run -p 8080:8080 -p 9090:9090 -v goload-data:/data IMAGE serve
#   worker: docker run --network host IMAGE worker -master HOST:9090
#
# Build: docker build --build-arg VERSION=$(git describe --tags --always) -t goloadtest .
# Multi-arch: docker buildx build --platform linux/amd64,linux/arm64 ...

ARG GO_VERSION=1.24
ARG NODE_VERSION=22

# ---------- 1. UI (Vite) ----------
FROM --platform=$BUILDPLATFORM node:${NODE_VERSION}-bookworm-slim AS ui
ARG PNPM_VERSION=10.12.1
WORKDIR /src/web
RUN corepack enable && corepack prepare pnpm@${PNPM_VERSION} --activate
COPY web/package.json web/pnpm-lock.yaml web/pnpm-workspace.yaml ./
RUN --mount=type=cache,target=/root/.local/share/pnpm/store \
    pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build

# ---------- 2. Go binary ----------
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-bookworm AS build
ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
# web/embed.go embeds web/dist, so the UI must be in place before go build.
COPY --from=ui /src/web/dist ./web/dist
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/goloadtest ./cmd/goloadtest
# Data directory for the default SQLite store, owned by the runtime user
# (distroless nonroot = 65532) so a fresh named volume mounted on /data
# inherits writable ownership.
RUN mkdir -p /out/data

# ---------- 3. Runtime ----------
FROM gcr.io/distroless/static-debian12:nonroot
ARG VERSION=dev
LABEL org.opencontainers.image.title="goloadtest" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.source="https://github.com/hakanngul/GoLoadTestingApp"
COPY --from=build /out/goloadtest /usr/local/bin/goloadtest
COPY --from=build --chown=65532:65532 /out/data /data
USER nonroot:nonroot
# Default store: SQLite in /data (mount a volume there to keep it).
# Override with -db / GOLOAD_DB (e.g. a postgres:// DSN, or "memory").
ENV GOLOAD_DB=sqlite:/data/goload.db
WORKDIR /data
# 8080: REST API + UI, 9090: gRPC for workers.
EXPOSE 8080 9090
ENTRYPOINT ["/usr/local/bin/goloadtest"]
CMD ["serve"]
