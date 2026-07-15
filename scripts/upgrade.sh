#!/usr/bin/env bash
set -Eeuo pipefail

REPO="${YESNAS_REPO:-i-dj/yesnas-server}"
VERSION="${YESNAS_VERSION:-latest}"
INSTALL_DIR="${YESNAS_INSTALL_DIR:-/opt/yesnas}"
SERVICE_NAME="${YESNAS_SERVICE_NAME:-yesnas}"

STEP=0
TOTAL_STEPS=7

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

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64) echo "amd64" ;;
    aarch64|arm64) echo "arm64" ;;
    *) fail "Unsupported architecture: $(uname -m). Only amd64 and arm64 are supported." ;;
  esac
}

release_url() {
  local asset="$1"
  if [[ "${VERSION}" == "latest" ]]; then
    echo "https://github.com/${REPO}/releases/latest/download/${asset}"
  else
    echo "https://github.com/${REPO}/releases/download/${VERSION}/${asset}"
  fi
}

main() {
  step "Check system environment"
  require_command uname
  require_command curl
  require_command tar
  require_command awk
  require_command sha256sum
  if [[ "${EUID}" -ne 0 ]]; then
    require_command sudo
    sudo -v
  fi
  if [[ ! -d "${INSTALL_DIR}" ]]; then
    fail "Install dir not found: ${INSTALL_DIR}. Please install YesNAS first."
  fi

  step "Read current service user"
  local service_file="/etc/systemd/system/${SERVICE_NAME}.service"
  local install_user="root"
  local install_group="root"
  if [[ -f "${service_file}" ]]; then
    install_user="$(awk -F= '/^User=/{print $2; exit}' "${service_file}")"
    install_group="$(awk -F= '/^Group=/{print $2; exit}' "${service_file}")"
  fi
  if [[ -z "${install_user}" ]]; then
    install_user="root"
  fi
  if [[ -z "${install_group}" ]]; then
    install_group="$(id -gn "${install_user}" 2>/dev/null || echo root)"
  fi
  log "Service user: ${install_user}:${install_group}"

  step "Download YesNAS release"
  local arch
  local asset
  local asset_sha
  local tmp_dir
  local archive
  arch="$(detect_arch)"
  asset="yesnas-server-linux-${arch}.tar.gz"
  asset_sha="${asset}.sha256"
  tmp_dir="$(mktemp -d)"
  archive="${tmp_dir}/${asset}"
  log "Repository: ${REPO}"
  log "Version: ${VERSION}"
  log "Asset: ${asset}"
  curl -fL --retry 3 --retry-delay 2 -o "${archive}" "$(release_url "${asset}")"
  if curl -fL --retry 3 --retry-delay 2 -o "${archive}.sha256" "$(release_url "${asset_sha}")"; then
    local expected_sha
    expected_sha="$(awk '{print $1}' "${archive}.sha256" | head -n 1)"
    if [[ -z "${expected_sha}" ]]; then
      fail "Downloaded SHA256 file is empty."
    fi
    printf '%s  %s\n' "${expected_sha}" "${archive}" | sha256sum -c -
  else
    warn "SHA256 file not found; skipping checksum verification."
  fi

  step "Stop YesNAS service"
  if systemctl list-unit-files "${SERVICE_NAME}.service" >/dev/null 2>&1; then
    run_root systemctl stop "${SERVICE_NAME}" || true
  fi

  step "Backup current binary"
  local backup_dir="${INSTALL_DIR}/backup"
  local backup_name="yesnas-server.$(date -u +%Y%m%d%H%M%S).bak"
  run_root mkdir -p "${backup_dir}"
  if [[ -f "${INSTALL_DIR}/yesnas-server" ]]; then
    run_root cp "${INSTALL_DIR}/yesnas-server" "${backup_dir}/${backup_name}"
    log "Backup: ${backup_dir}/${backup_name}"
  else
    warn "Current binary not found; skipping backup."
  fi

  step "Install upgraded binary files"
  tar -xzf "${archive}" -C "${tmp_dir}"
  local extracted_dir="${tmp_dir}/yesnas-server-linux-${arch}"
  if [[ ! -x "${extracted_dir}/yesnas-server" ]]; then
    fail "Release archive does not contain executable yesnas-server."
  fi
  run_root install -m 0755 -o "${install_user}" -g "${install_group}" "${extracted_dir}/yesnas-server" "${INSTALL_DIR}/yesnas-server"
  if [[ -f "${extracted_dir}/VERSION" ]]; then
    run_root install -m 0644 -o "${install_user}" -g "${install_group}" "${extracted_dir}/VERSION" "${INSTALL_DIR}/VERSION"
  fi
  if [[ -f "${extracted_dir}/README.md" ]]; then
    run_root install -m 0644 -o "${install_user}" -g "${install_group}" "${extracted_dir}/README.md" "${INSTALL_DIR}/README.md"
  fi
  if [[ -d "${extracted_dir}/database" ]]; then
    run_root mkdir -p "${INSTALL_DIR}/database"
    for sql_file in "${extracted_dir}"/database/*.sql; do
      [[ -f "${sql_file}" ]] || continue
      run_root install -m 0644 -o "${install_user}" -g "${install_group}" "${sql_file}" "${INSTALL_DIR}/database/$(basename "${sql_file}")"
    done
  fi
  rm -rf "${tmp_dir}"

  step "Restart YesNAS service"
  run_root systemctl daemon-reload
  run_root systemctl restart "${SERVICE_NAME}"
  sleep 2
  if ! run_root systemctl is-active --quiet "${SERVICE_NAME}"; then
    run_root systemctl status "${SERVICE_NAME}" --no-pager || true
    fail "YesNAS service failed to start after upgrade. Backup binary is in ${backup_dir}."
  fi
  log "Upgrade completed."
  log "Service status: systemctl status ${SERVICE_NAME}"
  log "Service logs: journalctl -u ${SERVICE_NAME} -f"
}

main "$@"
