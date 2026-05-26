package smb

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"nas-server/internal/storagepool"
	"nas-server/internal/users"
	commandrunner "nas-server/pkg/shell"
)

const (
	sambaConfigPath = "/etc/samba/smb.conf"
	yesnasBegin     = "# BEGIN YESNAS MANAGED SHARES"
	yesnasEnd       = "# END YESNAS MANAGED SHARES"
)

func ApplyConfig(ctx context.Context) error {
	shares, err := ListShares()
	if err != nil {
		return fmt.Errorf("list smb shares: %w", err)
	}
	allUsers, err := users.List()
	if err != nil {
		return fmt.Errorf("list users: %w", err)
	}
	userByID := map[string]users.User{}
	for _, user := range allUsers {
		userByID[user.ID] = user
	}

	if err := ensureSharePaths(ctx, shares); err != nil {
		return err
	}
	activeUserIDs := map[string]struct{}{}
	config := renderSharesConfig(shares, userByID, activeUserIDs)
	if err := syncUserSMBStatus(ctx, allUsers, activeUserIDs); err != nil {
		return err
	}
	if err := writeManagedSambaConfig(ctx, config); err != nil {
		return err
	}
	if _, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "testparm", "-s", sambaConfigPath); err != nil {
		return fmt.Errorf("validate samba config: %w", err)
	}
	if _, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "systemctl", "reload", "smbd"); err != nil {
		return fmt.Errorf("reload smbd: %w", err)
	}
	return nil
}

func ensureSharePaths(ctx context.Context, shares []Share) error {
	for _, share := range shares {
		if !share.Enabled {
			continue
		}
		if isCloudSharePath(share) {
			continue
		}
		if _, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "mkdir", "-p", share.Path); err != nil {
			return fmt.Errorf("create smb share path %s: %w", share.Path, err)
		}
		if _, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "chmod", "0777", share.Path); err != nil {
			return fmt.Errorf("chmod smb share path %s: %w", share.Path, err)
		}
	}
	return nil
}

func isCloudSharePath(share Share) bool {
	if strings.HasPrefix(strings.TrimSpace(share.Path), "/srv/yesnas/cloud/") {
		return true
	}
	if strings.TrimSpace(share.StoragePoolID) == "" {
		return false
	}
	pool, err := storagepool.Get(share.StoragePoolID)
	if err != nil || pool == nil {
		return false
	}
	return strings.TrimSpace(pool.Filesystem) == "google_drive" || strings.HasPrefix(strings.TrimSpace(pool.MountPath), "/srv/yesnas/cloud/")
}

func renderSharesConfig(shares []Share, userByID map[string]users.User, activeUserIDs map[string]struct{}) string {
	var builder strings.Builder
	builder.WriteString(yesnasBegin + "\n")
	builder.WriteString("\n[global]\n")
	builder.WriteString("   access based share enum = yes\n")
	builder.WriteString("   map to guest = Bad User\n")
	sort.Slice(shares, func(i, j int) bool { return shares[i].Name < shares[j].Name })
	for _, share := range shares {
		if !share.Enabled {
			continue
		}
		isPublicShare := len(share.UserIDs) == 0
		validUsers := make([]string, 0, len(share.UserIDs))
		for _, userID := range share.UserIDs {
			user, ok := userByID[userID]
			if !ok || user.Status != string(users.StatusActive) || strings.TrimSpace(user.SMBUsername) == "" {
				continue
			}
			activeUserIDs[user.ID] = struct{}{}
			validUsers = append(validUsers, user.SMBUsername)
		}
		if !isPublicShare && len(validUsers) == 0 {
			continue
		}
		sort.Strings(validUsers)
		builder.WriteString("\n")
		builder.WriteString("[" + share.Name + "]\n")
		builder.WriteString("   path = " + share.Path + "\n")
		builder.WriteString("   browseable = " + yesNo(share.Browseable) + "\n")
		builder.WriteString("   read only = " + yesNo(share.ReadOnly) + "\n")
		if isPublicShare {
			builder.WriteString("   guest ok = yes\n")
			builder.WriteString("   public = yes\n")
		} else {
			builder.WriteString("   guest ok = no\n")
			builder.WriteString("   valid users = " + strings.Join(validUsers, " ") + "\n")
		}
	}
	builder.WriteString("\n" + yesnasEnd + "\n")
	return builder.String()
}

func syncUserSMBStatus(ctx context.Context, allUsers []users.User, activeUserIDs map[string]struct{}) error {
	for _, user := range allUsers {
		_, active := activeUserIDs[user.ID]
		if active && user.SMBStatus != string(users.SMBStatusActive) {
			if err := users.EnableSambaAccount(ctx, user); err != nil {
				_ = users.UpdateSMBState(user.ID, users.SMBStatusError)
				return err
			}
		}
		if !active && user.SMBStatus == string(users.SMBStatusActive) {
			if err := users.DisableSambaAccount(ctx, user); err != nil {
				_ = users.UpdateSMBState(user.ID, users.SMBStatusError)
				return err
			}
		}
	}
	return nil
}

func writeManagedSambaConfig(ctx context.Context, managed string) error {
	currentBytes, _ := os.ReadFile(sambaConfigPath)
	current := string(currentBytes)
	if strings.TrimSpace(current) == "" {
		current = "[global]\n   server role = standalone server\n"
	}
	updated := replaceManagedBlock(current, managed)
	if _, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true, Stdin: updated}, "tee", sambaConfigPath); err != nil {
		return fmt.Errorf("write samba config: %w", err)
	}
	return nil
}

func replaceManagedBlock(content string, managed string) string {
	start := strings.Index(content, yesnasBegin)
	end := strings.Index(content, yesnasEnd)
	if start >= 0 && end > start {
		end += len(yesnasEnd)
		return strings.TrimSpace(content[:start]) + "\n\n" + strings.TrimSpace(managed) + "\n\n" + strings.TrimSpace(content[end:]) + "\n"
	}
	return strings.TrimSpace(content) + "\n\n" + strings.TrimSpace(managed) + "\n"
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}
