package smb

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"nas-server/database"
	"nas-server/pkg/idgen"
)

func ListShares() ([]Share, error) {
	var items []Share
	if err := database.DB.Select(&items, `SELECT id, name, storage_pool_id, path, enabled, browseable, read_only, created_at, updated_at FROM smb_shares ORDER BY created_at DESC`); err != nil {
		return nil, err
	}
	for i := range items {
		userIDs, err := listShareUserIDs(items[i].ID)
		if err != nil {
			return nil, err
		}
		items[i].UserIDs = userIDs
	}
	return items, nil
}

func GetShare(id string) (*Share, error) {
	var item Share
	if err := database.DB.Get(&item, `SELECT id, name, storage_pool_id, path, enabled, browseable, read_only, created_at, updated_at FROM smb_shares WHERE id = ?`, strings.TrimSpace(id)); err != nil {
		return nil, err
	}
	userIDs, err := listShareUserIDs(item.ID)
	if err != nil {
		return nil, err
	}
	item.UserIDs = userIDs
	return &item, nil
}

func UpsertShare(id string, req UpsertShareRequest) (*Share, error) {
	name := strings.TrimSpace(req.Name)
	if err := validateShareName(name); err != nil {
		return nil, err
	}
	path := filepath.Clean(strings.TrimSpace(req.Path))
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("share path must be absolute")
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	browseable := true
	if req.Browseable != nil {
		browseable = *req.Browseable
	}
	readOnly := false
	if req.ReadOnly != nil {
		readOnly = *req.ReadOnly
	}
	now := time.Now().Format(time.RFC3339)
	if strings.TrimSpace(id) == "" {
		id = idgen.New()
		_, err := database.DB.Exec(
			`INSERT INTO smb_shares (id, name, storage_pool_id, path, enabled, browseable, read_only, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, name, strings.TrimSpace(req.StoragePoolID), path, boolToInt(enabled), boolToInt(browseable), boolToInt(readOnly), now, now,
		)
		if err != nil {
			return nil, fmt.Errorf("create smb share: %w", err)
		}
	} else {
		_, err := database.DB.Exec(
			`UPDATE smb_shares SET name = ?, storage_pool_id = ?, path = ?, enabled = ?, browseable = ?, read_only = ?, updated_at = ? WHERE id = ?`,
			name, strings.TrimSpace(req.StoragePoolID), path, boolToInt(enabled), boolToInt(browseable), boolToInt(readOnly), now, strings.TrimSpace(id),
		)
		if err != nil {
			return nil, fmt.Errorf("update smb share: %w", err)
		}
	}
	if err := replaceShareUsers(id, req.UserIDs); err != nil {
		return nil, err
	}
	return GetShare(id)
}

func DeleteShare(id string) error {
	_, err := database.DB.Exec(`DELETE FROM smb_shares WHERE id = ?`, strings.TrimSpace(id))
	return err
}

func listShareUserIDs(shareID string) ([]string, error) {
	var ids []string
	err := database.DB.Select(&ids, `SELECT user_id FROM smb_share_users WHERE share_id = ? ORDER BY created_at ASC`, shareID)
	return ids, err
}

func replaceShareUsers(shareID string, userIDs []string) error {
	tx, err := database.DB.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM smb_share_users WHERE share_id = ?`, shareID); err != nil {
		return err
	}
	seen := map[string]struct{}{}
	for _, userID := range userIDs {
		userID = strings.TrimSpace(userID)
		if userID == "" {
			continue
		}
		if _, ok := seen[userID]; ok {
			continue
		}
		seen[userID] = struct{}{}
		if _, err := tx.Exec(`INSERT INTO smb_share_users (share_id, user_id) VALUES (?, ?)`, shareID, userID); err != nil {
			return fmt.Errorf("add smb share user: %w", err)
		}
	}
	return tx.Commit()
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func validateShareName(name string) error {
	if name == "" {
		return fmt.Errorf("share name is required")
	}
	if utf8.RuneCountInString(name) > 64 {
		return fmt.Errorf("share name must be 64 characters or fewer")
	}
	if name == "." || name == ".." {
		return fmt.Errorf("share name cannot be . or ..")
	}
	if strings.ContainsAny(name, `/\:*?"<>|[]`) {
		return fmt.Errorf(`share name cannot contain / \ : * ? " < > | [ ]`)
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("share name cannot contain control characters")
		}
	}
	return nil
}
