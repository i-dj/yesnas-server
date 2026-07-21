package users

import (
	"fmt"
	osuser "os/user"
	"regexp"
	"strings"
	"time"

	"nas-server/database"
	"nas-server/pkg/idgen"
)

var usernamePattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]{1,31}$`)
var reservedSystemUsernames = map[string]struct{}{
	"adm": {}, "admin": {}, "apache": {}, "backup": {}, "bin": {}, "daemon": {}, "dbus": {},
	"ftp": {}, "games": {}, "gnats": {}, "irc": {}, "list": {}, "lp": {}, "mail": {},
	"man": {}, "messagebus": {}, "mysql": {}, "news": {}, "nobody": {}, "nogroup": {},
	"operator": {}, "postgres": {}, "proxy": {}, "root": {}, "sshd": {}, "sync": {},
	"sys": {}, "systemd-coredump": {}, "systemd-network": {}, "systemd-resolve": {},
	"systemd-timesync": {}, "uucp": {}, "www-data": {},
}

func List() ([]User, error) {
	var items []User
	err := database.DB.Select(&items, userSelectSQL+` ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	return hydrateUsers(items)
}

func Get(id string) (*User, error) {
	var item User
	err := database.DB.Get(&item, userSelectSQL+` WHERE id = ?`, strings.TrimSpace(id))
	if err != nil {
		return nil, err
	}
	if err := hydrateUser(&item); err != nil {
		return nil, err
	}
	return &item, nil
}

func GetByUsername(username string) (*User, error) {
	var item User
	err := database.DB.Get(&item, userSelectSQL+` WHERE username = ?`, normalizeUsername(username))
	if err != nil {
		return nil, err
	}
	if err := hydrateUser(&item); err != nil {
		return nil, err
	}
	return &item, nil
}

func ListByIDs(ids []string) ([]User, error) {
	result := make([]User, 0, len(ids))
	for _, id := range ids {
		item, err := Get(id)
		if err != nil {
			return nil, err
		}
		result = append(result, *item)
	}
	return result, nil
}

func Authenticate(username string, password string) (*User, error) {
	item, err := GetByUsername(username)
	if err != nil {
		return nil, err
	}
	if strings.EqualFold(strings.TrimSpace(item.Status), string(StatusDisabled)) {
		return nil, fmt.Errorf("user is disabled")
	}
	if !verifyPassword(password, item.PasswordHash) {
		return nil, fmt.Errorf("invalid username or password")
	}
	return item, nil
}

func Create(req CreateRequest) (*User, string, error) {
	username := normalizeUsername(req.Username)
	if err := validateUsername(username); err != nil {
		return nil, "", err
	}
	passwordHash, err := hashPassword(req.Password)
	if err != nil {
		return nil, "", err
	}
	status := strings.TrimSpace(req.Status)
	if status == "" {
		status = string(StatusActive)
	}
	if status != string(StatusActive) && status != string(StatusDisabled) {
		return nil, "", fmt.Errorf("invalid user status")
	}
	if err := validateGroupIDs(req.GroupIDs); err != nil {
		return nil, "", err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	item := &User{
		ID:           idgen.New(),
		Username:     username,
		DisplayName:  strings.TrimSpace(req.DisplayName),
		IsAdmin:      req.IsAdmin,
		Avatar:       normalizeAvatar(req.Avatar),
		PasswordHash: passwordHash,
		Status:       status,
		CreatedAt:    now,
	}
	_, err = database.DB.Exec(
		`INSERT INTO users (id, username, display_name, is_admin, avatar, password_hash, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.ID, item.Username, item.DisplayName, item.IsAdmin, item.Avatar, item.PasswordHash, item.Status, now, now,
	)
	if err != nil {
		return nil, "", fmt.Errorf("create user: %w", err)
	}
	if err := ReplaceUserGroups(item.ID, req.GroupIDs); err != nil {
		return nil, "", err
	}
	created, err := Get(item.ID)
	if err != nil {
		return nil, "", err
	}
	return created, req.Password, nil
}

func Update(id string, req UpdateRequest) (*User, string, error) {
	item, err := Get(id)
	if err != nil {
		return nil, "", err
	}
	displayName := item.DisplayName
	if req.DisplayName != nil {
		displayName = strings.TrimSpace(*req.DisplayName)
	}
	isAdmin := item.IsAdmin
	if req.IsAdmin != nil {
		isAdmin = *req.IsAdmin
	}
	avatar := item.Avatar
	if req.Avatar != nil {
		avatar = normalizeAvatar(*req.Avatar)
	}
	status := item.Status
	if req.Status != nil {
		status = strings.TrimSpace(*req.Status)
		if status != string(StatusActive) && status != string(StatusDisabled) {
			return nil, "", fmt.Errorf("invalid user status")
		}
	}
	passwordHash := item.PasswordHash
	plainPassword := ""
	if strings.TrimSpace(req.Password) != "" {
		passwordHash, err = hashPassword(req.Password)
		if err != nil {
			return nil, "", err
		}
		plainPassword = req.Password
	}
	if req.GroupIDs != nil {
		if err := validateGroupIDs(req.GroupIDs); err != nil {
			return nil, "", err
		}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = database.DB.Exec(
		`UPDATE users SET display_name = ?, is_admin = ?, avatar = ?, password_hash = ?, status = ?, updated_at = ? WHERE id = ?`,
		displayName, isAdmin, avatar, passwordHash, status, now, item.ID,
	)
	if err != nil {
		return nil, "", fmt.Errorf("update user: %w", err)
	}
	if req.GroupIDs != nil {
		if err := ReplaceUserGroups(item.ID, req.GroupIDs); err != nil {
			return nil, "", err
		}
	}
	updated, err := Get(item.ID)
	return updated, plainPassword, err
}

func UpdateMyProfile(userID string, req UpdateMyProfileRequest) (*User, error) {
	item, err := Get(userID)
	if err != nil {
		return nil, err
	}
	displayName := item.DisplayName
	if req.DisplayName != nil {
		displayName = strings.TrimSpace(*req.DisplayName)
	}
	avatar := item.Avatar
	if req.Avatar != nil {
		avatar = normalizeAvatar(*req.Avatar)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := database.DB.Exec(
		`UPDATE users SET display_name = ?, avatar = ?, updated_at = ? WHERE id = ?`,
		displayName, avatar, now, item.ID,
	); err != nil {
		return nil, fmt.Errorf("update profile: %w", err)
	}
	return Get(item.ID)
}

func UpdateMyPassword(userID string, req UpdateMyPasswordRequest) error {
	item, err := Get(userID)
	if err != nil {
		return err
	}
	if !verifyPassword(req.CurrentPassword, item.PasswordHash) {
		return fmt.Errorf("current password is incorrect")
	}
	passwordHash, err := hashPassword(req.NewPassword)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = database.DB.Exec(
		`UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?`,
		passwordHash, now, item.ID,
	)
	if err != nil {
		return fmt.Errorf("update password: %w", err)
	}
	return nil
}

func Delete(id string) error {
	_, err := database.DB.Exec(`DELETE FROM users WHERE id = ?`, strings.TrimSpace(id))
	return err
}

func ListGroups() ([]Group, error) {
	var items []Group
	err := database.DB.Select(&items, `SELECT g.id, g.name, COALESCE(g.description, '') AS description, g.created_at, g.updated_at, COUNT(ug.user_id) AS user_count
		FROM "groups" g
		LEFT JOIN user_groups ug ON ug.group_id = g.id
		GROUP BY g.id
		ORDER BY g.created_at ASC, g.id ASC`)
	if items == nil {
		items = []Group{}
	}
	return items, err
}

func GetGroup(id string) (*Group, error) {
	var item Group
	err := database.DB.Get(&item, `SELECT g.id, g.name, COALESCE(g.description, '') AS description, g.created_at, g.updated_at, COUNT(ug.user_id) AS user_count
		FROM "groups" g
		LEFT JOIN user_groups ug ON ug.group_id = g.id
		WHERE g.id = ?
		GROUP BY g.id`, strings.TrimSpace(id))
	if err != nil {
		return nil, err
	}
	return &item, nil
}

func CreateGroup(req CreateGroupRequest) (*Group, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, fmt.Errorf("group name is required")
	}
	now := time.Now().UTC().Format(time.RFC3339)
	id := idgen.New()
	_, err := database.DB.Exec(
		`INSERT INTO "groups" (id, name, description, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		id, name, strings.TrimSpace(req.Description), now, now,
	)
	if err != nil {
		return nil, fmt.Errorf("create group: %w", err)
	}
	return GetGroup(id)
}

func UpdateGroup(id string, req UpdateGroupRequest) (*Group, error) {
	item, err := GetGroup(id)
	if err != nil {
		return nil, err
	}
	name := item.Name
	if req.Name != nil {
		name = strings.TrimSpace(*req.Name)
		if name == "" {
			return nil, fmt.Errorf("group name is required")
		}
	}
	description := item.Description
	if req.Description != nil {
		description = strings.TrimSpace(*req.Description)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := database.DB.Exec(
		`UPDATE "groups" SET name = ?, description = ?, updated_at = ? WHERE id = ?`,
		name, description, now, item.ID,
	); err != nil {
		return nil, fmt.Errorf("update group: %w", err)
	}
	return GetGroup(item.ID)
}

func DeleteGroup(id string) error {
	_, err := database.DB.Exec(`DELETE FROM "groups" WHERE id = ?`, strings.TrimSpace(id))
	return err
}

func ReplaceUserGroups(userID string, groupIDs []string) error {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return fmt.Errorf("user id is required")
	}
	tx, err := database.DB.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM user_groups WHERE user_id = ?`, userID); err != nil {
		return err
	}

	seen := map[string]struct{}{}
	now := time.Now().UTC().Format(time.RFC3339)
	for _, groupID := range groupIDs {
		groupID = strings.TrimSpace(groupID)
		if groupID == "" {
			continue
		}
		if _, ok := seen[groupID]; ok {
			continue
		}
		seen[groupID] = struct{}{}
		var exists int
		if err := tx.Get(&exists, `SELECT COUNT(1) FROM "groups" WHERE id = ?`, groupID); err != nil {
			return err
		}
		if exists == 0 {
			return fmt.Errorf("group not found")
		}
		if _, err := tx.Exec(`INSERT INTO user_groups (user_id, group_id, created_at) VALUES (?, ?, ?)`, userID, groupID, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func validateGroupIDs(groupIDs []string) error {
	seen := map[string]struct{}{}
	for _, groupID := range groupIDs {
		groupID = strings.TrimSpace(groupID)
		if groupID == "" {
			continue
		}
		if _, ok := seen[groupID]; ok {
			continue
		}
		seen[groupID] = struct{}{}
		var exists int
		if err := database.DB.Get(&exists, `SELECT COUNT(1) FROM "groups" WHERE id = ?`, groupID); err != nil {
			return err
		}
		if exists == 0 {
			return fmt.Errorf("group not found")
		}
	}
	return nil
}

func CountAdmins() (int, error) {
	var count int
	err := database.DB.Get(&count, `SELECT COUNT(1) FROM users WHERE COALESCE(is_admin, 0) = 1`)
	return count, err
}

func EnsureDefaultAdmin() (bool, error) {
	var count int
	if err := database.DB.Get(&count, `SELECT COUNT(1) FROM users`); err != nil {
		return false, fmt.Errorf("count users: %w", err)
	}
	if count > 0 {
		return false, nil
	}
	passwordHash, err := hashPassword("admin")
	if err != nil {
		return false, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := database.DB.Exec(
		`INSERT INTO users (id, username, display_name, is_admin, avatar, password_hash, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		idgen.New(), "admin", "Administrator", true, "", passwordHash, string(StatusActive), now, now,
	); err != nil {
		return false, fmt.Errorf("create default admin user: %w", err)
	}
	return true, nil
}

const userSelectSQL = `SELECT id, username, COALESCE(display_name, '') AS display_name, COALESCE(is_admin, 0) AS is_admin, COALESCE(avatar, '') AS avatar, password_hash, status, created_at, updated_at FROM users`

func hydrateUsers(items []User) ([]User, error) {
	for i := range items {
		if err := hydrateUser(&items[i]); err != nil {
			return nil, err
		}
	}
	if items == nil {
		items = []User{}
	}
	return items, nil
}

func hydrateUser(item *User) error {
	var groups []Group
	if err := database.DB.Select(&groups, `SELECT g.id, g.name, COALESCE(g.description, '') AS description, g.created_at, g.updated_at, 0 AS user_count
		FROM "groups" g
		JOIN user_groups ug ON ug.group_id = g.id
		WHERE ug.user_id = ?
		ORDER BY g.name COLLATE NOCASE ASC`, item.ID); err != nil {
		return err
	}
	item.Groups = groups
	item.GroupIDs = make([]string, 0, len(groups))
	for _, group := range groups {
		item.GroupIDs = append(item.GroupIDs, group.ID)
	}
	return nil
}

func normalizeUsername(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func normalizeAvatar(value string) string {
	return strings.TrimSpace(value)
}

func validateUsername(username string) error {
	if !usernamePattern.MatchString(username) {
		return fmt.Errorf("username must start with a letter and contain 2-32 letters, numbers, underscores, or dashes")
	}
	if _, reserved := reservedSystemUsernames[username]; reserved {
		return fmt.Errorf("username is reserved by the system")
	}
	if _, err := osuser.Lookup(username); err == nil {
		return fmt.Errorf("username already exists on this system")
	}
	return nil
}
