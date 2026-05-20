package files

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"nas-server/database"
	"nas-server/internal/storage"
	"nas-server/pkg/idgen"
)

type FavoriteColor string

const (
	ColorRed    FavoriteColor = "red"
	ColorBlue   FavoriteColor = "blue"
	ColorGreen  FavoriteColor = "green"
	ColorYellow FavoriteColor = "yellow"
	ColorGray   FavoriteColor = "gray"
)

type Favorite struct {
	ID        string          `db:"id" json:"id"`
	StorageID string          `db:"storage_id" json:"storageId"`
	FileName  string          `db:"file_name" json:"fileName"`
	FilePath  string          `db:"file_path" json:"filePath"`
	Colors    []FavoriteColor `db:"-" json:"colors"`
	CreatedAt time.Time       `db:"created_at" json:"createdAt"`
}

type RecycleBinItem struct {
	ID           string     `db:"id" json:"id"`
	StorageID    string     `db:"storage_id" json:"storageId"`
	FileName     string     `db:"file_name" json:"fileName"`
	OriginalPath string     `db:"original_path" json:"originalPath"`
	RecyclePath  string     `db:"recycle_path" json:"recyclePath"`
	FileType     string     `db:"file_type" json:"fileType"`
	Size         int64      `db:"size" json:"size"`
	DeletedAt    time.Time  `db:"deleted_at" json:"deletedAt"`
	ExpiresAt    *time.Time `db:"expires_at" json:"expiresAt,omitempty"`
}

func ListFavorites() ([]Favorite, error) {
	query := `SELECT id, storage_id, file_name, file_path, color, created_at FROM favorites ORDER BY created_at DESC`
	rows, err := database.DB.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []Favorite
	for rows.Next() {
		var item Favorite
		var colorData string
		if err := rows.Scan(&item.ID, &item.StorageID, &item.FileName, &item.FilePath, &colorData, &item.CreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(colorData), &item.Colors); err != nil {
			item.Colors = []FavoriteColor{FavoriteColor(colorData)}
		}
		list = append(list, item)
	}
	return list, nil
}

func DeleteFavoriteByPath(storageID, filePath string) error {
	_, err := database.DB.Exec(`DELETE FROM favorites WHERE storage_id = ? AND file_path = ?`, storageID, filePath)
	return err
}

func AddRecycleBinItem(item RecycleBinItem) (string, error) {
	if item.ID == "" {
		item.ID = idgen.New()
	}
	query := `
		INSERT INTO recycle_bin (
			id, storage_id, file_name, original_path, recycle_path, file_type, size, expires_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`
	_, err := database.DB.Exec(
		query,
		item.ID,
		item.StorageID,
		item.FileName,
		item.OriginalPath,
		item.RecyclePath,
		item.FileType,
		item.Size,
		item.ExpiresAt,
	)
	if err != nil {
		return "", fmt.Errorf("recycle_bin insert failed: %w", err)
	}
	return item.ID, nil
}

func ListRecycleBinItemsByStorage(storageID string) ([]RecycleBinItem, error) {
	var items []RecycleBinItem
	query := `
		SELECT id, storage_id, file_name, original_path, recycle_path, file_type, size, deleted_at, expires_at
		FROM recycle_bin
		WHERE storage_id = ?
		ORDER BY deleted_at DESC
	`
	if err := database.DB.Select(&items, query, storageID); err != nil {
		return nil, err
	}
	return items, nil
}

func ListAllRecycleBinItems() ([]RecycleBinItem, error) {
	var items []RecycleBinItem
	query := `
		SELECT id, storage_id, file_name, original_path, recycle_path, file_type, size, deleted_at, expires_at
		FROM recycle_bin
		ORDER BY deleted_at DESC
	`
	if err := database.DB.Select(&items, query); err != nil {
		return nil, err
	}
	return items, nil
}

func (f *Favorite) GetFullPath() (string, error) {
	storageRecord, err := storage.Get(f.StorageID)
	if err != nil {
		return "", fmt.Errorf("find storage failed: %w", err)
	}
	return filepath.Join(storageRecord.MountPath, f.FilePath), nil
}
