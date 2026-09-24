#!/bin/sh
# Build and test on the build machine.
#
# Why this exists: the editing machine has no C compiler, so `-race` cannot run
# there, and anything shipped has to come off the same machine CI uses or the
# reproducibility claim in the Makefile is untested. Rather than everyone
# reconstructing the same rsync-then-ssh incantation with slightly different
# excludes, it lives here once.
#
#   scripts/remote.sh                 rsync the tree, then run `make check`
#   scripts/remote.sh make repro      run any command in the remote tree
#   scripts/remote.sh make dist
#
# The working tree is synced as it is, uncommitted changes included, so what
# builds there is what you are editing here. --delete means the remote copy is
# a mirror: files removed locally go away remotely too, and a stale object left
# behind by an earlier layout cannot quietly take part in a build.
#
# Overridable: REMOTE (user@host), REMOTE_DIR (path on the build machine).

set -eu

REMOTE="${REMOTE:-guillaume@10.0.50.21}"
REMOTE_DIR="${REMOTE_DIR:-claudication}"

# shellcheck disable=SC1007  # clearing CDPATH for this one command, not assigning
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)

# node_modules is excluded and reinstalled from the lockfile on the far side:
# it holds platform-specific binaries (esbuild, rollup), and copying this
# machine's over would be both slow and wrong.
#
# dist/ and webdist/ are build outputs. Syncing them would let a local artifact
# masquerade as something the build machine produced, which is the exact
# confusion this script is meant to remove.
rsync -a --delete \
  --exclude '.git/' \
  --exclude 'web/node_modules/' \
  --exclude 'dist/' \
  --exclude 'internal/httpapi/webdist/assets/' \
  --exclude 'internal/httpapi/webdist/index.html' \
  --exclude '.serena/' \
  --exclude '.claude/' \
  "$root"/ "$REMOTE:$REMOTE_DIR/"

# Carry the version stamp across, because .git is not synced.
#
# Without this the build machine has no repository to read and every artifact
# it produces calls itself "dev / none / 1970-01-01" — which would make the
# binary that actually ships the one binary nobody can identify. Reading the
# values here and passing them over keeps the stamp a property of the source
# and, as a side effect, makes a remote build byte-identical to a local one, so
# `make repro` on either machine is checking the same thing.
VERSION=$(git -C "$root" describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT=$(git -C "$root" rev-parse --short HEAD 2>/dev/null || echo none)
SOURCE_DATE_EPOCH=$(git -C "$root" log -1 --format=%ct 2>/dev/null || echo 0)
stamp="VERSION=$VERSION COMMIT=$COMMIT SOURCE_DATE_EPOCH=$SOURCE_DATE_EPOCH"

# The default does what you almost always want: install the UI's dependencies
# if they are not there yet, then run everything CI runs. `npm ci` deletes and
# refetches node_modules, so it is worth skipping when the tree already has it.
if [ "$#" -eq 0 ]; then
  command='[ -d web/node_modules ] || make web-deps; make check'
else
  command="$*"
fi

# A login shell, so a Node or Go installed under ~/.local or a version manager
# is on PATH the same way it is for a person sitting at that machine.
exec ssh "$REMOTE" "cd $REMOTE_DIR && . ~/.profile 2>/dev/null; export $stamp; $command"
