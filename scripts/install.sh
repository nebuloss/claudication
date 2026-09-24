#!/bin/sh
# claudication — install / update (the same command does both)
#
# Installs the gateway as a service and starts it. One static binary, no
# runtime, no dependencies behind it.
#
# Usage, as root:
#   curl -fsSL https://raw.githubusercontent.com/nebuloss/claudication/main/scripts/install.sh | sh
#
# A minimal Alpine has wget but not curl, so there:
#   wget -qO- https://raw.githubusercontent.com/nebuloss/claudication/main/scripts/install.sh | sh
#
# Pin a version, or bind somewhere else:
#   curl -fsSL .../install.sh | VERSION=v0.2.0 LISTEN=127.0.0.1:8317 sh
#
# Install a binary you built yourself instead of a release (scripts/deploy.sh
# does this for you):
#   BINARY_FILE=/tmp/claudication sh install.sh
#
# An update never leaves the gateway down: the new binary has to run before
# anything is touched, the state is backed up first, and if the new version
# does not come up answering on every port, the previous one is put back.
#
# Systems: Alpine Linux (OpenRC), Debian/Ubuntu (systemd)
#
# Layout: settings, output helpers, a platform layer holding every
# Alpine/Debian difference, the install steps, and main() at the bottom —
# which reads as the list of things this script does.

set -eu

PATH="/usr/local/bin:$PATH"
export PATH


# ═══ Settings, all overridable from the environment ═══════════════════════════

BIN_DIR="${BIN_DIR:-/usr/local/bin}"
STATE_DIR="${STATE_DIR:-/var/lib/claudication}"
SERVICE_NAME="${SERVICE_NAME:-claudication}"
SERVICE_USER="${SERVICE_USER:-claudication}"
GH_REPO="${GH_REPO:-nebuloss/claudication}"
GH_API="${GH_API:-https://api.github.com}"
GH_DL="${GH_DL:-https://github.com}"
VERSION="${VERSION:-}"                # install this tag instead of the newest
# Whether this run named one, captured before the default below destroys the
# difference between "not mentioned" and "deliberately set to this".
LISTEN_GIVEN="${LISTEN+yes}"
LISTEN="${LISTEN:-0.0.0.0:8317}"      # what the service binds
# Put the admin API and UI on their own address, leaving LISTEN serving only
# the relay. Set this whenever the gateway is reachable from anywhere you do
# not control: the admin surface can add Claude accounts and mint API keys, and
# by default it shares the published listener. Empty keeps them together.
ADMIN_LISTEN="${ADMIN_LISTEN:-}"
CONFIG_DIR="${CONFIG_DIR:-/etc/claudication}"
CONFIG_FILE="${CONFIG_FILE:-$CONFIG_DIR/config.yaml}"
# Set this when something fronts the gateway (nginx, Nginx Proxy Manager,
# Traefik, a tunnel): comma-separated CIDRs or IPs the *proxy* connects from.
# Without it every client behind it shares one rate-limit bucket, the access
# log cannot tell them apart, and the session cookie cannot be marked Secure.
# Whether this run named one, captured before the default assignment destroys
# the difference between "not mentioned" and "deliberately emptied".
TRUSTED_PROXIES_GIVEN="${TRUSTED_PROXIES+yes}"
TRUSTED_PROXIES="${TRUSTED_PROXIES:-}"
BOOT_TIMEOUT="${BOOT_TIMEOUT:-30}"    # seconds to wait for the first response
# Seconds to wait for the old process to let go of its listeners. Longer than
# the shutdown grace it is draining under, or the wait is pointless.
STOP_TIMEOUT="${STOP_TIMEOUT:-150}"
SET_PASSWORD="${SET_PASSWORD:-1}"     # 0 to leave the gateway unclaimed
# A binary already on this machine, installed instead of downloading a release.
# For a build that has no release: a branch under test, a local fix.
BINARY_FILE="${BINARY_FILE:-}"
# Its expected digest, when the file came from somewhere you do not control.
BINARY_SHA256="${BINARY_SHA256:-}"
# Pre-upgrade snapshots of the state, newest kept, in $STATE_DIR/backups.
# 0 skips the snapshot — only for a version too old to have `backup`.
BACKUPS_KEPT="${BACKUPS_KEPT:-5}"
QUIET="${QUIET:-0}"                   # 1 to drop the progress dots

BINARY="$BIN_DIR/claudication"
LOG_FILE="/var/log/${SERVICE_NAME}.log"


# ═══ Output ═══════════════════════════════════════════════════════════════════

# Colour only when something is there to read it. Piped into a provisioning
# log, escape codes are noise that outlives the install.
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
  GREEN='\033[0;32m'; YELLOW='\033[1;33m'; RED='\033[0;31m'
  BOLD='\033[1m'; DIM='\033[2m'; NC='\033[0m'
else
  GREEN=''; YELLOW=''; RED=''; BOLD=''; DIM=''; NC=''
fi

info() { printf "${GREEN}[+]${NC} %s\n" "$*"; }
warn() { printf "${YELLOW}[!]${NC} %s\n" "$*"; }
fail() { printf "${RED}[x]${NC} %s\n" "$*"; }
step() { printf "${DIM}    %s${NC}\n" "$*"; }
die()  { fail "$*"; exit 1; }

tick()    { [ "$QUIET" = "1" ] || printf '.'; }
endline() { [ "$QUIET" = "1" ] || printf '\n'; }

dump_logs() {
  printf '\n'
  warn "last log lines:"
  service_tail
}


# ═══ Platform ═════════════════════════════════════════════════════════════════
#
# The only place Alpine and Debian differ. Every step below goes through these,
# so supporting another distribution means editing this section and nothing
# else.

detect_platform() {
  if [ -f /etc/alpine-release ]; then
    OS=alpine
  elif [ -f /etc/debian_version ]; then
    OS=debian
  else
    die "unsupported system (needs Alpine or Debian/Ubuntu)"
  fi
}

# Which build to fetch, from the machine's own architecture.
#
# Getting this wrong is not a friendly failure: the wrong binary runs and the
# kernel says "Exec format error", which says nothing about why.
detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64)  ASSET=claudication-linux-amd64 ;;
    aarch64|arm64) ASSET=claudication-linux-arm64 ;;
    armv7l|armv6l) ASSET=claudication-linux-arm ;;
    riscv64)       ASSET=claudication-linux-riscv64 ;;
    *) die "no build for $(uname -m) — amd64, arm64, arm and riscv64 are published" ;;
  esac
}

pkg_install() {
  case "$OS" in
    alpine)
      apk update -q >/dev/null 2>&1 || true
      apk add --no-cache "$@" || warn "apk reported an error"
      ;;
    debian)
      export DEBIAN_FRONTEND=noninteractive
      apt-get update -qq >/dev/null 2>&1 || true
      apt-get install -y -qq "$@" || warn "apt reported an error"
      ;;
  esac
}

create_system_user() {
  case "$OS" in
    alpine)
      adduser -S -D -H -s /sbin/nologin "$SERVICE_USER" >/dev/null 2>&1 || true
      ;;
    debian)
      useradd --system --no-create-home --shell /usr/sbin/nologin \
        "$SERVICE_USER" >/dev/null 2>&1 || true
      ;;
  esac
}

# Run a command as the service user, so anything it writes into the state
# directory is owned by the account that later has to read it.
run_as_service() {
  su -s /bin/sh "$SERVICE_USER" -c "$*"
}

service_stop() {
  case "$OS" in
    alpine) rc-service "$SERVICE_NAME" stop 2>/dev/null || true ;;
    debian) systemctl stop "$SERVICE_NAME" 2>/dev/null || true ;;
  esac
}

service_running() {
  case "$OS" in
    alpine) rc-service "$SERVICE_NAME" status >/dev/null 2>&1 ;;
    debian) systemctl is-active --quiet "$SERVICE_NAME" ;;
  esac
}

service_tail() {
  case "$OS" in
    alpine) tail -25 "$LOG_FILE" 2>/dev/null || true ;;
    debian) journalctl -u "$SERVICE_NAME" -n 25 --no-pager 2>/dev/null || true ;;
  esac
}

service_hints() {
  case "$OS" in
    alpine)
      step "logs:    tail -f $LOG_FILE"
      step "restart: rc-service $SERVICE_NAME restart"
      ;;
    debian)
      step "logs:    journalctl -u $SERVICE_NAME -f"
      step "restart: systemctl restart $SERVICE_NAME"
      ;;
  esac
}

# What the installed service was last told to trust.
#
# This script rewrites the service file on every run, so without reading the old
# one back an update would silently drop the setting — and dropping it is not
# visible: the gateway keeps serving, but every client behind the proxy shares
# one rate-limit bucket again and the access log stops telling them apart. An
# update should not undo a decision nobody is re-making.
existing_unit_env() {
  case "$OS" in
    alpine)
      sed -n "s/^export $1=\"\(.*\)\"$/\1/p" \
        "/etc/init.d/$SERVICE_NAME" 2>/dev/null | head -1
      ;;
    debian)
      sed -n "s/^Environment=$1=\(.*\)$/\1/p" \
        "/etc/systemd/system/$SERVICE_NAME.service" 2>/dev/null | head -1
      ;;
  esac
}

existing_trusted_proxies() {
  case "$OS" in
    alpine)
      sed -n 's/^export CLAUDICATION_TRUSTED_PROXIES="\(.*\)"$/\1/p' \
        "/etc/init.d/$SERVICE_NAME" 2>/dev/null | head -1
      ;;
    debian)
      sed -n 's/^Environment=CLAUDICATION_TRUSTED_PROXIES=\(.*\)$/\1/p' \
        "/etc/systemd/system/$SERVICE_NAME.service" 2>/dev/null | head -1
      ;;
  esac
}

# Settings an earlier install put in the service unit, carried into the config
# file the first time one is written.
#
# The unit is rewritten on every run, so without this an update silently
# reverts them — and neither reversion announces itself. Losing trusted-proxies
# puts every client behind the proxy back in one rate-limit bucket. Losing a
# narrowed listen is worse: an install deliberately bound to 127.0.0.1 would
# come back on 0.0.0.0, publishing a gateway that was meant to be local.
carry_forward_unit_settings() {
  if [ -z "$TRUSTED_PROXIES_GIVEN" ]; then
    TRUSTED_PROXIES="$(existing_trusted_proxies)"
    [ -n "$TRUSTED_PROXIES" ] && step "keeping trusted proxy: $TRUSTED_PROXIES"
  fi
  [ -n "$TRUSTED_PROXIES" ] && info "Trusting proxy: $TRUSTED_PROXIES"

  if [ -z "$LISTEN_GIVEN" ] && [ ! -f "$CONFIG_FILE" ]; then
    previous="$(existing_unit_env CLAUDICATION_LISTEN)"
    if [ -n "$previous" ] && [ "$previous" != "$LISTEN" ]; then
      LISTEN="$previous"
      step "keeping listen: $LISTEN"
    fi
  fi
  return 0
}


service_install() {
  carry_forward_unit_settings
  # Before the unit, which points at it.
  write_config_file
  case "$OS" in
    alpine) write_openrc_service ;;
    debian) write_systemd_service ;;
  esac
}

# Nothing else rotates this file.
#
# systemd hands its unit's output to the journal, which has its own limits; the
# OpenRC path writes to a plain file that grows for as long as the service runs.
# The gateway logs a line per proxied request, so an unattended box eventually
# fills its disk — and the state database is on the same one.
write_logrotate() {
  [ -d /etc/logrotate.d ] || return 0
  # Weekly, or at 32 MB, whichever comes first. The gateway writes about 470
  # bytes per proxied request across two lines, so a busy week is tens of
  # megabytes and a very busy one is hundreds — on a small VM that is the whole
  # disk, days before the weekly rotation would have caught it.
  #
  # maxsize rather than size: `size` replaces the time trigger outright, which
  # would leave a quiet gateway's log unrotated indefinitely. maxsize adds a
  # ceiling to the weekly schedule instead of replacing it, so the total is
  # bounded at roughly 8 x 32 MB regardless of traffic.
  cat > "/etc/logrotate.d/$SERVICE_NAME" <<EOF
$LOG_FILE {
  weekly
  maxsize 32M
  rotate 8
  compress
  delaycompress
  missingok
  notifempty
  copytruncate
  su root root
  create 640 $SERVICE_USER root
}
EOF
  # copytruncate rather than a restart: supervise-daemon holds the file open,
  # and rotating it out from under a running gateway would send every
  # subsequent line to a deleted inode.
  info "Log rotation configured (/etc/logrotate.d/$SERVICE_NAME)"
}

write_openrc_service() {
  info "Installing the OpenRC service"

  # start-stop-daemon opens the log after dropping privileges, and /var/log is
  # root-owned 0755 — so the file has to exist and be writable beforehand, or
  # the service dies before producing any output to explain why.
  #
  # Created only if absent: this script is also the update path, and `: >` would
  # throw away the log every time, taking with it whatever the operator was
  # about to read to find out why they were updating.
  [ -f "$LOG_FILE" ] || : > "$LOG_FILE"
  chown "$SERVICE_USER" "$LOG_FILE"
  chmod 640 "$LOG_FILE"
  write_logrotate

  cat > "/etc/init.d/$SERVICE_NAME" <<EOF
#!/sbin/openrc-run
name="$SERVICE_NAME"
description="claudication - Claude API gateway"
command="$BINARY"
command_args="serve -config $CONFIG_FILE"
command_user="$SERVICE_USER"
directory="$STATE_DIR"
pidfile="/run/\$RC_SVCNAME.pid"
output_log="$LOG_FILE"
error_log="$LOG_FILE"

# Supervised, so a crash is a five-second gap rather than an outage.
supervisor="supervise-daemon"
respawn_delay=5
respawn_max=10
respawn_period=1800

# Wait out the drain before calling it stopped, as TimeoutStopSec does for
# systemd: longer than the two-minute grace, so a restart does not start the
# new process while the old one still holds the ports and a stream is open.
retry="TERM/150/KILL/5"

# Only the state directory, which the CLI subcommands need before any config
# is read. Everything else lives in the config file, because an environment
# variable silently overrides it and half a configuration is worse than either.
export CLAUDICATION_STATE_DIR="$STATE_DIR"

depend() { need net; after firewall; }
EOF
  chmod +x "/etc/init.d/$SERVICE_NAME"
  rc-update add "$SERVICE_NAME" default >/dev/null 2>&1 || true
}

# The config file is the source of truth, and this writes it once.
#
# It used to be neither written nor read: the units carried
# CLAUDICATION_LISTEN and CLAUDICATION_TRUSTED_PROXIES, and nothing created
# /etc/claudication/config.yaml at all. Since env overrides the file, an
# operator who followed the README and wrote a config got half of it applied —
# admin-listen took effect because no unit set it, listen was silently
# overridden because one did. Half a configuration with no error is worse than
# either mechanism on its own.
#
# So: the units now pass -config and set nothing but the state directory, which
# the CLI subcommands need before any config is read. An existing file is never
# rewritten, because it is the operator's and it has their comments in it.
write_config_file() {
  if [ -f "$CONFIG_FILE" ]; then
    info "Keeping $CONFIG_FILE"
    return
  fi
  info "Writing $CONFIG_FILE"
  mkdir -p "$CONFIG_DIR"

  case "$OS" in
    alpine) restart_hint="rc-service $SERVICE_NAME restart" ;;
    *)      restart_hint="systemctl restart $SERVICE_NAME" ;;
  esac

  {
    echo "# claudication configuration."
    echo "#"
    echo "# Written once by the installer and never rewritten, so your comments"
    echo "# survive. Restart the service after editing:"
    echo "#     $restart_hint"
    echo "#"
    echo "# Every field is optional. The full annotated example is at"
    echo "# https://github.com/$GH_REPO/blob/main/configs/config.example.yaml"
    echo
    echo "# What the relay binds."
    echo "listen: \"$LISTEN\""
    echo
    if [ -n "$ADMIN_LISTEN" ]; then
      echo "# The admin API and UI, on their own address: the relay listener"
      echo "# answers 404 to /admin, to the UI, and to a ?token= sign-in link."
      echo "admin-listen: \"$ADMIN_LISTEN\""
    else
      echo "# Put the admin API and UI on their own address, leaving the line"
      echo "# above serving only the relay and /health. SET THIS IF THE GATEWAY"
      echo "# IS EXPOSED: the admin surface can add Claude accounts and mint API"
      echo "# keys, and shares the published listener until you do."
      echo "# admin-listen: \"127.0.0.1:8318\""
    fi
    echo
    echo "state-dir: \"$STATE_DIR\""
    echo
    if [ -n "$TRUSTED_PROXIES" ]; then
      echo "# CIDRs or IPs the proxy in front connects from."
      echo "trusted-proxies:"
      # shellcheck disable=SC2086
      echo "$TRUSTED_PROXIES" | tr ',' '\n' | while read -r cidr; do
        [ -n "$cidr" ] || continue
        echo "  - \"$(echo "$cidr" | tr -d ' ')\""
      done
    else
      echo "# SET THIS if anything fronts the gateway, or every client behind it"
      echo "# shares one rate-limit bucket and the session cookie cannot be"
      echo "# marked Secure. The value is the address the PROXY connects from."
      echo "# trusted-proxies:"
      echo "#   - \"10.0.0.0/8\""
    fi
  } > "$CONFIG_FILE"

  chmod 0644 "$CONFIG_FILE"
}

write_systemd_service() {
  info "Installing the systemd service"

  cat > "/etc/systemd/system/$SERVICE_NAME.service" <<EOF
[Unit]
Description=claudication - Claude API gateway
Documentation=https://github.com/$GH_REPO
After=network-online.target
Wants=network-online.target

[Service]
Type=exec
User=$SERVICE_USER
Group=$SERVICE_USER
ExecStart=$BINARY serve -config $CONFIG_FILE
Restart=on-failure
RestartSec=2s

# Only the state directory, which the CLI subcommands need before any config
# is read. Everything else lives in the config file, because an environment
# variable silently overrides it and half a configuration is worse than either.
Environment=CLAUDICATION_STATE_DIR=$STATE_DIR

# Responses are long-lived streams and the gateway drains them within its own
# grace period, so give systemd more patience than that grace.
KillSignal=SIGTERM
TimeoutStopSec=150s

NoNewPrivileges=yes
PrivateTmp=yes
PrivateDevices=yes
ProtectSystem=strict
ProtectHome=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
RestrictAddressFamilies=AF_INET AF_INET6
RestrictNamespaces=yes
LockPersonality=yes
MemoryDenyWriteExecute=yes
SystemCallArchitectures=native
SystemCallFilter=@system-service
ReadWritePaths=$STATE_DIR

[Install]
WantedBy=multi-user.target
EOF
  systemctl daemon-reload
  systemctl enable "$SERVICE_NAME" >/dev/null 2>&1
}

# A top-level scalar from the config file, or empty. Good enough for the flat
# `key: "value"` lines this script writes and the example documents; it is used
# only to find the ports, never to change anything.
config_value() {
  [ -f "$CONFIG_FILE" ] || return 0
  sed -n "s/^$1:[[:space:]]*\"\{0,1\}\([^\"#[:space:]]*\).*/\1/p" "$CONFIG_FILE" | head -1
}

# Every port the gateway binds: the relay, and the admin and docs listeners
# when the config splits them out. Waiting on the relay alone is how the
# 2026-09-24 upgrade went wrong — the relay port was free while the admin one
# was still held by the draining process.
gateway_ports() {
  for key in listen admin-listen docs-listen; do
    addr="$(config_value "$key")"
    [ "$key" = listen ] && [ -z "$addr" ] && addr="$LISTEN"
    [ -n "$addr" ] && printf '%s\n' "${addr##*:}"
  done
  return 0
}

relay_port() {
  addr="$(config_value listen)"
  addr="${addr:-$LISTEN}"
  printf '%s' "${addr##*:}"
}

# Whether anything is listening on a TCP port, read from the kernel rather than
# by connecting: a draining process answers nothing and still holds the port.
port_listening() {
  hex="$(printf '%04X' "$1")"
  cat /proc/net/tcp /proc/net/tcp6 2>/dev/null |
    awk -v p="$hex" 'NR > 1 { n = split($2, a, ":"); if (a[n] == p && $4 == "0A") f = 1 } END { exit !f }'
}

any_port_held() {
  for port in $(gateway_ports); do
    port_listening "$port" && return 0
  done
  return 1
}

# Wait for the old process to let go of every listener.
#
# It shuts down gracefully, which means it finishes what is in flight — and a
# streaming request legitimately runs for minutes. Until every port is free the
# new process cannot bind, and on Alpine supervise-daemon respawns it into that
# failure until respawn_max and then gives up for good.
#
# Bounded, so a wedged process delays an upgrade rather than blocking it for
# ever. Reaching the bound is worth saying out loud.
wait_released() {
  i=0
  while any_port_held; do
    i=$((i + 1))
    if [ "$i" -gt "$STOP_TIMEOUT" ]; then
      endline
      warn "a port is still held after ${STOP_TIMEOUT}s: $(gateway_ports | tr '\n' ' ')"
      return 1
    fi
    [ $((i % 2)) -eq 0 ] && tick
    sleep 1
  done
  [ "$i" -gt 0 ] && endline
  return 0
}

stop_and_release() {
  info "Stopping $SERVICE_NAME (in-flight requests finish first)"
  service_stop
  wait_released || true
}

service_start() {
  info "Starting $SERVICE_NAME"
  case "$OS" in
    alpine) rc-service "$SERVICE_NAME" start >/dev/null 2>&1 ;;
    debian) systemctl start "$SERVICE_NAME" ;;
  esac
}


# ═══ Steps ════════════════════════════════════════════════════════════════════

require_root() {
  [ "$(id -u)" -eq 0 ] || die "must be run as root"
  detect_platform
  info "System detected: $OS"
}

missing_tools() {
  out=""
  for tool in curl; do
    command -v "$tool" >/dev/null 2>&1 || out="$out $tool"
  done
  printf '%s' "$out"
}

ensure_tools() {
  missing="$(missing_tools)"
  [ -n "$missing" ] || return 0

  info "Installing:$missing"
  pkg_install ca-certificates $missing
  still="$(missing_tools)"
  [ -z "$still" ] || die "still missing after install:$still"
}

resolve_release_tag() {
  if [ -n "$VERSION" ]; then
    printf '%s' "$VERSION"
    return 0
  fi
  curl -fsSL "${GH_API}/repos/${GH_REPO}/releases/latest" |
    sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' |
    head -1
}

# Check a download against the release's published digest.
#
# This installs an executable that then runs as a service, so "it downloaded"
# is not the same as "it is what was built". A missing SHA256SUMS is not fatal
# — a release predating it should still install — but a mismatch is.
verify_download() {
  file="$1"; name="$2"; tag="$3"

  if ! command -v sha256sum >/dev/null 2>&1; then
    warn "sha256sum not available: cannot verify $name"
    return 0
  fi

  sums="$(curl -fsSL "${GH_DL}/${GH_REPO}/releases/download/${tag}/SHA256SUMS" 2>/dev/null)" || sums=""
  if [ -z "$sums" ]; then
    warn "no SHA256SUMS in $tag: cannot verify $name"
    return 0
  fi

  want="$(printf '%s\n' "$sums" | awk -v n="$name" '$2 == n || $2 == "*"n {print $1; exit}')"
  if [ -z "$want" ]; then
    warn "$name is not listed in SHA256SUMS: cannot verify it"
    return 0
  fi

  got="$(sha256sum "$file" | awk '{print $1}')"
  if [ "$got" != "$want" ]; then
    rm -f "$file"
    die "$name does not match its published checksum — refusing to install it"
  fi
  info "Checksum verified"
}

# The version a binary reports, or empty. Also the pre-flight check: a binary
# that cannot print its version will not serve either — wrong architecture, a
# truncated copy, a file that is not the gateway at all.
binary_version() {
  [ -x "$1" ] || return 0
  "$1" version 2>/dev/null | awk '{print $1; exit}'
}

installed_version() { binary_version "$BINARY"; }

# Put the new binary in a temporary file and prove it runs, touching nothing
# that is serving. Sets NEW_BIN, NEW_VERSION and BEFORE.
fetch_binary() {
  BEFORE="$(installed_version)"
  NEW_DIR="$(mktemp -d)"
  NEW_BIN="$NEW_DIR/claudication"

  if [ -n "$BINARY_FILE" ]; then
    [ -f "$BINARY_FILE" ] || die "BINARY_FILE=$BINARY_FILE does not exist"
    cp "$BINARY_FILE" "$NEW_BIN"
    if [ -n "$BINARY_SHA256" ]; then
      got="$(sha256sum "$NEW_BIN" | awk '{print $1}')"
      [ "$got" = "$BINARY_SHA256" ] || die "$BINARY_FILE does not match BINARY_SHA256 — refusing to install it"
      info "Checksum verified"
    fi
    label="$BINARY_FILE"
  else
    tag="$(resolve_release_tag)"
    [ -n "$tag" ] || die "could not resolve the latest release of $GH_REPO"
    step "$(uname -m) -> $ASSET"
    url="${GH_DL}/${GH_REPO}/releases/download/${tag}/${ASSET}"
    curl -fsSL "$url" -o "$NEW_BIN" || { rm -rf "$NEW_DIR"; die "download failed: $url"; }
    verify_download "$NEW_BIN" "$ASSET" "$tag"
    label="$tag"
  fi

  chmod 755 "$NEW_BIN"
  NEW_VERSION="$(binary_version "$NEW_BIN")"
  if [ -z "$NEW_VERSION" ]; then
    rm -rf "$NEW_DIR"
    die "the new binary ($label) does not run on this machine — nothing was changed"
  fi

  if [ -n "$BEFORE" ]; then
    info "Updating $BEFORE -> $NEW_VERSION"
  else
    info "Installing $NEW_VERSION"
  fi
}

# Snapshot the state before a new version can migrate it.
#
# Taken by the binary that is still installed, with the service still serving:
# `backup` is a consistent VACUUM INTO, safe under load. A migration that goes
# wrong is otherwise unrecoverable, because the old binary may not open a
# database the new one has already changed.
backup_state() {
  [ -n "$BEFORE" ] || return 0
  [ -f "$STATE_DIR/claudication.db" ] || return 0
  if [ "$BACKUPS_KEPT" = 0 ]; then
    warn "BACKUPS_KEPT=0: no snapshot before updating"
    return 0
  fi

  dir="$STATE_DIR/backups"
  mkdir -p "$dir"
  chown "$SERVICE_USER" "$dir"
  chmod 700 "$dir"
  BACKUP_FILE="$dir/pre-$NEW_VERSION-$(date -u +%Y%m%d-%H%M%S).tar.gz"
  if ! run_as_service "CLAUDICATION_STATE_DIR='$STATE_DIR' '$BINARY' backup -out '$BACKUP_FILE'" >/dev/null 2>&1; then
    rm -rf "$NEW_DIR"
    die "could not snapshot $STATE_DIR before updating — nothing was changed (BACKUPS_KEPT=0 skips this)"
  fi
  info "State backed up: $BACKUP_FILE"

  # Newest first; everything past the limit goes. The names are ours and carry
  # no whitespace, so ls is safe here.
  ls -1t "$dir"/pre-*.tar.gz 2>/dev/null | tail -n +$((BACKUPS_KEPT + 1)) | while read -r old; do
    rm -f "$old"
  done
}

# Put the checked binary in place, keeping the one it replaces beside it.
# Only called with the service stopped: replacing the file under a running
# service leaves it executing a binary that is no longer the one on disk.
swap_binary() {
  mkdir -p "$BIN_DIR"
  if [ -x "$BINARY" ]; then cp -p "$BINARY" "$BINARY.prev"; fi
  mv "$NEW_BIN" "$BINARY"
  rm -rf "$NEW_DIR"
}

# The update did not come up: put the previous binary back and start that.
rollback() {
  [ -n "$BEFORE" ] && [ -x "$BINARY.prev" ] || return 1
  warn "rolling back to $BEFORE"
  stop_and_release
  mv "$BINARY.prev" "$BINARY"
  NEW_VERSION="$BEFORE"
  service_start
  if wait_ready; then
    warn "$BEFORE is serving again; the failed version's log lines are above"
    return 0
  fi
  fail "$BEFORE did not come back either"
  if [ -n "${BACKUP_FILE:-}" ]; then
    step "the new version may have migrated the database; the state before it is in"
    step "  $BACKUP_FILE"
    step "restore with the service stopped: claudication restore -force -in $BACKUP_FILE"
  fi
  return 1
}

ensure_user() {
  id "$SERVICE_USER" >/dev/null 2>&1 && return 0
  info "Creating user $SERVICE_USER"
  create_system_user
}

ensure_dirs() {
  mkdir -p "$STATE_DIR"
  chown -R "$SERVICE_USER" "$STATE_DIR"
  # It holds sealed credentials, so it is the service user's and nobody else's.
  chmod 700 "$STATE_DIR"
}

# A fresh install has no admin account, and the first person to reach the port
# claims it. On a headless box nobody is watching that window, so close it here
# and hand the password back once.
#
# Runs on updates too, and leaves an existing account alone: login-url succeeds
# only when there is already an admin to mint a link for.
claim_gateway() {
  [ "$SET_PASSWORD" = "1" ] || return 0

  if run_as_service "CLAUDICATION_STATE_DIR='$STATE_DIR' '$BINARY' login-url" >/dev/null 2>&1; then
    return 0
  fi

  ADMIN_PASSWORD="$(head -c 18 /dev/urandom | od -An -tx1 | tr -d ' \n')"
  if printf '%s\n' "$ADMIN_PASSWORD" |
       run_as_service "CLAUDICATION_STATE_DIR='$STATE_DIR' '$BINARY' passwd" >/dev/null 2>&1; then
    info "Admin password set"
  else
    ADMIN_PASSWORD=""
    warn "could not set an admin password — claim the gateway yourself before anyone else does"
  fi
}

# What /health says the serving process is, or empty.
serving_version() {
  curl -fsS -m 2 "http://127.0.0.1:$(relay_port)/health" 2>/dev/null |
    sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p'
}

# Ready means the NEW version answers on the relay and every configured port is
# bound. Answering alone is not enough: during an upgrade the old process can
# still be draining, and a health check it answers proves nothing about the
# binary just installed.
ready() {
  [ "$(serving_version)" = "$NEW_VERSION" ] || return 1
  for port in $(gateway_ports); do
    port_listening "$port" || return 1
  done
  return 0
}

wait_ready() {
  i=0
  while [ "$i" -lt "$BOOT_TIMEOUT" ]; do
    if ready; then
      endline
      return 0
    fi
    # Only trust a "not running" reading once it has had a moment to start.
    if [ "$i" -gt 3 ] && ! service_running; then
      endline
      fail "the service stopped unexpectedly"
      dump_logs
      return 1
    fi
    i=$((i + 1))
    [ $((i % 2)) -eq 0 ] && tick
    sleep 1
  done

  endline
  got="$(serving_version)"
  fail "$NEW_VERSION not serving on every port after ${BOOT_TIMEOUT}s${got:+ (health reports $got)}"
  dump_logs
  return 1
}

print_summary() {
  ip="$(hostname -I 2>/dev/null | awk '{print $1}')"
  # The admin UI is on admin-listen when the config splits it out; pointing at
  # the relay port there hands the operator a 404.
  admin="$(config_value admin-listen)"
  port="$(relay_port)"
  [ -n "$admin" ] && port="${admin##*:}"

  printf '\n'
  info "claudication $(installed_version) is running"
  step "admin UI: http://${ip:-127.0.0.1}:${port}/"
  service_hints

  if [ -n "${ADMIN_PASSWORD:-}" ]; then
    printf '\n'
    printf "  ${BOLD}admin password: %s${NC}\n" "$ADMIN_PASSWORD"
    step "shown once — nothing can recover it, but 'claudication passwd' sets a new one"
  fi

  printf '\n'
  step "next: sign in, connect a Claude account, then mint an API key"
  warn "served over plain HTTP: put it behind a tunnel or a TLS proxy before exposing it"
}


# ═══ Run ══════════════════════════════════════════════════════════════════════

main() {
  require_root
  [ -n "$BINARY_FILE" ] || detect_arch
  ensure_tools

  # Nothing that is serving is touched until the new binary has run here and
  # the state has been snapshotted.
  fetch_binary
  ensure_user
  ensure_dirs
  backup_state

  # The unit first, so the stop below already runs under its patience.
  service_install
  stop_and_release
  swap_binary
  claim_gateway
  service_start

  ready=0
  if wait_ready; then
    ready=1
    print_summary
  else
    rollback || true
  fi

  # A non-zero exit matters when this is piped into a provisioning tool, and a
  # rolled-back update is a failed one.
  [ "$ready" = "1" ]
}

main "$@"
