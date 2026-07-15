#!/usr/bin/env bash
set -Eeuo pipefail

REPO="${YESNAS_REPO:-i-dj/yesnas-server}"
VERSION="${YESNAS_VERSION:-latest}"
INSTALL_DIR="${YESNAS_INSTALL_DIR:-/opt/yesnas}"
CONFIG_DIR="${YESNAS_CONFIG_DIR:-/etc/yesnas}"
DATA_ROOT="${YESNAS_DATA_ROOT:-/srv/yesnas}"
SERVICE_NAME="${YESNAS_SERVICE_NAME:-yesnas}"
DEFAULT_PORT="${YESNAS_PORT:-8080}"

STEP=0
TOTAL_STEPS=13

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

prompt_value() {
  local prompt="$1"
  local default="$2"
  local value=""
  read -r -p "${prompt} [${default}]: " value || true
  if [[ -z "${value// }" ]]; then
    echo "${default}"
  else
    echo "${value}"
  fi
}

append_once() {
  local file="$1"
  local line="$2"
  run_root touch "$file"
  if ! grep -Fqx "$line" "$file"; then
    printf '%s\n' "$line" | run_root tee -a "$file" >/dev/null
  fi
}

main() {
  step "Check system environment"
  require_command uname
  require_command grep
  require_command tar
  require_command sed
  require_command awk
  if ! command -v apt-get >/dev/null 2>&1; then
    fail "This installer currently supports Debian/Ubuntu systems with apt-get."
  fi
  if [[ "${EUID}" -ne 0 ]]; then
    require_command sudo
    sudo -v
  fi

  local detected_user="${SUDO_USER:-$(id -un)}"
  if [[ "${detected_user}" == "root" ]]; then
    detected_user="dj"
  fi
  local install_user
  local host_name
  install_user="$(prompt_value "Enter the Linux user that will run YesNAS" "${YESNAS_USER:-${detected_user}}")"
  host_name="$(prompt_value "Enter hostname" "${YESNAS_HOSTNAME:-YesNAS}")"

  if ! id "${install_user}" >/dev/null 2>&1; then
    fail "User '${install_user}' does not exist. Please create it first, or rerun with YESNAS_USER=<existing-user>."
  fi
  local install_group
  install_group="$(id -gn "${install_user}")"

  step "Update apt package index"
  export DEBIAN_FRONTEND=noninteractive
  run_root apt-get update

  step "Install system dependencies"
  local packages=(
    ca-certificates
    curl
    tar
    gzip
    sudo
    coreutils
    util-linux
    mount
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
  run_root apt-get install -y "${packages[@]}"

  step "Set hostname and create directories"
  if command -v hostnamectl >/dev/null 2>&1; then
    run_root hostnamectl set-hostname "${host_name}" || warn "Failed to set hostname with hostnamectl."
  else
    printf '%s\n' "${host_name}" | run_root tee /etc/hostname >/dev/null
  fi
  run_root mkdir -p \
    "${INSTALL_DIR}" \
    "${CONFIG_DIR}" \
    "${DATA_ROOT}" \
    "${DATA_ROOT}/cloud" \
    "${DATA_ROOT}/cache/uploads" \
    "${DATA_ROOT}/webdav" \
    "${INSTALL_DIR}/data" \
    "${INSTALL_DIR}/oauth" \
    "${INSTALL_DIR}/logs"
  run_root chown -R "${install_user}:${install_group}" "${INSTALL_DIR}" "${DATA_ROOT}"

  step "Configure sudo NOPASSWD allowlist"
  local sudoers_file="/etc/sudoers.d/yesnas"
  local sudoers_tmp
  sudoers_tmp="$(mktemp)"
  cat >"${sudoers_tmp}" <<EOF
${install_user} ALL=(root) NOPASSWD: /usr/bin/lsblk, /usr/sbin/smartctl, /sbin/smartctl, /usr/sbin/mdadm, /sbin/mdadm, /usr/sbin/wipefs, /sbin/wipefs, /usr/sbin/blkid, /sbin/blkid, /usr/bin/mount, /bin/mount, /usr/bin/umount, /bin/umount, /usr/bin/tee, /bin/tee, /usr/sbin/mkfs.btrfs, /sbin/mkfs.btrfs, /usr/bin/mkfs.btrfs, /usr/bin/btrfs, /bin/btrfs, /usr/bin/mkdir, /bin/mkdir, /usr/bin/rm, /bin/rm, /usr/bin/dd, /bin/dd, /usr/bin/sync, /bin/sync, /usr/bin/cp, /bin/cp, /usr/bin/chmod, /bin/chmod, /usr/bin/chown, /bin/chown, /usr/bin/touch, /bin/touch, /usr/bin/mv, /bin/mv, /usr/bin/testparm, /usr/sbin/testparm, /usr/bin/smbpasswd, /usr/sbin/smbpasswd, /usr/bin/systemctl, /bin/systemctl, /usr/bin/setfacl, /usr/bin/getfacl, /usr/sbin/exportfs, /usr/sbin/showmount, /usr/sbin/proftpd, /usr/sbin/apache2ctl, /usr/bin/htpasswd, /usr/bin/fusermount, /bin/fusermount, /usr/bin/fusermount3, /bin/fusermount3, /usr/bin/id, /bin/id, /usr/sbin/dmidecode, /usr/bin/rclone, /usr/local/bin/rclone, /usr/bin/vnstat, /usr/local/bin/vnstat
EOF
  run_root install -m 0440 -o root -g root "${sudoers_tmp}" "${sudoers_file}"
  rm -f "${sudoers_tmp}"
  run_root visudo -cf "${sudoers_file}" >/dev/null

  step "Create SMB / FTP / WebDAV / NFS base config files"
  run_root touch /etc/samba/yesnas-shares.conf
  run_root chmod 0644 /etc/samba/yesnas-shares.conf
  if ! grep -Fq "include = /etc/samba/yesnas-shares.conf" /etc/samba/smb.conf; then
    printf '\n# YesNAS managed shares\ninclude = /etc/samba/yesnas-shares.conf\n' | run_root tee -a /etc/samba/smb.conf >/dev/null
  fi

  run_root mkdir -p /etc/exports.d
  run_root touch /etc/exports.d/yesnas.exports
  run_root chmod 0644 /etc/exports.d/yesnas.exports

  run_root mkdir -p /etc/proftpd/conf.d
  cat <<'EOF' | run_root tee /etc/proftpd/conf.d/yesnas.conf >/dev/null
# YesNAS FTP base configuration.
# Share-specific rules can be managed separately.
DefaultRoot ~
RequireValidShell off
EOF

  run_root a2enmod dav dav_fs auth_basic authn_file headers >/dev/null || true
  append_once /etc/apache2/ports.conf "Listen 8088"
  cat <<EOF | run_root tee /etc/apache2/sites-available/yesnas-webdav.conf >/dev/null
<VirtualHost *:8088>
    ServerName ${host_name}
    DocumentRoot ${DATA_ROOT}/webdav

    <Directory ${DATA_ROOT}/webdav>
        Options Indexes FollowSymLinks
        AllowOverride None
        Require all denied
    </Directory>

    Alias /webdav ${DATA_ROOT}/webdav
    <Location /webdav>
        DAV On
        Require all denied
    </Location>
</VirtualHost>
EOF
  warn "WebDAV sample config created at /etc/apache2/sites-available/yesnas-webdav.conf but not opened to the network by default."

  step "Enable and restart dependency services"
  local services=(smbd nmbd nfs-server nfs-kernel-server proftpd apache2 avahi-daemon vnstat)
  for service in "${services[@]}"; do
    if systemctl list-unit-files "${service}.service" >/dev/null 2>&1; then
      run_root systemctl enable "${service}" >/dev/null 2>&1 || true
      run_root systemctl restart "${service}" >/dev/null 2>&1 || warn "Failed to restart ${service}."
    fi
  done
  run_root exportfs -ra || true

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
    (cd "${tmp_dir}" && sha256sum -c "${asset_sha}")
  else
    warn "SHA256 file not found; skipping checksum verification."
  fi

  step "Install YesNAS binary files"
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
  rm -rf "${tmp_dir}"

  step "Create YesNAS environment config"
  cat <<EOF | run_root tee "${CONFIG_DIR}/yesnas.env" >/dev/null
ADDR=:${DEFAULT_PORT}
GEOLITE2_CITY_DB=${INSTALL_DIR}/data/GeoLite2-City.mmdb
YESNAS_UPLOAD_METADIR=${INSTALL_DIR}/data/uploads
YESNAS_CLOUD_UPLOAD_TMPDIR=${DATA_ROOT}/cache/uploads
OAUTH_BROKER_BASE_URL=https://oauth.yesnas.com
OAUTH_BROKER_DEVICE_NAME=${host_name}
EOF
  run_root chmod 0640 "${CONFIG_DIR}/yesnas.env"
  run_root chown "root:${install_group}" "${CONFIG_DIR}/yesnas.env"

  step "Create systemd service"
  cat <<EOF | run_root tee "/etc/systemd/system/${SERVICE_NAME}.service" >/dev/null
[Unit]
Description=YesNAS Server
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=${install_user}
Group=${install_group}
WorkingDirectory=${INSTALL_DIR}
EnvironmentFile=${CONFIG_DIR}/yesnas.env
ExecStart=${INSTALL_DIR}/yesnas-server
Restart=always
RestartSec=3
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
EOF
  run_root systemctl daemon-reload
  run_root systemctl enable "${SERVICE_NAME}" >/dev/null

  step "Start YesNAS service"
  run_root systemctl restart "${SERVICE_NAME}"
  sleep 2
  if ! run_root systemctl is-active --quiet "${SERVICE_NAME}"; then
    run_root systemctl status "${SERVICE_NAME}" --no-pager || true
    fail "YesNAS service failed to start."
  fi

  step "Installation completed"
  local ip_addr
  ip_addr="$(hostname -I 2>/dev/null | awk '{print $1}')"
  log "YesNAS service: ${SERVICE_NAME}"
  log "Install dir: ${INSTALL_DIR}"
  log "Config file: ${CONFIG_DIR}/yesnas.env"
  log "Data root: ${DATA_ROOT}"
  log "Open: http://${ip_addr:-localhost}:${DEFAULT_PORT}"
  log "Service status: systemctl status ${SERVICE_NAME}"
  log "Service logs: journalctl -u ${SERVICE_NAME} -f"
}

main "$@"
