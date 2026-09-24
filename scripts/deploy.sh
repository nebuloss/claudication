#!/bin/sh
# Build a commit on the build machine and install it on a gateway host.
#
# Why this exists: every upgrade before it was a handful of commands typed by
# hand or by an agent — copy a binary, stop, swap, start — and the one run on
# 2026-09-24 started the new process while the old one still held the admin
# port, leaving the gateway down. The install script now does the upgrade
# safely (pre-flight, backup, wait for every port, verify, roll back); this is
# the one way to get a local build to it, so nobody improvises the rest.
#
#   scripts/deploy.sh                 deploy HEAD
#   scripts/deploy.sh v0.15.0         deploy a tag, or any pushed ref
#
# The commit is built from the remote repository, not from this working tree,
# so it has to be pushed first: what runs in production is then something
# anyone can check out and rebuild to the same bytes (see `make repro`).
#
# Overridable:
#   BUILD_HOST   user@host that builds            (guillaume@10.0.50.21)
#   BUILD_DIR    clone on the build host          (claudication-deploy)
#   TARGET       user@host that runs the gateway  (root@10.0.0.2)
#   PCT          Proxmox container id on TARGET, empty for TARGET itself (407)
#   SKIP_CHECK   1 to skip `make check` before building
#   BUILD_WRAP   command prefixed to make on the build host, e.g. rtk

set -eu

BUILD_HOST="${BUILD_HOST:-guillaume@10.0.50.21}"
BUILD_DIR="${BUILD_DIR:-claudication-deploy}"
TARGET="${TARGET:-root@10.0.0.2}"
PCT="${PCT-407}"
SKIP_CHECK="${SKIP_CHECK:-0}"
BUILD_WRAP="${BUILD_WRAP:-}"

# shellcheck disable=SC1007  # clearing CDPATH for this one command, not assigning
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
ref="${1:-HEAD}"
commit=$(git -C "$root" rev-parse --verify "$ref^{commit}")
repo=$(git -C "$root" remote get-url origin)

# Pushed, or the build machine cannot fetch it.
if ! git -C "$root" branch -r --contains "$commit" | grep -q . &&
   ! git -C "$root" tag --contains "$commit" | grep -q .; then
  echo "deploy: $commit is not on any remote branch — push it first" >&2
  exit 1
fi

echo "== build $commit on $BUILD_HOST"
if [ "$SKIP_CHECK" = 1 ]; then targets="build"; else targets="check build"; fi
# shellcheck disable=SC2029  # expanded here on purpose
ssh "$BUILD_HOST" "set -e; . ~/.profile 2>/dev/null || true
  [ -d '$BUILD_DIR/.git' ] || git clone -q '$repo' '$BUILD_DIR'
  cd '$BUILD_DIR'
  git fetch -q --tags origin
  git checkout -q --detach '$commit'
  git clean -qfdx -e web/node_modules
  [ -d web/node_modules ] || make web-deps >/dev/null
  $BUILD_WRAP make $targets"

# shellcheck disable=SC2029
sum=$(ssh "$BUILD_HOST" "cd '$BUILD_DIR' && sha256sum dist/claudication" | awk '{print $1}')
version=$(ssh "$BUILD_HOST" "cd '$BUILD_DIR' && ./dist/claudication version")
echo "== built $version"
echo "   sha256 $sum"

# Streamed straight from the build host to the target: the binary never lands
# on the machine running this script.
tmp="/tmp/claudication-deploy-$commit"
echo "== copy to $TARGET${PCT:+ (container $PCT)}"
# shellcheck disable=SC2029
ssh "$BUILD_HOST" "cd '$BUILD_DIR' && tar -c dist/claudication scripts/install.sh" |
  ssh "$TARGET" "rm -rf '$tmp' && mkdir -p '$tmp' && tar -x -C '$tmp'"

install="BINARY_FILE=/tmp/claudication-deploy/dist/claudication BINARY_SHA256=$sum QUIET=1 sh /tmp/claudication-deploy/scripts/install.sh"
echo "== install"
if [ -n "$PCT" ]; then
  # shellcheck disable=SC2029
  ssh "$TARGET" "set -e
    pct exec $PCT -- mkdir -p /tmp/claudication-deploy/dist /tmp/claudication-deploy/scripts
    pct push $PCT '$tmp/dist/claudication' /tmp/claudication-deploy/dist/claudication --perms 0755
    pct push $PCT '$tmp/scripts/install.sh' /tmp/claudication-deploy/scripts/install.sh
    rm -rf '$tmp'
    status=0
    pct exec $PCT -- sh -c '$install' || status=\$?
    pct exec $PCT -- rm -rf /tmp/claudication-deploy
    exit \$status"
else
  # shellcheck disable=SC2029
  ssh "$TARGET" "rm -rf /tmp/claudication-deploy && mv '$tmp' /tmp/claudication-deploy
    status=0; $install || status=\$?
    rm -rf /tmp/claudication-deploy; exit \$status"
fi
echo "== deployed $version"
