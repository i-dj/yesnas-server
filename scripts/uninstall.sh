#!/usr/bin/env bash
set -Eeuo pipefail

INSTALL_DIR="${YESNAS_INSTALL_DIR:-/opt/yesnas/server}"
CONFIG_DIR="${YESNAS_CONFIG_DIR:-/etc/yesnas-server}"
DATA_ROOT="${YESNAS_DATA_ROOT:-/srv/yesnas}"
SERVICE_NAME="${YESNAS_SERVICE_NAME:-yesnas-server}"

STEP=0
TOTAL_STEPS=9

DEPENDENCY_PACKAGES=(
  btrfs-progs
  smartmontools
  mdadm
  acl
  samba
  nfs-kernel-server
  proftpd-basic
  apache2
  apache2-utils
  avahi-daemon
  rclone
  fuse3
  ffmpeg
  vnstat
  dmidecode
)

log() {
  printf '\033[1;32m[YesNAS]\033[0m %s\n' "$*"
}

warn() {
  printf '\033[1;33m[YesNAS][WARN]\033[0m %s\n' "$*" >&2
}

fail() {
  printf '\033[1;31m[YesNAS][ERROR]\033[0m %s\n' "$*" >&2
  exit 1
}

step() {
  STEP=$((STEP + 1))
  printf '\n\033[1;34m[%02d/%02d]\033[0m %s\n' "$STEP" "$TOTAL_STEPS" "$*"
}

run_root() {
  if [[ "${EUID}" -eq 0 ]]; then
    "$@"
  else
    sudo "$@"
  fi
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "Missing command: $1"
}

confirm() {
  local prompt="$1"
  local expected="$2"
  local value=""
  if [[ -r /dev/tty ]]; then
    read -r -p "${prompt} " value </dev/tty || true
  else
    warn "No interactive terminal detected; confirmation failed."
    return 1
  fi
  [[ "${value}" == "${expected}" ]]
}

remove_line_once() {
  local file="$1"
  local pattern="$2"
  if [[ -f "${file}" ]]; then
    run_root sed -i "\|${pattern}|d" "${file}"
  fi
}

main() {
  step "Check system environment"
  require_command sed
  if [[ "${EUID}" -ne 0 ]]; then
    require_command sudo
    sudo -v
  fi
  if [[ "${YESNAS_NONINTERACTIVE:-0}" != "1" ]]; then
    if ! confirm "This will uninstall YesNAS and remove its program/config/data files. Type YESNAS to continue:" "YESNAS"; then
      fail "Uninstall cancelled."
    fi
  else
    log "Non-interactive uninstall enabled."
  fi

  step "Stop and disable YesNAS service"
  if systemctl list-unit-files "${SERVICE_NAME}.service" >/dev/null 2>&1; then
    run_root systemctl stop "${SERVICE_NAME}" || true
    run_root systemctl disable "${SERVICE_NAME}" >/dev/null 2>&1 || true
  fi

  step "Remove systemd service"
  run_root rm -f "/etc/systemd/system/${SERVICE_NAME}.service"
  run_root systemctl daemon-reload
  run_root systemctl reset-failed >/dev/null 2>&1 || true

  step "Remove YesNAS sudoers allowlist"
  run_root rm -f /etc/sudoers.d/yesnas

  step "Remove SMB / FTP / WebDAV / NFS base config files"
  remove_line_once /etc/samba/smb.conf "include = /etc/samba/yesnas-shares.conf"
  run_root rm -f /etc/samba/yesnas-shares.conf
  run_root rm -f /etc/exports.d/yesnas.exports
  run_root rm -f /etc/proftpd/conf.d/yesnas.conf
  run_root rm -f /etc/apache2/sites-available/yesnas-webdav.conf
  remove_line_once /etc/apache2/ports.conf "Listen 8088"
  if command -v a2dismod >/dev/null 2>&1; then
    run_root a2dismod dav dav_fs auth_basic authn_file headers >/dev/null 2>&1 || true
  fi
  run_root exportfs -ra >/dev/null 2>&1 || true

  step "Remove YesNAS program and config directories"
  run_root rm -rf "${INSTALL_DIR}" "${CONFIG_DIR}"

  step "Remove YesNAS data directory"
  warn "Data root will be removed: ${DATA_ROOT}"
  if [[ "${YESNAS_NONINTERACTIVE:-0}" == "1" && "${YESNAS_REMOVE_DATA:-0}" == "1" ]]; then
    run_root rm -rf "${DATA_ROOT}"
  elif [[ "${YESNAS_NONINTERACTIVE:-0}" == "1" ]]; then
    warn "Skipped data directory removal: ${DATA_ROOT}"
  elif confirm "Type DELETE-DATA to remove ${DATA_ROOT}:" "DELETE-DATA"; then
    run_root rm -rf "${DATA_ROOT}"
  else
    warn "Skipped data directory removal: ${DATA_ROOT}"
  fi

  step "Optionally uninstall system dependencies"
  warn "This can remove SMB / FTP / WebDAV / NFS / rclone packages that may be used by other services."
  if [[ "${YESNAS_NONINTERACTIVE:-0}" == "1" && "${YESNAS_REMOVE_DEPS:-0}" == "1" ]]; then
    if command -v apt-get >/dev/null 2>&1; then
      export DEBIAN_FRONTEND=noninteractive
      run_root apt-get purge -y "${DEPENDENCY_PACKAGES[@]}" || true
      run_root apt-get autoremove -y || true
    else
      warn "apt-get not found; skipping dependency removal."
    fi
  elif [[ "${YESNAS_NONINTERACTIVE:-0}" == "1" ]]; then
    warn "Skipped dependency package removal."
  elif confirm "Type REMOVE-DEPS to purge YesNAS dependency packages:" "REMOVE-DEPS"; then
    if command -v apt-get >/dev/null 2>&1; then
      export DEBIAN_FRONTEND=noninteractive
      run_root apt-get purge -y "${DEPENDENCY_PACKAGES[@]}" || true
      run_root apt-get autoremove -y || true
    else
      warn "apt-get not found; skipping dependency removal."
    fi
  else
    warn "Skipped dependency package removal."
  fi

  step "Uninstall completed"
  log "Removed service: ${SERVICE_NAME}"
  log "Removed install dir: ${INSTALL_DIR}"
  log "Removed config dir: ${CONFIG_DIR}"
  log "If you skipped dependency removal, packages such as Samba/NFS/Apache remain installed."
}

main "$@"
