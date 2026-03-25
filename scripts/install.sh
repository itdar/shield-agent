#!/bin/sh
# shield-agent installer
# Usage: curl -sSL https://raw.githubusercontent.com/itdar/shield-agent/main/scripts/install.sh | sh
set -e

REPO="itdar/shield-agent"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"

# Detect OS and architecture.
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)

case "$ARCH" in
  x86_64|amd64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) echo "Unsupported architecture: $ARCH" >&2; exit 1 ;;
esac

case "$OS" in
  linux|darwin) ;;
  *) echo "Unsupported OS: $OS" >&2; exit 1 ;;
esac

# Get latest release tag.
if command -v curl >/dev/null 2>&1; then
  LATEST=$(curl -sSL "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | sed -E 's/.*"([^"]+)".*/\1/')
elif command -v wget >/dev/null 2>&1; then
  LATEST=$(wget -qO- "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | sed -E 's/.*"([^"]+)".*/\1/')
else
  echo "Error: curl or wget is required" >&2
  exit 1
fi

if [ -z "$LATEST" ]; then
  echo "Error: could not determine latest release" >&2
  exit 1
fi

VERSION="${LATEST#v}"
FILENAME="shield-agent_${VERSION}_${OS}_${ARCH}.tar.gz"
URL="https://github.com/${REPO}/releases/download/${LATEST}/${FILENAME}"

echo "Downloading shield-agent ${LATEST} for ${OS}/${ARCH}..."

TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT

if command -v curl >/dev/null 2>&1; then
  curl -sSL "$URL" -o "${TMPDIR}/${FILENAME}"
else
  wget -q "$URL" -O "${TMPDIR}/${FILENAME}"
fi

tar -xzf "${TMPDIR}/${FILENAME}" -C "$TMPDIR"

if [ -w "$INSTALL_DIR" ]; then
  mv "${TMPDIR}/shield-agent" "${INSTALL_DIR}/shield-agent"
else
  echo "Installing to ${INSTALL_DIR} (requires sudo)..."
  sudo mv "${TMPDIR}/shield-agent" "${INSTALL_DIR}/shield-agent"
fi

chmod +x "${INSTALL_DIR}/shield-agent"

echo "shield-agent ${LATEST} installed to ${INSTALL_DIR}/shield-agent"
echo ""
echo "Run 'shield-agent --help' to get started."
