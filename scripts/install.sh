#!/bin/sh
# shield-agent installer
# Usage: curl -sSL https://raw.githubusercontent.com/itdar/shield-agent/main/scripts/install.sh | sh
#
# Environment variables:
#   INSTALL_DIR   — installation directory (default: /usr/local/bin)
#   GITHUB_TOKEN  — GitHub token for private repo access (optional)
#   VERSION       — specific version to install (default: latest)
set -e

REPO="itdar/shield-agent"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"

# Detect OS and architecture.
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)

case "$ARCH" in
  x86_64|amd64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) echo "Error: unsupported architecture: $ARCH" >&2; exit 1 ;;
esac

case "$OS" in
  linux|darwin) ;;
  *) echo "Error: unsupported OS: $OS" >&2; exit 1 ;;
esac

# Build auth header if token is provided (for private repos).
AUTH_HEADER=""
if [ -n "$GITHUB_TOKEN" ]; then
  AUTH_HEADER="Authorization: token $GITHUB_TOKEN"
fi

# Helper: fetch URL with optional auth.
fetch() {
  if command -v curl >/dev/null 2>&1; then
    if [ -n "$AUTH_HEADER" ]; then
      curl -sSL -H "$AUTH_HEADER" "$1"
    else
      curl -sSL "$1"
    fi
  elif command -v wget >/dev/null 2>&1; then
    if [ -n "$AUTH_HEADER" ]; then
      wget -qO- --header="$AUTH_HEADER" "$1"
    else
      wget -qO- "$1"
    fi
  else
    echo "Error: curl or wget is required" >&2
    exit 1
  fi
}

# Helper: download file with optional auth.
download() {
  if command -v curl >/dev/null 2>&1; then
    if [ -n "$AUTH_HEADER" ]; then
      curl -sSL -H "$AUTH_HEADER" -H "Accept: application/octet-stream" "$1" -o "$2"
    else
      curl -sSL "$1" -o "$2"
    fi
  else
    if [ -n "$AUTH_HEADER" ]; then
      wget -q --header="$AUTH_HEADER" --header="Accept: application/octet-stream" "$1" -O "$2"
    else
      wget -q "$1" -O "$2"
    fi
  fi
}

# Determine version.
if [ -n "$VERSION" ]; then
  LATEST="v${VERSION#v}"
else
  echo "Fetching latest release..."
  RELEASE_JSON=$(fetch "https://api.github.com/repos/${REPO}/releases/latest")

  # Check for API errors (404, 403, etc.)
  if echo "$RELEASE_JSON" | grep -q '"message"'; then
    MSG=$(echo "$RELEASE_JSON" | grep '"message"' | sed -E 's/.*"message": *"([^"]+)".*/\1/')
    echo "Error: GitHub API returned: $MSG" >&2
    if echo "$MSG" | grep -qi "not found"; then
      echo "" >&2
      echo "Possible causes:" >&2
      echo "  - No releases published yet" >&2
      echo "  - Repository is private (set GITHUB_TOKEN env var)" >&2
      echo "  - Repository name is incorrect" >&2
    fi
    exit 1
  fi

  LATEST=$(echo "$RELEASE_JSON" | grep '"tag_name"' | sed -E 's/.*"([^"]+)".*/\1/')
  if [ -z "$LATEST" ]; then
    echo "Error: could not determine latest release version" >&2
    exit 1
  fi
fi

VERSION="${LATEST#v}"
FILENAME="shield-agent_${VERSION}_${OS}_${ARCH}.tar.gz"
URL="https://github.com/${REPO}/releases/download/${LATEST}/${FILENAME}"

echo "Downloading shield-agent ${LATEST} for ${OS}/${ARCH}..."

TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT

download "$URL" "${TMPDIR}/${FILENAME}"

# Verify download is a valid tar.gz (not an HTML error page).
if ! tar -tzf "${TMPDIR}/${FILENAME}" >/dev/null 2>&1; then
  echo "Error: downloaded file is not a valid archive" >&2
  echo "The release asset may not exist. Check:" >&2
  echo "  - https://github.com/${REPO}/releases/tag/${LATEST}" >&2
  echo "  - Expected asset: ${FILENAME}" >&2
  exit 1
fi

tar -xzf "${TMPDIR}/${FILENAME}" -C "$TMPDIR"

if [ ! -f "${TMPDIR}/shield-agent" ]; then
  echo "Error: shield-agent binary not found in archive" >&2
  exit 1
fi

if [ -w "$INSTALL_DIR" ]; then
  mv "${TMPDIR}/shield-agent" "${INSTALL_DIR}/shield-agent"
else
  echo "Installing to ${INSTALL_DIR} (requires sudo)..."
  sudo mv "${TMPDIR}/shield-agent" "${INSTALL_DIR}/shield-agent"
fi

chmod +x "${INSTALL_DIR}/shield-agent"

echo ""
echo "shield-agent ${LATEST} installed to ${INSTALL_DIR}/shield-agent"
echo "Run 'shield-agent --help' to get started."
