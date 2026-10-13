#!/bin/sh
# Install pinner from a GitHub release, verifying the archive's SHA-256 checksum.
#
#   curl -fsSL https://raw.githubusercontent.com/osean-man/pinner/master/install.sh | sh
#
# Environment:
#   PINNER_VERSION   release tag to install (default: latest), e.g. v1.0.0
#   BIN_DIR          install directory (default: /usr/local/bin; uses sudo if not writable)
#   PINNER_VERIFY_ATTESTATION=1   also verify the build provenance with `gh attestation verify`
set -eu

REPO="osean-man/pinner"
VERSION="${PINNER_VERSION:-latest}"
BIN_DIR="${BIN_DIR:-/usr/local/bin}"
BASE="${PINNER_DOWNLOAD_BASE:-https://github.com/$REPO/releases/download}"

fail() { echo "install.sh: $*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || fail "$1 is required"; }
need curl
need tar
need uname

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in
  linux|darwin|freebsd) ;;
  *) fail "unsupported OS: $os (download a release manually from https://github.com/$REPO/releases)" ;;
esac
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  armv7l|armv7) arch=armv7 ;;
  i386|i686) arch=386 ;;
  riscv64) arch=riscv64 ;;
  *) fail "unsupported architecture: $(uname -m)" ;;
esac

if [ "$VERSION" = latest ]; then
  # The /releases/latest page redirects to /releases/tag/<tag>; this avoids the API rate limit.
  url=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest")
  VERSION=${url##*/}
  if [ -z "$VERSION" ] || [ "$VERSION" = latest ]; then
    fail "could not determine the latest release"
  fi
fi
plain=${VERSION#v}
archive="pinner_${plain}_${os}_${arch}.tar.gz"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM
echo "Downloading pinner $VERSION ($os/$arch)..."
curl -fsSL -o "$tmp/$archive" "$BASE/$VERSION/$archive" || fail "download failed: $BASE/$VERSION/$archive"
curl -fsSL -o "$tmp/checksums.txt" "$BASE/$VERSION/checksums.txt" || fail "could not download checksums.txt"

expected=$(awk -v f="$archive" '$2 == f { print $1 }' "$tmp/checksums.txt")
[ -n "$expected" ] || fail "$archive is not listed in checksums.txt"
if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$tmp/$archive" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
  actual=$(shasum -a 256 "$tmp/$archive" | awk '{print $1}')
else
  fail "sha256sum or shasum is required to verify the download"
fi
[ "$expected" = "$actual" ] || fail "checksum mismatch for $archive (expected $expected, got $actual)"
echo "Checksum verified."

if [ "${PINNER_VERIFY_ATTESTATION:-0}" = 1 ]; then
  need gh
  gh attestation verify "$tmp/$archive" --repo "$REPO" >/dev/null || fail "build provenance verification failed"
  echo "Build provenance verified."
fi

tar -xzf "$tmp/$archive" -C "$tmp" pinner
chmod 0755 "$tmp/pinner"
if [ -w "$BIN_DIR" ] || { [ -e "$BIN_DIR/pinner" ] && [ -w "$BIN_DIR/pinner" ]; }; then
  mkdir -p "$BIN_DIR" && cp "$tmp/pinner" "$BIN_DIR/pinner"
else
  echo "Installing to $BIN_DIR needs sudo."
  sudo mkdir -p "$BIN_DIR" && sudo cp "$tmp/pinner" "$BIN_DIR/pinner"
fi
echo "Installed: $("$BIN_DIR/pinner" --version)"
case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *) echo "Note: $BIN_DIR is not on your PATH." ;;
esac
