#!/usr/bin/env bash
# One-time setup for the rsync+air dev workflow on the Silo LXC.
# Installs Go, Node, pnpm, libvips, ffmpeg (linked at the jellyfin-ffmpeg path), air, and tmux.
# Usage: DEV_HOST=root@silo-dev.example.invalid ./scripts/dev-remote-setup.sh
set -euo pipefail

DEV_HOST="${DEV_HOST:-root@silo-dev.example.invalid}"
DEV_DIR="${DEV_DIR:-/opt/git/silo-dev}"

# Install the Go that CI's setup-go picks from go.mod: the toolchain line if
# there is one, otherwise the go line.
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GO_VERSION="$(awk '
    $1 == "toolchain" && $2 != "default" { sub(/^go/, "", $2); toolchain = $2 }
    $1 == "go" && !go { go = $2 }
    END { print (toolchain != "" ? toolchain : go) }
' "${ROOT_DIR}/go.mod")"
# go.dev/dl only has files for exact releases, so "1.27" has no download.
if ! printf '%s\n' "${GO_VERSION}" | grep -Eq '^[0-9]+\.[0-9]+(\.[0-9]+|rc[0-9]+)$'; then
    echo "error: go.mod names Go '${GO_VERSION}', which is not an exact release to download" >&2
    exit 1
fi

echo "==> Setting up dev environment on ${DEV_HOST}..."

ssh "${DEV_HOST}" bash -s -- "${DEV_DIR}" "${GO_VERSION}" <<'REMOTE'
set -euo pipefail
DEV_DIR="$1"
GO_VERSION="$2"

export DEBIAN_FRONTEND=noninteractive

# --- Go (go.mod's version or newer) ---
# A non-interactive ssh shell does not read /etc/profile.d, so an installed Go
# can be missing from PATH here. Put /usr/local/go first before checking.
export PATH=/usr/local/go/bin:/root/go/bin:$PATH
INSTALLED_GO=""
if command -v go &>/dev/null; then
    # GOTOOLCHAIN=local reports the installed Go instead of switching to another.
    INSTALLED_GO="$(GOTOOLCHAIN=local go env GOVERSION 2>/dev/null || true)"
    INSTALLED_GO="${INSTALLED_GO#go}"
fi
case "${INSTALLED_GO}" in
    [0-9]*) ;;
    *) INSTALLED_GO="" ;; # missing, or a devel build: reinstall
esac
if [ -n "${INSTALLED_GO}" ] &&
    printf '%s\n%s\n' "${GO_VERSION}" "${INSTALLED_GO}" | sort -V -C; then
    echo "==> Go already installed: $(go version)"
else
    echo "==> Installing Go ${GO_VERSION}..."
    case "$(uname -m)" in
        x86_64) GO_ARCH=amd64 ;;
        aarch64 | arm64) GO_ARCH=arm64 ;;
        armv6l | armv7l) GO_ARCH=armv6l ;;
        i?86) GO_ARCH=386 ;;
        riscv64 | ppc64le | s390x) GO_ARCH="$(uname -m)" ;;
        *) echo "error: no Go download for $(uname -m)" >&2; exit 1 ;;
    esac
    # Download and unpack beside /usr/local/go, and swap only once that
    # succeeds. A failed download keeps the old Go, and a cut-off extraction
    # never lands where the next run would take it as installed. The old tree
    # is removed rather than extracted over: tar merges, and files left from
    # another release break the build.
    GO_STAGE="$(mktemp -d /usr/local/.go-install.XXXXXX)"
    trap 'rm -rf "${GO_STAGE}"' EXIT
    curl -fsSL -o "${GO_STAGE}/go.tar.gz" \
        "https://go.dev/dl/go${GO_VERSION}.linux-${GO_ARCH}.tar.gz"
    tar -C "${GO_STAGE}" -xzf "${GO_STAGE}/go.tar.gz"
    STAGED_GO="$(GOTOOLCHAIN=local "${GO_STAGE}/go/bin/go" env GOVERSION)"
    if [ "${STAGED_GO}" != "go${GO_VERSION}" ]; then
        echo "error: downloaded Go reports ${STAGED_GO}, expected go${GO_VERSION}" >&2
        exit 1
    fi
    rm -rf /usr/local/go
    mv "${GO_STAGE}/go" /usr/local/go
    echo "==> Installed: $(go version)"
fi
# Rewrite on every run so hosts set up by older versions of this script lose
# their appended PATH lines, which put /usr/local/go after a distro Go.
cat > /etc/profile.d/golang.sh <<'EOF'
export PATH=/usr/local/go/bin:/root/go/bin:$PATH
EOF

# --- System packages (libvips, tmux, build tools) ---
echo "==> Installing system packages..."
apt-get update -qq
apt-get install -y --no-install-recommends \
    build-essential pkg-config \
    libvips-dev \
    tmux \
    ca-certificates curl gnupg

# --- ffmpeg (symlinked to jellyfin-ffmpeg path for compatibility) ---
if ! command -v ffmpeg &>/dev/null; then
    echo "==> Installing ffmpeg..."
    apt-get install -y --no-install-recommends ffmpeg
else
    echo "==> ffmpeg already installed: $(ffmpeg -version 2>&1 | head -1)"
fi
mkdir -p /usr/lib/jellyfin-ffmpeg
ln -sf /usr/bin/ffmpeg /usr/lib/jellyfin-ffmpeg/ffmpeg
ln -sf /usr/bin/ffprobe /usr/lib/jellyfin-ffmpeg/ffprobe

# --- Node 22 + pnpm ---
if ! command -v node &>/dev/null; then
    echo "==> Installing Node 22..."
    curl -fsSL https://deb.nodesource.com/setup_22.x | bash -
    apt-get install -y --no-install-recommends nodejs
else
    echo "==> Node already installed: $(node --version)"
fi

if ! command -v pnpm &>/dev/null; then
    echo "==> Installing pnpm..."
    corepack enable
    corepack prepare pnpm@latest --activate
else
    echo "==> pnpm already installed: $(pnpm --version)"
fi

# --- air (Go hot-reload) ---
if ! command -v air &>/dev/null; then
    echo "==> Installing air..."
    go install github.com/air-verse/air@latest
else
    echo "==> air already installed: $(air -v 2>&1 | head -1)"
fi

# --- Create directories ---
mkdir -p "${DEV_DIR}/Silo/web/dist"
mkdir -p "${DEV_DIR}/silo-plugin-sdk"
mkdir -p /tmp/silo-transcode
mkdir -p /opt/silo/plugins /opt/silo/transcode /opt/silo/postgres /opt/silo/redis

# Jellycompat debug logging bind-mounts a file, not a directory.
DEBUG_LOG_PATH="/opt/silo/jellycompat-debug.log"
if [ -d "${DEBUG_LOG_PATH}" ]; then
    if ! rmdir "${DEBUG_LOG_PATH}" 2>/dev/null; then
        mv "${DEBUG_LOG_PATH}" "${DEBUG_LOG_PATH}.dir.$(date +%s)"
    fi
fi
touch "${DEBUG_LOG_PATH}"

# Symlink plugin cache so DB paths (using Docker's /var/lib/silo/plugins)
# resolve correctly when running natively.
mkdir -p /var/lib/silo
if [ ! -L /var/lib/silo/plugins ] && [ -d /opt/silo/plugins ]; then
    ln -sf /opt/silo/plugins /var/lib/silo/plugins
fi

# --- Create .env for native execution ---
ENV_FILE="${DEV_DIR}/Silo/.env"
if [ ! -f "${ENV_FILE}" ]; then
    echo "==> Creating ${ENV_FILE}..."
    cat > "${ENV_FILE}" <<'EOF'
DATABASE_URL=postgres://silo:silo@localhost:5432/silo?sslmode=disable
REDIS_URL=redis://localhost:6379
PORT=8090
JF_PORT=8096
MODE=integrated
SILO_PLUGIN_CACHE_DIR=/var/lib/silo/plugins
EOF
else
    echo "==> ${ENV_FILE} already exists, skipping"
fi

echo ""
echo "=== Setup complete ==="
echo "Go:       $(go version)"
echo "Node:     $(node --version)"
echo "pnpm:     $(pnpm --version)"
echo "air:      $(air -v 2>&1 | head -1)"
echo "ffmpeg:   $(/usr/lib/jellyfin-ffmpeg/ffmpeg -version 2>&1 | head -1)"
echo "Dev dir:  ${DEV_DIR}"
REMOTE

echo "==> Done! Run 'make dev-deploy' to start developing."
