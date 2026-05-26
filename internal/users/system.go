package users

import (
	"context"
	"fmt"
	"strings"

	commandrunner "nas-server/pkg/shell"
)

func SyncSambaAccount(ctx context.Context, user User, password string, enabled bool) error {
	if strings.TrimSpace(user.SMBUsername) == "" {
		return fmt.Errorf("smb username is required")
	}
	if strings.TrimSpace(password) != "" {
		if err := ensureLinuxUser(ctx, user.SMBUsername); err != nil {
			return err
		}
		if err := setSambaPassword(ctx, user.SMBUsername, password); err != nil {
			return err
		}
	}
	if enabled {
		return EnableSambaAccount(ctx, user)
	}
	return DisableSambaAccount(ctx, user)
}

func EnableSambaAccount(ctx context.Context, user User) error {
	if strings.TrimSpace(user.SMBUsername) == "" {
		return fmt.Errorf("smb username is required")
	}
	if _, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "smbpasswd", "-e", user.SMBUsername); err != nil {
		return fmt.Errorf("enable samba user %s: %w", user.SMBUsername, err)
	}
	return UpdateSMBState(user.ID, SMBStatusActive)
}

func DisableSambaAccount(ctx context.Context, user User) error {
	if strings.TrimSpace(user.SMBUsername) == "" {
		return fmt.Errorf("smb username is required")
	}
	if _, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "smbpasswd", "-d", user.SMBUsername); err != nil {
		return fmt.Errorf("disable samba user %s: %w", user.SMBUsername, err)
	}
	return UpdateSMBState(user.ID, SMBStatusDisabled)
}

func ensureLinuxUser(ctx context.Context, smbUsername string) error {
	if _, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "id", "-u", smbUsername); err == nil {
		return nil
	}
	if _, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "useradd", "-M", "-s", "/usr/sbin/nologin", smbUsername); err != nil {
		return fmt.Errorf("create linux user %s: %w", smbUsername, err)
	}
	return nil
}

func setSambaPassword(ctx context.Context, smbUsername string, password string) error {
	stdin := password + "\n" + password + "\n"
	if _, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true, Stdin: stdin}, "smbpasswd", "-a", "-s", smbUsername); err != nil {
		return fmt.Errorf("set samba password for %s: %w", smbUsername, err)
	}
	return nil
}
