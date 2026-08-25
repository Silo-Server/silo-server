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
# The settings contract is a Go package (contracts/settings/v1) that embeds the
# manifest, so the binary carries the exact bytes it was built from. It lives
# outside internal/ because clients vendor these files.
COPY contracts/ contracts/
ARG BUILD_REVISION
ARG BUILD_DIRTY=false
ARG BUILD_NUMBER
ARG BUILD_DATE
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    go build \
    -ldflags "-X github.com/Silo-Server/silo-server/internal/buildinfo.revisionOverride=${BUILD_REVISION} -X github.com/Silo-Server/silo-server/internal/buildinfo.dirtyOverride=${BUILD_DIRTY} -X github.com/Silo-Server/silo-server/internal/buildinfo.buildNumberOverride=${BUILD_NUMBER} -X github.com/Silo-Server/silo-server/internal/buildinfo.builtAtOverride=${BUILD_DATE}" \
    -o /silo ./cmd/silo/

# Stage 3: Runtime
FROM debian:trixie-slim
ARG TARGETARCH
ARG INTEL_GMMLIB_VERSION=22.10.0
ARG INTEL_IGC_VERSION=2.34.4
ARG INTEL_IGC_BUILD=21428
ARG INTEL_NEO_VERSION=26.18.38308.1
RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates curl gnupg && \
    curl -fsSL https://repo.jellyfin.org/jellyfin_team.gpg.key \
      | gpg --dearmor -o /usr/share/keyrings/jellyfin.gpg && \
    echo "deb [signed-by=/usr/share/keyrings/jellyfin.gpg arch=${TARGETARCH}] https://repo.jellyfin.org/debian trixie main" \
      > /etc/apt/sources.list.d/jellyfin.list && \
    apt-get update && \
    apt-get install -y --no-install-recommends jellyfin-ffmpeg7 git libvips42 fonts-noto-core fonts-noto-cjk && \
    apt-get purge -y gnupg && apt-get autoremove -y && \
    rm -rf /var/lib/apt/lists/*
# Debian's Intel OpenCL runtime lags the media hardware supported by the
# Jellyfin FFmpeg build. Match Jellyfin's container packaging on amd64 so QSV
# tone mapping can use the quality-preserving OpenCL path. Other architectures
# retain software tone mapping unless their own hardware executor probes clean.
RUN if [ "${TARGETARCH}" = "amd64" ]; then \
      runtime_dir="$(mktemp -d)" && \
      cd "${runtime_dir}" && \
      curl -fsSLO "https://github.com/intel/compute-runtime/releases/download/${INTEL_NEO_VERSION}/libigdgmm12_${INTEL_GMMLIB_VERSION}_amd64.deb" && \
      curl -fsSLO "https://github.com/intel/intel-graphics-compiler/releases/download/v${INTEL_IGC_VERSION}/intel-igc-core-2_${INTEL_IGC_VERSION}+${INTEL_IGC_BUILD}_amd64.deb" && \
      curl -fsSLO "https://github.com/intel/intel-graphics-compiler/releases/download/v${INTEL_IGC_VERSION}/intel-igc-opencl-2_${INTEL_IGC_VERSION}+${INTEL_IGC_BUILD}_amd64.deb" && \
      curl -fsSLO "https://github.com/intel/compute-runtime/releases/download/${INTEL_NEO_VERSION}/intel-opencl-icd_${INTEL_NEO_VERSION}-0_amd64.deb" && \
      apt-get update && \
      apt-get install -y --no-install-recommends ./*.deb && \
      cd / && \
      rm -rf "${runtime_dir}" /var/lib/apt/lists/*; \
    fi
RUN mkdir -p /tmp/silo-transcode /var/lib/silo/compat/jellyfin-web
COPY --from=frontend /usr/local/bin/node /usr/local/bin/node
COPY --from=frontend /usr/local/lib/node_modules/npm /usr/local/lib/node_modules/npm
RUN ln -sf ../lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm && \
    ln -sf ../lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx
COPY --from=build /silo /usr/local/bin/silo
EXPOSE 8080 8096 13378
HEALTHCHECK --interval=15s --timeout=5s --start-period=10s --retries=3 \
    CMD curl -f http://localhost:${PORT:-8080}/api/v1/health || exit 1
ENTRYPOINT ["silo"]
