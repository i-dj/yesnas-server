package storagepool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	commandrunner "nas-server/pkg/shell"
)

const systemdMountUnitDir = "/etc/systemd/system"

func persistPoolMount(ctx context.Context, devicePath string, mountPath string) error {
	uuidResult, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "blkid", "-s", "UUID", "-o", "value", devicePath)
	if err != nil {
		return fmt.Errorf("read filesystem uuid for %s: %w", devicePath, err)
	}
	uuid := strings.TrimSpace(uuidResult.Stdout)
	if uuid == "" {
		return fmt.Errorf("filesystem uuid is empty for %s", devicePath)
	}
	unitName, err := mountUnitName(mountPath)
	if err != nil {
		return err
	}
	unitPath := filepath.Join(systemdMountUnitDir, unitName)
	unitContent := renderMountUnit(uuid, mountPath)
	if _, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true, Stdin: unitContent}, "tee", unitPath); err != nil {
		return fmt.Errorf("write mount unit %s: %w", unitPath, err)
	}
	if _, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "systemctl", "daemon-reload"); err != nil {
		return fmt.Errorf("systemctl daemon-reload: %w", err)
	}
	if _, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "systemctl", "enable", "--now", unitName); err != nil {
		return fmt.Errorf("enable mount unit %s: %w", unitName, err)
	}
	return nil
}

func removePoolMountUnit(ctx context.Context, mountPath string) error {
	unitName, err := mountUnitName(mountPath)
	if err != nil {
		return err
	}
	unitPath := filepath.Join(systemdMountUnitDir, unitName)
	if _, statErr := os.Stat(unitPath); statErr == nil {
		if _, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "systemctl", "disable", "--now", unitName); err != nil {
			return fmt.Errorf("disable mount unit %s: %w", unitName, err)
		}
		if _, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "rm", "-f", unitPath); err != nil {
			return fmt.Errorf("remove mount unit %s: %w", unitPath, err)
		}
		if _, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "systemctl", "daemon-reload"); err != nil {
			return fmt.Errorf("systemctl daemon-reload: %w", err)
		}
		return nil
	} else if !os.IsNotExist(statErr) {
		return fmt.Errorf("stat mount unit %s: %w", unitPath, statErr)
	}
	if isMountpointActive(mountPath) {
		if _, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "umount", mountPath); err != nil {
			return fmt.Errorf("unmount pool: %w", err)
		}
	}
	return nil
}

func removeLegacyPoolMountFromFstab(ctx context.Context, mountPath string) error {
	content, err := os.ReadFile("/etc/fstab")
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	lines := strings.Split(string(content), "\n")
	filtered := make([]string, 0, len(lines))
	changed := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") && strings.Contains(trimmed, mountPath) {
			changed = true
			continue
		}
		filtered = append(filtered, line)
	}
	if !changed {
		return nil
	}
	updated := strings.Join(filtered, "\n")
	if !strings.HasSuffix(updated, "\n") {
		updated += "\n"
	}
	if _, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true, Stdin: updated}, "tee", "/etc/fstab"); err != nil {
		return fmt.Errorf("update /etc/fstab: %w", err)
	}
	return nil
}

func mountUnitName(mountPath string) (string, error) {
	clean := filepath.Clean(strings.TrimSpace(mountPath))
	if clean == "" || clean == "." || !strings.HasPrefix(clean, "/") {
		return "", fmt.Errorf("invalid mount path: %s", mountPath)
	}
	if clean == "/" {
		return "-.mount", nil
	}
	res, err := commandrunner.Run(context.Background(), "systemd-escape", "--path", "--suffix=mount", clean)
	if err == nil {
		name := strings.TrimSpace(res.Stdout)
		if name != "" {
			return name, nil
		}
	}
	trimmed := strings.TrimPrefix(clean, "/")
	parts := strings.Split(trimmed, "/")
	escaped := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.ReplaceAll(part, "-", `\x2d`)
		escaped = append(escaped, part)
	}
	return strings.Join(escaped, "-") + ".mount", nil
}

func renderMountUnit(uuid string, mountPath string) string {
	return fmt.Sprintf(`[Unit]
Description=YesNAS storage pool mount for %s
After=local-fs-pre.target
Before=local-fs.target

[Mount]
What=UUID=%s
Where=%s
Type=btrfs
Options=defaults,nofail,x-systemd.device-timeout=5

[Install]
WantedBy=local-fs.target
`, mountPath, uuid, mountPath)
}
