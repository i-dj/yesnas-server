# YesNAS Server

YesNAS Server is the backend service for [YesNAS](https://www.yesnas.com). It provides user authentication, storage management, file sharing, background jobs, system information, and audit logging, while serving as a secure backend foundation for the web interface and future AI capabilities.

## Key Features

- Manage local disks, storage pools, network storage, and cloud storage
- Share files through SMB, NFS, FTP, and WebDAV
- Provide authentication, access control, background jobs, and audit logs
- Monitor hardware, system health, and storage capacity
- Provide an extensible backend for AI features such as model configuration, API authentication, request proxying, asynchronous jobs, and invocation logs

> AI integrations are under development. The current release does not yet include direct integrations with services such as OpenAI, Ollama, or DeepSeek.

## Related Projects

- [yesnas-server](https://github.com/i-dj/yesnas-server): YesNAS backend service (this repository)
- [yesnas](https://github.com/i-dj/yesnas): YesNAS web management interface
- [yesnas-web](https://github.com/i-dj/yesnas-web): YesNAS official website
- [www.yesnas.com](https://www.yesnas.com): YesNAS website

## System Requirements

- Debian or Ubuntu
- `amd64` or `arm64` architecture
- An existing Linux user with `sudo` access

The installer automatically installs and configures the required system components, including Samba, NFS, FTP, WebDAV, Btrfs, rclone, and FFmpeg. To keep port `80` available for other applications, the Apache default HTTP listener is moved to port `28081` during installation.

The YesNAS-managed WebDAV endpoint listens on port `28088`.

## One-Command Installation

```bash
curl -fsSL https://raw.githubusercontent.com/i-dj/yesnas-server/main/scripts/install.sh | bash
```

The installer asks for two values:

- **Service user:** the Linux user that runs the YesNAS backend. Press Enter to use the user who started the installer.
- **Device name (hostname):** the name used to identify this NAS on the system and local network. Press Enter to keep the current hostname.

### Non-interactive installation

Parent installers and automated deployments can disable all YesNAS Server prompts without detaching the terminal or using `setsid`:

```bash
curl -fsSL https://raw.githubusercontent.com/i-dj/yesnas-server/main/scripts/install.sh \
  | bash -s -- --non-interactive --user "$(id -un)" --hostname "$(hostname)"
```

The equivalent environment-variable form is:

```bash
curl -fsSL https://raw.githubusercontent.com/i-dj/yesnas-server/main/scripts/install.sh \
  | YESNAS_NONINTERACTIVE=1 YESNAS_USER="$(id -un)" YESNAS_HOSTNAME="$(hostname)" bash
```

Environment variables must be applied to `bash`, on the right side of the pipe. Applying them only to `curl` does not pass them to the installer.

Non-interactive mode never opens `/dev/tty` and uses non-interactive `sudo`. Run the parent installer as root, or ensure it already has a valid passwordless/cached sudo credential.

After installation, the service listens on port `28080` by default:

```text
http://SERVER_IP:28080
```

When a new database starts for the first time, YesNAS creates a default administrator account:

```text
Username: admin
Password: admin
```

Change the default password immediately after your first sign-in.

## One-Command Upgrade

```bash
curl -fsSL https://raw.githubusercontent.com/i-dj/yesnas-server/main/scripts/upgrade.sh | bash
```

The upgrade script downloads the latest release for the current architecture, verifies its checksum, stops the service, backs up the existing binary, installs the new version, and restarts the service. It does not overwrite the existing database or user data.

## One-Command Uninstall

```bash
curl -fsSL https://raw.githubusercontent.com/i-dj/yesnas-server/main/scripts/uninstall.sh | bash
```

The uninstall script requires `YESNAS` as confirmation. Removing the data directory and system dependencies requires separate confirmation to reduce the risk of deleting important files or affecting other services.

## Common Commands

```bash
# Check service status
systemctl status yesnas-server

# Follow service logs
journalctl -u yesnas-server -f

# Restart the service
sudo systemctl restart yesnas-server
```

## Directory Layout

The default directories have different purposes:

- `/opt/yesnas/server`: application directory containing the executable, SQLite database, GeoIP database, and runtime files
- `/etc/yesnas-server`: configuration directory containing the YesNAS Server environment configuration
- `/srv/yesnas`: NAS file-data root containing user files, cloud-storage data, upload cache, and WebDAV data; it does not contain the application's SQLite database

These paths can be changed through environment variables supported by the installation scripts.

## Database

YesNAS Server uses SQLite for users, storage configuration, jobs, audit records, and other backend data. On first startup, the application creates the schema from the embedded `database/init.sql` file and applies compatibility migrations. The runtime database is application data and is stored at:

```text
/opt/yesnas/server/data/nas.db
```

Release archives do not contain the runtime database or user data. They include the GeoLite2 City database for IP geolocation in audit logs.

## License

Licensing information will be provided in the repository's `LICENSE` file.
