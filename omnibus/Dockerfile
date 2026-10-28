# Silo omnibus image: the regular Silo image plus PostgreSQL, and optional Redis
# and Meilisearch, in one container supervised by s6-overlay. Each service runs
# as its own user and keeps its data under /config/<service>.
#
# CI builds this on the regular image of the same commit, so both images run
# the same Silo binary against the same libraries, FFmpeg build, and GPU
# runtimes. This file adds only what the regular image does not carry. The
# base changes on every build, so no layer here outlives a package update.

ARG SILO_IMAGE=ghcr.io/silo-server/silo-server:latest
FROM ${SILO_IMAGE}
ARG TARGETARCH
ARG S6_OVERLAY_VERSION=3.2.3.2
ARG S6_OVERLAY_NOARCH_SHA256=5379750ed30a84bbd2e2dd74847ba6b5bd29cd0b2e3ea2ec58049b57eb2eda12
ARG S6_OVERLAY_X86_64_SHA256=e6befcc96a437a3831386ecfc51808c5d3e939dc5fe3c02ae9284599e8aa2408
ARG S6_OVERLAY_AARCH64_SHA256=b17f17a82e7a515c682a91edaf2ffdabb73f891981b6c1fd712115693a2f8b4c
ARG PG_MAJOR=18
# Pinned: Meilisearch will not open data written by another version. See
# "Two versions are load-bearing" in docs/architecture/omnibus-image.md.
ARG MEILISEARCH_VERSION=v1.54.0
ARG MEILISEARCH_AMD64_SHA256=4c039396c19436c248d3429935559c990f6606d4ada781e7079325f32bbb7a3c
ARG MEILISEARCH_AARCH64_SHA256=b7817c408620383b56e5b516df5bcf55ed1267dc755cadb79e6f97038fcded7c

# Service accounts with fixed ids, created before the packages so the packages
# reuse them. /config/<service> then keeps a valid owner across image updates
# even if a later package takes the next free system id. The silo account is
# the only one remapped at start (PUID and PGID). Its home is /nonexistent:
# usermod -u re-owns every file in the account's home, and init-config already
# hands /config/silo over when the ids change. The scripts set HOME themselves.
RUN groupadd -r -g 999 postgres && \
    useradd -r -u 999 -g postgres -d /var/lib/postgresql -s /bin/bash -M postgres && \
    groupadd -r -g 998 redis && \
    useradd -r -u 998 -g redis -d /var/lib/redis -s /usr/sbin/nologin -M redis && \
    groupadd -r -g 997 meilisearch && \
    useradd -r -u 997 -g meilisearch -d /config/meilisearch -s /usr/sbin/nologin -M meilisearch && \
    groupadd -g 911 silo && \
    useradd -u 911 -g silo -d /nonexistent -s /usr/sbin/nologin -M silo

# PostgreSQL from the PostgreSQL apt repository, the same build the
# pgvector/pgvector image of the earlier multi-container Compose stack uses.
# create_main_cluster is turned off before postgresql-${PG_MAJOR} installs so
# the package does not create a cluster under /var/lib/postgresql; the data
# directory lives under /config/postgres instead. The cluster uses
# en_US.UTF-8, as that stack's does, so sorting matches.
RUN apt-get update && \
    apt-get install -y --no-install-recommends locales xz-utils && \
    sed -i 's/^# *\(en_US.UTF-8 UTF-8\)/\1/' /etc/locale.gen && \
    locale-gen && \
    install -d /etc/apt/keyrings && \
    curl -fsSL -o /etc/apt/keyrings/pgdg.asc https://www.postgresql.org/media/keys/ACCC4CF8.asc && \
    echo "deb [signed-by=/etc/apt/keyrings/pgdg.asc arch=${TARGETARCH}] https://apt.postgresql.org/pub/repos/apt trixie-pgdg main" \
      > /etc/apt/sources.list.d/pgdg.list && \
    apt-get update && \
    apt-get install -y --no-install-recommends postgresql-common && \
    sed -ri 's/^#?(create_main_cluster)\s*=.*/\1 = false/' /etc/postgresql-common/createcluster.conf && \
    apt-get install -y --no-install-recommends \
      "postgresql-${PG_MAJOR}" "postgresql-${PG_MAJOR}-pgvector" redis-server && \
    rm -rf /var/lib/apt/lists/* /var/lib/postgresql /etc/postgresql

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

# Silo's state moves under /config: /var/lib/silo, which the regular image
# creates, becomes a link to /config/silo.
RUN rm -rf /var/lib/silo && \
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
    S6_CMD_WAIT_FOR_SERVICES_MAXTIME=0 \
    S6_BEHAVIOUR_IF_STAGE2_FAILS=2 \
    S6_VERBOSITY=1

VOLUME /config

# The ports and the health check come from the regular image.
ENTRYPOINT ["/init"]
