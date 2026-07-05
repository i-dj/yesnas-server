package storage

import (
	"database/sql"
	"encoding/json"
	"time"

	"nas-server/database"
	"nas-server/pkg/idgen"
)

func List() ([]Storage, error) {
	var items []Storage
	err := database.DB.Select(&items,
		`SELECT id, name, COALESCE(location, '') AS location, mount_path, type, COALESCE(provider, '') AS provider, COALESCE(host, '') AS host, COALESCE(port, 0) AS port, COALESCE(url, '') AS url, COALESCE(username, '') AS username, COALESCE(password, '') AS password, COALESCE(token, '') AS token, COALESCE(domain, '') AS domain, COALESCE(share_name, '') AS share_name, COALESCE(root_path, '') AS root_path, COALESCE(extra_config, '') AS extra_config, status, total_size, free_size, COALESCE(updated_at, created_at, CURRENT_TIMESTAMP) AS updated_at FROM storage`,
	)
	return items, err
}

func Get(id string) (*Storage, error) {
	var item Storage
	err := database.DB.Get(&item,
		`SELECT id, name, COALESCE(location, '') AS location, mount_path, type, COALESCE(provider, '') AS provider, COALESCE(host, '') AS host, COALESCE(port, 0) AS port, COALESCE(url, '') AS url, COALESCE(username, '') AS username, COALESCE(password, '') AS password, COALESCE(token, '') AS token, COALESCE(domain, '') AS domain, COALESCE(share_name, '') AS share_name, COALESCE(root_path, '') AS root_path, COALESCE(extra_config, '') AS extra_config, status, total_size, free_size, COALESCE(updated_at, created_at, CURRENT_TIMESTAMP) AS updated_at FROM storage WHERE id = ?`, id,
	)
	if err != nil {
		return nil, err
	}
	return &item, nil
}

func Add(item Storage) (string, error) {
	if item.ID == "" {
		item.ID = idgen.New()
	}
	_, err := database.DB.Exec(
		`INSERT INTO storage (id, name, location, mount_path, type, provider, host, port, url, username, password, token, domain, share_name, root_path, extra_config, status, total_size, free_size) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.ID, item.Name, item.Location, item.MountPath, item.Type, item.Provider, item.Host, item.Port, item.URL, item.Username, item.Password, item.Token, item.Domain, item.ShareName, item.RootPath, item.ExtraConfig, item.Status, item.TotalSize, item.FreeSize,
	)
	return item.ID, err
}

func GetByMountPath(mountPath string) (*Storage, error) {
	var item Storage
	err := database.DB.Get(&item,
		`SELECT id, name, COALESCE(location, '') AS location, mount_path, type, COALESCE(provider, '') AS provider, COALESCE(host, '') AS host, COALESCE(port, 0) AS port, COALESCE(url, '') AS url, COALESCE(username, '') AS username, COALESCE(password, '') AS password, COALESCE(token, '') AS token, COALESCE(domain, '') AS domain, COALESCE(share_name, '') AS share_name, COALESCE(root_path, '') AS root_path, COALESCE(extra_config, '') AS extra_config, status, total_size, free_size, COALESCE(updated_at, created_at, CURRENT_TIMESTAMP) AS updated_at FROM storage WHERE mount_path = ?`,
		mountPath,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &item, nil
}

func Delete(id string) error {
	_, err := database.DB.Exec(`DELETE FROM storage WHERE id = ?`, id)
	return err
}

func UpdateRuntime(id string, mountPath string, status Status, totalSize int64, freeSize int64, extraConfig string) error {
	_, err := database.DB.Exec(
		`UPDATE storage SET mount_path = ?, status = ?, total_size = ?, free_size = ?, extra_config = ?, updated_at = ? WHERE id = ?`,
		mountPath,
		status,
		totalSize,
		freeSize,
		extraConfig,
		time.Now().UTC(),
		id,
	)
	return err
}

func UpdateIdentity(id string, username string) error {
	_, err := database.DB.Exec(
		`UPDATE storage SET username = ?, updated_at = ? WHERE id = ?`,
		username,
		time.Now().UTC(),
		id,
	)
	return err
}

func BuildExtraConfig(values map[string]any) string {
	if len(values) == 0 {
		return ""
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return ""
	}
	return string(encoded)
}
