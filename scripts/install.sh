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
LISTEN="${LISTEN:-0.0.0.0:8317}"      # what the service binds
BOOT_TIMEOUT="${BOOT_TIMEOUT:-30}"    # seconds to wait for the first response
SET_PASSWORD="${SET_PASSWORD:-1}"     # 0 to leave the gateway unclaimed
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

service_install() {
  case "$OS" in
    alpine) write_openrc_service ;;
    debian) write_systemd_service ;;
  esac
}

write_openrc_service() {
  info "Installing the OpenRC service"

  # start-stop-daemon opens the log after dropping privileges, and /var/log is
  # root-owned 0755 — so the file has to exist and be writable beforehand, or
  # the service dies before producing any output to explain why.
  : > "$LOG_FILE"
  chown "$SERVICE_USER" "$LOG_FILE"
  chmod 640 "$LOG_FILE"

  cat > "/etc/init.d/$SERVICE_NAME" <<EOF
#!/sbin/openrc-run
name="$SERVICE_NAME"
description="claudication - Claude API gateway"
command="$BINARY"
command_args="serve"
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

export CLAUDICATION_STATE_DIR="$STATE_DIR"
export CLAUDICATION_LISTEN="$LISTEN"

depend() { need net; after firewall; }
EOF
  chmod +x "/etc/init.d/$SERVICE_NAME"
  rc-update add "$SERVICE_NAME" default >/dev/null 2>&1 || true
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
ExecStart=$BINARY serve
Restart=on-failure
RestartSec=2s

Environment=CLAUDICATION_STATE_DIR=$STATE_DIR
Environment=CLAUDICATION_LISTEN=$LISTEN

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

service_start() {
  info "Starting $SERVICE_NAME"
  case "$OS" in
    alpine) rc-service "$SERVICE_NAME" restart >/dev/null 2>&1 ||
            rc-service "$SERVICE_NAME" start >/dev/null 2>&1 ;;
    debian) systemctl restart "$SERVICE_NAME" ;;
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

# The version already installed, or empty. Only used to say what changed.
installed_version() {
  [ -x "$BINARY" ] || return 0
  "$BINARY" version 2>/dev/null | awk '{print $1; exit}'
}

install_binary() {
  before="$(installed_version)"

  tag="$(resolve_release_tag)"
  [ -n "$tag" ] || die "could not resolve the latest release of $GH_REPO"

  if [ -n "$before" ]; then
    info "Updating $before -> $tag"
  else
    info "Installing $tag"
  fi
  step "$(uname -m) -> $ASSET"

  tmp="$(mktemp -d)"
  url="${GH_DL}/${GH_REPO}/releases/download/${tag}/${ASSET}"
  curl -fsSL "$url" -o "$tmp/claudication" || { rm -rf "$tmp"; die "download failed: $url"; }
  verify_download "$tmp/claudication" "$ASSET" "$tag"

  # Stop first: replacing the file under a running service leaves it executing
  # a binary that is no longer the one on disk.
  service_stop

  mkdir -p "$BIN_DIR"
  chmod +x "$tmp/claudication"
  mv "$tmp/claudication" "$BINARY"
  rm -rf "$tmp"
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

responding() {
  curl -fsS -m 2 "http://127.0.0.1:${LISTEN##*:}/health" >/dev/null 2>&1
}

wait_ready() {
  i=0
  while [ "$i" -lt "$BOOT_TIMEOUT" ]; do
    if responding; then
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
  fail "no response on ${LISTEN} after ${BOOT_TIMEOUT}s"
  dump_logs
  return 1
}

print_summary() {
  ip="$(hostname -I 2>/dev/null | awk '{print $1}')"
  port="${LISTEN##*:}"

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
  detect_arch
  ensure_tools

  install_binary
  ensure_user
  ensure_dirs
  claim_gateway

  service_install
  service_start

  if wait_ready; then ready=1; else ready=0; fi
  print_summary

  # A non-zero exit matters when this is piped into a provisioning tool.
  [ "$ready" = "1" ]
}

main "$@"
