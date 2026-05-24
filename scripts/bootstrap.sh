#!/usr/bin/env bash
# Bootstrap the local development environment.
# Safe to run multiple times (idempotent).
set -euo pipefail

REQUIRED_RAM_GB=8
REQUIRED_DISK_GB=20

log()  { echo "[bootstrap] $*"; }
fail() { echo "[bootstrap] ERROR: $*" >&2; exit 1; }

# ── System checks ──────────────────────────────────────────────────────────────
log "Checking system requirements..."

if command -v free > /dev/null 2>&1; then
  RAM_GB=$(free -g | awk '/^Mem:/{print $2}')
  [ "$RAM_GB" -ge "$REQUIRED_RAM_GB" ] || fail "Need >= ${REQUIRED_RAM_GB}GB RAM (found ${RAM_GB}GB)"
fi

DISK_GB=$(df -BG . | awk 'NR==2{gsub("G","",$4); print $4}')
[ "${DISK_GB:-0}" -ge "$REQUIRED_DISK_GB" ] || log "WARNING: low disk space (< ${REQUIRED_DISK_GB}GB free)"

# ── Tool version checks ────────────────────────────────────────────────────────
check_tool() {
  command -v "$1" > /dev/null 2>&1 || fail "$1 is required but not installed. See docs/development.md"
}

check_tool docker
check_tool go
check_tool node
check_tool pnpm
check_tool terraform
check_tool vault

GO_VERSION=$(go version | awk '{print $3}' | sed 's/go//')
log "Go $GO_VERSION"

NODE_VERSION=$(node --version)
log "Node $NODE_VERSION"

# ── Install pnpm dependencies ─────────────────────────────────────────────────
log "Installing pnpm dependencies..."
pnpm install --frozen-lockfile

# ── Install husky hooks ───────────────────────────────────────────────────────
log "Installing git hooks (husky)..."
pnpm exec husky

# ── Install golangci-lint ─────────────────────────────────────────────────────
if ! command -v golangci-lint > /dev/null 2>&1; then
  log "Installing golangci-lint..."
  curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b "$(go env GOPATH)/bin" v1.59.0
fi

# ── Install gitleaks ─────────────────────────────────────────────────────────
if ! command -v gitleaks > /dev/null 2>&1; then
  log "Installing gitleaks..."
  GITLEAKS_VERSION=8.18.4
  OS=$(uname -s | tr '[:upper:]' '[:lower:]')
  ARCH=$(uname -m | sed 's/x86_64/x64/')
  curl -sSfL "https://github.com/gitleaks/gitleaks/releases/download/v${GITLEAKS_VERSION}/gitleaks_${GITLEAKS_VERSION}_${OS}_${ARCH}.tar.gz" \
    | tar -xz -C /usr/local/bin gitleaks
fi

# ── Copy env example ─────────────────────────────────────────────────────────
if [ ! -f .env ]; then
  log "Copying .env.example → .env"
  cp .env.example .env
fi

log "Bootstrap complete. Run: make up"
