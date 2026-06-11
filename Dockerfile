# Stage 1: Build frontend
FROM node:22-slim AS frontend
RUN corepack enable && corepack prepare pnpm@10.32.1 --activate
WORKDIR /app/web
COPY web/package.json web/pnpm-lock.yaml ./
COPY web/vendor/foliate-js ./vendor/foliate-js
RUN --mount=type=cache,target=/root/.local/share/pnpm/store \
    pnpm install --frozen-lockfile
COPY web/ .
RUN pnpm run build

# Allow CI to inject prebuilt frontend assets via a named `frontend_dist`
# context while local builds keep using the in-Docker frontend stage.
FROM scratch AS frontend_dist
COPY --from=frontend /app/web/dist/. /

# Stage 2: Build Go binary
FROM golang:1.26 AS build
ENV CGO_ENABLED=1
ENV GOPROXY=https://proxy.golang.org,direct
ENV GOPRIVATE=github.com/Silo-Server/*
ENV GONOSUMDB=github.com/Silo-Server/*
RUN apt-get update && apt-get install -y --no-install-recommends libvips-dev && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY go.mod go.sum ./
COPY internal/compat/zishang520-webtransport-go/ internal/compat/zishang520-webtransport-go/
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    go mod download
COPY web/embed.go web/embed.go
COPY --from=frontend_dist / web/dist
COPY cmd/ cmd/
COPY internal/ internal/
COPY migrations/ migrations/
ARG BUILD_REVISION
ARG BUILD_DIRTY=false
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    go build \
    -ldflags "-X github.com/Silo-Server/silo-server/internal/buildinfo.revisionOverride=${BUILD_REVISION} -X github.com/Silo-Server/silo-server/internal/buildinfo.dirtyOverride=${BUILD_DIRTY}" \
    -o /silo ./cmd/silo/

# Stage 3: Runtime
FROM debian:bookworm-slim
ARG TARGETARCH
RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates curl gnupg && \
    curl -fsSL https://repo.jellyfin.org/jellyfin_team.gpg.key \
      | gpg --dearmor -o /usr/share/keyrings/jellyfin.gpg && \
    echo "deb [signed-by=/usr/share/keyrings/jellyfin.gpg arch=${TARGETARCH}] https://repo.jellyfin.org/debian bookworm main" \
      > /etc/apt/sources.list.d/jellyfin.list && \
    apt-get update && \
    apt-get install -y --no-install-recommends jellyfin-ffmpeg7 libvips42 fonts-noto-core fonts-noto-cjk && \
    apt-get purge -y gnupg && apt-get autoremove -y && \
    rm -rf /var/lib/apt/lists/*
RUN mkdir -p /tmp/silo-transcode
COPY --from=build /silo /usr/local/bin/silo
COPY third_party/jellyfin-web/ /srv/jellyfin-web/
EXPOSE 8080 8096 13378
HEALTHCHECK --interval=15s --timeout=5s --start-period=10s --retries=3 \
    CMD curl -f http://localhost:${PORT:-8080}/api/v1/health || exit 1
ENTRYPOINT ["silo"]
