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
	return items, err
}

func Get(id string) (*User, error) {
	var item User
	err := database.DB.Get(&item, userSelectSQL+` WHERE id = ?`, strings.TrimSpace(id))
	if err != nil {
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
	now := time.Now().Format(time.RFC3339)
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
	return item, req.Password, nil
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
	now := time.Now().Format(time.RFC3339)
	_, err = database.DB.Exec(
		`UPDATE users SET display_name = ?, is_admin = ?, avatar = ?, password_hash = ?, status = ?, updated_at = ? WHERE id = ?`,
		displayName, isAdmin, avatar, passwordHash, status, now, item.ID,
	)
	if err != nil {
		return nil, "", fmt.Errorf("update user: %w", err)
	}
	updated, err := Get(item.ID)
	return updated, plainPassword, err
}

func Delete(id string) error {
	_, err := database.DB.Exec(`DELETE FROM users WHERE id = ?`, strings.TrimSpace(id))
	return err
}

func CountAdmins() (int, error) {
	var count int
	err := database.DB.Get(&count, `SELECT COUNT(1) FROM users WHERE COALESCE(is_admin, 0) = 1`)
	return count, err
}

const userSelectSQL = `SELECT id, username, COALESCE(display_name, '') AS display_name, COALESCE(is_admin, 0) AS is_admin, COALESCE(avatar, '') AS avatar, password_hash, status, created_at, updated_at FROM users`

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
