#!/bin/sh
# Install claudication from a GitHub release.
#
#   curl -fsSL https://raw.githubusercontent.com/nebuloss/claudication/main/scripts/install.sh | sh
#
# Environment:
#   VERSION   tag to install (default: the latest release)
#   BINDIR    where to put the binary (default: /usr/local/bin, or ~/.local/bin
#             when that is not writable and sudo is not available)
#   REPO, GITHUB_API, GITHUB_DL
#             point the script at a fork or a mirror
#
# POSIX sh on purpose: this runs on whatever the machine happens to have.
set -eu

REPO=${REPO:-nebuloss/claudication}
GITHUB_API=${GITHUB_API:-https://api.github.com}
GITHUB_DL=${GITHUB_DL:-https://github.com}
BINARY=claudication
VERSION=${VERSION:-}
BINDIR=${BINDIR:-}

say()  { printf '%s\n' "$*"; }
warn() { printf '%s\n' "$*" >&2; }
die()  { printf 'install: %s\n' "$*" >&2; exit 1; }

need() { command -v "$1" >/dev/null 2>&1; }

# fetch <url> <dest>, or to stdout when dest is "-".
fetch() {
  if need curl; then
    if [ "$2" = - ]; then curl -fsSL "$1"; else curl -fsSL -o "$2" "$1"; fi
  elif need wget; then
    if [ "$2" = - ]; then wget -qO- "$1"; else wget -qO "$2" "$1"; fi
  else
    die 'neither curl nor wget is installed'
  fi
}

# The release assets are named by GOOS/GOARCH, so the job here is to translate
# uname's vocabulary into Go's.
detect_platform() {
  os=$(uname -s | tr '[:upper:]' '[:lower:]')
  arch=$(uname -m)

  case $os in
    linux | darwin | freebsd) ;;
    *) die "unsupported OS: $os (Windows builds are on the releases page)" ;;
  esac

  case $arch in
    x86_64 | amd64) arch=amd64 ;;
    aarch64 | arm64) arch=arm64 ;;
    armv7l | armv6l | arm) arch=arm ;;
    riscv64) arch=riscv64 ;;
    *) die "unsupported architecture: $arch" ;;
  esac

  PLATFORM="$os-$arch"
}

# Resolve the newest release without needing jq: the API answer has exactly one
# "tag_name" field, and it is the first one.
latest_version() {
  fetch "$GITHUB_API/repos/$REPO/releases/latest" - |
    sed -n 's/.*"tag_name" *: *"\([^"]*\)".*/\1/p' |
    head -n 1
}

# checksum <file> -> lowercase hex
checksum() {
  if need sha256sum; then
    sha256sum "$1" | cut -d' ' -f1
  elif need shasum; then
    shasum -a 256 "$1" | cut -d' ' -f1
  else
    return 1
  fi
}

pick_bindir() {
  if [ -n "$BINDIR" ]; then
    return 0
  fi

  for candidate in /usr/local/bin /usr/bin; do
    if [ -w "$candidate" ]; then
      BINDIR=$candidate
      return 0
    fi
  done
  if need sudo; then
    BINDIR=/usr/local/bin
    SUDO=sudo
    return 0
  fi

  # No root and no sudo: land somewhere the user owns rather than failing.
  BINDIR=$HOME/.local/bin
  warn "not root and sudo is unavailable; installing to $BINDIR"
}

main() {
  detect_platform

  if [ -z "$VERSION" ]; then
    VERSION=$(latest_version) || true
    [ -n "$VERSION" ] || die "could not find the latest release of $REPO"
  fi

  SUDO=
  pick_bindir
  mkdir -p "$BINDIR" 2>/dev/null || true

  asset="$BINARY-$PLATFORM"
  base="$GITHUB_DL/$REPO/releases/download/$VERSION"

  tmp=$(mktemp -d) || die 'could not create a temporary directory'
  trap 'rm -rf "$tmp"' EXIT INT TERM

  say "claudication $VERSION ($PLATFORM)"
  fetch "$base/$asset" "$tmp/$asset" ||
    die "no build for $PLATFORM in $VERSION; see $GITHUB_DL/$REPO/releases"

  # Verifying is not optional when the payload came off the network, but a
  # machine with no sha256 tool should still be told rather than silently
  # skipped past.
  if fetch "$base/SHA256SUMS" "$tmp/SHA256SUMS" 2>/dev/null; then
    expected=$(sed -n "s/^\([0-9a-f]\{64\}\)  *$asset\$/\1/p" "$tmp/SHA256SUMS" | head -n 1)
    if [ -z "$expected" ]; then
      warn "warning: $asset is not listed in SHA256SUMS; not verified"
    elif actual=$(checksum "$tmp/$asset"); then
      [ "$actual" = "$expected" ] || die "checksum mismatch for $asset"
      say "checksum ok"
    else
      warn 'warning: no sha256sum or shasum available; not verified'
    fi
  else
    warn 'warning: SHA256SUMS is missing from the release; not verified'
  fi

  chmod +x "$tmp/$asset"
  $SUDO mv "$tmp/$asset" "$BINDIR/$BINARY" ||
    die "could not install to $BINDIR (set BINDIR= to choose another location)"

  say "installed $BINDIR/$BINARY"
  "$BINDIR/$BINARY" version 2>/dev/null || true

  case ":$PATH:" in
    *":$BINDIR:"*) ;;
    *) warn "note: $BINDIR is not on your PATH" ;;
  esac

  cat <<EOF

Next:
  $BINARY passwd                 set the admin password
  $BINARY serve                  run the gateway on :8317

then open http://127.0.0.1:8317/
EOF
}

main "$@"
