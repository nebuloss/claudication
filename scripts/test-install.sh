#!/bin/sh
# Checks install.sh's update decisions without root, a service manager or a
# network: install.sh is sourced, the steps that touch the system are replaced
# by stubs that record their names, and each case asserts the order they ran
# in. What it proves is the choice — hand over or stop and start, and how a
# failure rolls back — not that rc-service or systemctl do their part; that is
# the live test in docs/deploying.md.
#
#   sh scripts/test-install.sh

set -eu

here=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

export INSTALL_SH_SOURCED=1 QUIET=1 NO_COLOR=1
BIN_DIR="$tmp/bin" CONFIG_FILE="$tmp/config.yaml"
export BIN_DIR CONFIG_FILE
# shellcheck source=install.sh
. "$here/install.sh"
OS=alpine

failures=0
calls="$tmp/calls"

check() {
  name=$1 want=$2
  got=$(tr '\n' ' ' < "$calls" | sed 's/ $//')
  if [ "$got" = "$want" ]; then
    printf 'ok   %s\n' "$name"
  else
    printf 'FAIL %s\n     want: %s\n     got:  %s\n' "$name" "$want" "$got"
    failures=$((failures + 1))
  fi
}

record() { echo "$1" >> "$calls"; }

# The world, as each case sets it.
running=1            # service_running
health=''            # what /health answers
handover_ok=1        # whether the new worker takes over
serving=''           # what serving_version says

stub_world() {
  : > "$calls"
  require_root()  { :; }
  detect_arch()   { :; }
  ensure_tools()  { :; }
  ensure_user()   { :; }
  ensure_dirs()   { :; }
  backup_state()  { record backup_state; }
  fetch_binary()  { BEFORE=v1; NEW_VERSION=v2; record fetch_binary; }
  service_install() { record service_install; }
  service_running() { [ "$running" = 1 ]; }
  curl()          { printf '%s' "$health"; }
  service_reload() { record service_reload; }
  stop_and_release() { record stop_and_release; }
  swap_binary()   { record swap_binary; }
  claim_gateway() { record claim_gateway; }
  service_start() { record service_start; }
  wait_ready()    { record wait_ready; }
  wait_handed_over() { record wait_handed_over; [ "$handover_ok" = 1 ]; }
  print_summary() { record print_summary; }
  sleep()         { :; }
}

run_main() {
  rollback() { record rollback; return 1; }
  (main) >/dev/null 2>&1 && echo "exit 0" >> "$calls" || echo "exit 1" >> "$calls"
}

# A gateway under the supervisor hands over: no stop, the claim only after the
# new version serves (the new binary would otherwise migrate the database
# under the old worker).
stub_world
health='{"status":"ok","version":"v1","accepting":true,"reloadable":true}'
run_main
check "a reloadable gateway hands over" \
  "fetch_binary backup_state service_install swap_binary service_reload wait_handed_over claim_gateway print_summary exit 0"

# A release from before the handover says nothing on /health, and has to be
# stopped: the first deploy of the new install.sh onto an old binary.
stub_world
health='{"status":"ok","version":"v1","accepting":true}'
run_main
check "an old binary is stopped and started" \
  "fetch_binary backup_state service_install stop_and_release swap_binary claim_gateway service_start wait_ready print_summary exit 0"

stub_world
health='{"status":"ok","version":"v1","accepting":true,"reloadable":true}'
HANDOVER=0
run_main
HANDOVER=1
check "HANDOVER=0 stops and starts" \
  "fetch_binary backup_state service_install stop_and_release swap_binary claim_gateway service_start wait_ready print_summary exit 0"

stub_world
running=0
health=''
run_main
running=1
check "a stopped service is started" \
  "fetch_binary backup_state service_install stop_and_release swap_binary claim_gateway service_start wait_ready print_summary exit 0"

# A handover the new version never completes rolls back, and the run fails.
stub_world
health='{"status":"ok","version":"v1","accepting":true,"reloadable":true}'
handover_ok=0
run_main
handover_ok=1
check "a failed handover rolls back" \
  "fetch_binary backup_state service_install swap_binary service_reload wait_handed_over rollback exit 1"

# rollback itself, with the stubs it calls. Its own code runs; the binary
# swap is real, in a scratch directory.
. "$here/install.sh"
stub_world
ready() { [ "$serving" = "$NEW_VERSION" ]; }
serving_version() { printf '%s' "$serving"; }
mkdir -p "$BIN_DIR"

prepare_rollback() {
  : > "$calls"
  echo new > "$BINARY"
  echo old > "$BINARY.prev"
  chmod +x "$BINARY" "$BINARY.prev"
  BEFORE=v1 NEW_VERSION=v2
}

# The new worker never got ready: the old one is still serving, so putting
# its binary back is all there is to do — nothing is stopped.
prepare_rollback
serving=v1
wait_ready() { record wait_ready; }
(rollback) >/dev/null 2>&1 && echo "exit 0" >> "$calls"
[ "$(cat "$BINARY")" = old ] && echo "binary=old" >> "$calls"
check "rollback with the old version still serving touches nothing" "exit 0 binary=old"

# The handover happened and the new version then failed: hand back to the
# previous one the same way it was handed over.
prepare_rollback
serving=v2
BEFORE_RELOADABLE=1
health='{"reloadable":true}'
wait_ready() { record wait_ready; serving=$NEW_VERSION; }
(rollback) >/dev/null 2>&1 && echo "exit 0" >> "$calls"
check "rollback after a handover hands back" "service_reload wait_ready exit 0"

# The previous version was not serving under the supervisor: its binary
# cannot be a worker, so the way back is a stop and a start.
prepare_rollback
serving=v2
BEFORE_RELOADABLE=0
(rollback) >/dev/null 2>&1 && echo "exit 0" >> "$calls"
check "rollback to an old binary stops and starts" "stop_and_release service_start wait_ready exit 0"

# swap_binary, for real: renamed into place, so a running process keeps its
# inode, and the previous one kept beside it.
. "$here/install.sh"
: > "$calls"
echo current > "$BINARY"
chmod +x "$BINARY"
before=$(ls -i "$BINARY" | awk '{print $1}')
NEW_DIR="$tmp/new" NEW_BIN="$tmp/new/claudication"
mkdir -p "$NEW_DIR"
echo next > "$NEW_BIN"
swap_binary
after=$(ls -i "$BINARY" | awk '{print $1}')
[ "$(cat "$BINARY")" = next ] && echo "new in place" >> "$calls"
[ "$(cat "$BINARY.prev")" = current ] && echo "previous kept" >> "$calls"
[ "$before" != "$after" ] && echo "renamed" >> "$calls"
[ -x "$BINARY" ] && echo "executable" >> "$calls"
check "swap_binary renames the new binary into place" "new in place previous kept renamed executable"

if [ "$failures" -gt 0 ]; then
  echo "$failures failed"
  exit 1
fi
echo "all passed"
