# syntax=docker/dockerfile:1.7
#
# Silo omnibus image: Silo, PostgreSQL, and optional Redis and Meilisearch in
# one container, supervised by s6-overlay. Each service runs as its own user
# and keeps its data under /config/<service>.
#
# The Silo binary and the Node.js runtime for the Jellyfin Web installer are
# copied from the regular Silo image of the same build, so this image never
# compiles Silo itself. Everything else is installed from Ubuntu or upstream
# package repositories.

ARG SILO_IMAGE=ghcr.io/silo-server/silo-server:latest
FROM ${SILO_IMAGE} AS silo

FROM ubuntu:noble
ARG TARGETARCH
ARG S6_OVERLAY_VERSION=3.2.3.2
ARG S6_OVERLAY_NOARCH_SHA256=5379750ed30a84bbd2e2dd74847ba6b5bd29cd0b2e3ea2ec58049b57eb2eda12
ARG S6_OVERLAY_X86_64_SHA256=e6befcc96a437a3831386ecfc51808c5d3e939dc5fe3c02ae9284599e8aa2408
ARG S6_OVERLAY_AARCH64_SHA256=b17f17a82e7a515c682a91edaf2ffdabb73f891981b6c1fd712115693a2f8b4c
ARG PG_MAJOR=18
# Pinned: Meilisearch will not open data written by another version.
ARG MEILISEARCH_VERSION=v1.54.0
ARG MEILISEARCH_AMD64_SHA256=4c039396c19436c248d3429935559c990f6606d4ada781e7079325f32bbb7a3c
ARG MEILISEARCH_AARCH64_SHA256=b7817c408620383b56e5b516df5bcf55ed1267dc755cadb79e6f97038fcded7c
ARG JELLYFIN_FFMPEG_VERSION=8.1.3-1
ARG INTEL_GMMLIB_VERSION=22.10.0
ARG INTEL_IGC_VERSION=2.34.4
ARG INTEL_IGC_BUILD=21428
ARG INTEL_NEO_VERSION=26.18.38308.1

ENV DEBIAN_FRONTEND=noninteractive \
    LANG=en_US.UTF-8 \
    LC_ALL=en_US.UTF-8

# Base packages, then the Jellyfin and PostgreSQL apt repositories.
# create_main_cluster is turned off before postgresql-${PG_MAJOR} installs so
# the package does not create a cluster under /var/lib/postgresql; the data
# directory lives under /config/postgres instead.
RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates curl gnupg locales tzdata xz-utils && \
    locale-gen en_US.UTF-8 && \
    curl -fsSL https://repo.jellyfin.org/jellyfin_team.gpg.key \
      | gpg --dearmor -o /usr/share/keyrings/jellyfin.gpg && \
    echo "deb [signed-by=/usr/share/keyrings/jellyfin.gpg arch=${TARGETARCH}] https://repo.jellyfin.org/ubuntu noble main" \
      > /etc/apt/sources.list.d/jellyfin.list && \
    curl -fsSL https://www.postgresql.org/media/keys/ACCC4CF8.asc \
      | gpg --dearmor -o /usr/share/keyrings/postgresql.gpg && \
    echo "deb [signed-by=/usr/share/keyrings/postgresql.gpg arch=${TARGETARCH}] https://apt.postgresql.org/pub/repos/apt noble-pgdg main" \
      > /etc/apt/sources.list.d/pgdg.list && \
    apt-get update && \
    apt-get install -y --no-install-recommends postgresql-common && \
    sed -ri 's/^#?(create_main_cluster)\s*=.*/\1 = false/' /etc/postgresql-common/createcluster.conf && \
    apt-get install -y --no-install-recommends \
      "jellyfin-ffmpeg8=${JELLYFIN_FFMPEG_VERSION}-noble" \
      "postgresql-${PG_MAJOR}" "postgresql-${PG_MAJOR}-pgvector" \
      redis-server \
      git libvips42t64 fonts-noto-core fonts-noto-cjk && \
    apt-get purge -y gnupg && apt-get autoremove -y && \
    rm -rf /var/lib/apt/lists/* /var/lib/postgresql /etc/postgresql

# Debian's and Ubuntu's Intel OpenCL runtime lags the media hardware supported
# by the Jellyfin FFmpeg build. Match the regular Silo image on amd64 so QSV
# tone mapping can use the OpenCL path.
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

# s6-overlay
RUN set -eu; \
    case "${TARGETARCH}" in \
      amd64) s6_arch=x86_64; s6_sha="${S6_OVERLAY_X86_64_SHA256}" ;; \
      arm64) s6_arch=aarch64; s6_sha="${S6_OVERLAY_AARCH64_SHA256}" ;; \
      *) echo "unsupported TARGETARCH ${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    base="https://github.com/just-containers/s6-overlay/releases/download/v${S6_OVERLAY_VERSION}"; \
    curl -fsSL -o /tmp/s6-noarch.tar.xz "${base}/s6-overlay-noarch.tar.xz"; \
    curl -fsSL -o /tmp/s6-arch.tar.xz "${base}/s6-overlay-${s6_arch}.tar.xz"; \
    echo "${S6_OVERLAY_NOARCH_SHA256}  /tmp/s6-noarch.tar.xz" | sha256sum -c -; \
    echo "${s6_sha}  /tmp/s6-arch.tar.xz" | sha256sum -c -; \
    tar -C / -Jxpf /tmp/s6-noarch.tar.xz; \
    tar -C / -Jxpf /tmp/s6-arch.tar.xz; \
    rm -f /tmp/s6-noarch.tar.xz /tmp/s6-arch.tar.xz

# Meilisearch
RUN set -eu; \
    case "${TARGETARCH}" in \
      amd64) meili_arch=amd64; meili_sha="${MEILISEARCH_AMD64_SHA256}" ;; \
      arm64) meili_arch=aarch64; meili_sha="${MEILISEARCH_AARCH64_SHA256}" ;; \
      *) echo "unsupported TARGETARCH ${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    curl -fsSL -o /usr/local/bin/meilisearch \
      "https://github.com/meilisearch/meilisearch/releases/download/${MEILISEARCH_VERSION}/meilisearch-linux-${meili_arch}"; \
    echo "${meili_sha}  /usr/local/bin/meilisearch" | sha256sum -c -; \
    chmod 0755 /usr/local/bin/meilisearch

# Silo and the Node.js runtime its Jellyfin Web installer needs.
COPY --from=silo /usr/local/bin/silo /usr/local/bin/silo
COPY --from=silo /usr/local/bin/node /usr/local/bin/node
COPY --from=silo /usr/local/lib/node_modules/npm /usr/local/lib/node_modules/npm
RUN ln -sf ../lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm && \
    ln -sf ../lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx

# Service accounts. The postgres and redis users come from their packages.
# The silo user is the only one remapped at start (PUID and PGID).
# The base image's ubuntu account (uid 1000) goes: a PUID of 1000 would make
# the kernel report that name for the silo user and break peer authentication.
RUN userdel -r ubuntu 2>/dev/null; \
    groupadd -g 911 silo && \
    useradd -u 911 -g silo -d /config/silo -s /usr/sbin/nologin -M silo && \
    useradd -r -d /config/meilisearch -s /usr/sbin/nologin -M meilisearch && \
    mkdir -p /config && \
    ln -s /config/silo /var/lib/silo

COPY rootfs/ /

ENV PATH="/usr/lib/postgresql/${PG_MAJOR}/bin:${PATH}" \
    PG_MAJOR="${PG_MAJOR}" \
    PUID=911 \
    PGID=911 \
    ENABLE_REDIS=false \
    ENABLE_MEILISEARCH=false \
    SILO_PLUGIN_CACHE_DIR=/config/silo/plugins \
    POSTGRES_TUNE=auto \
    POSTGRES_TUNE_MEMORY_BUDGET_PERCENT=50 \
    NODE_OPTIONS=--use-openssl-ca \
    S6_CMD_WAIT_FOR_SERVICES_MAXTIME=0 \
    S6_BEHAVIOUR_IF_STAGE2_FAILS=2 \
    S6_VERBOSITY=1

VOLUME /config
EXPOSE 8080 8096 13378

# Silo migrates before it listens, and an upgrade on a large library can take
# minutes. The start period matches the regular image for the same reason.
HEALTHCHECK --interval=15s --timeout=5s --start-period=20m --retries=3 \
    CMD curl -f http://localhost:${PORT:-8080}/api/v1/health || exit 1

ENTRYPOINT ["/init"]
