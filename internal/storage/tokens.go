package storage

import (
	"database/sql"
	"time"

	"nas-server/database"
	"nas-server/pkg/idgen"
)

func GetTokenByStorageID(storageID string) (*Token, error) {
	var item Token
	err := database.DB.Get(&item,
		`SELECT id, storage_id, COALESCE(token_type, '') AS token_type, COALESCE(access_token, '') AS access_token, COALESCE(refresh_token, '') AS refresh_token, expiry, COALESCE(scope, '') AS scope, COALESCE(raw_json, '') AS raw_json, COALESCE(created_at, CURRENT_TIMESTAMP) AS created_at, updated_at FROM storage_token WHERE storage_id = ?`,
		storageID,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &item, nil
}

func UpsertToken(item Token) (string, error) {
	if item.ID == "" {
		existing, err := GetTokenByStorageID(item.StorageID)
		if err != nil {
			return "", err
		}
		if existing != nil {
			item.ID = existing.ID
		} else {
			item.ID = idgen.New()
		}
	}

	now := time.Now()
	_, err := database.DB.Exec(
		`INSERT INTO storage_token (id, storage_id, token_type, access_token, refresh_token, expiry, scope, raw_json, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(storage_id) DO UPDATE SET
		 token_type = excluded.token_type,
		 access_token = excluded.access_token,
		 refresh_token = excluded.refresh_token,
		 expiry = excluded.expiry,
		 scope = excluded.scope,
		 raw_json = excluded.raw_json,
		 updated_at = excluded.updated_at`,
		item.ID,
		item.StorageID,
		item.TokenType,
		item.AccessToken,
		item.RefreshToken,
		item.Expiry,
		item.Scope,
		item.RawJSON,
		now,
	)
	if err != nil {
		return "", err
	}
	return item.ID, nil
}

func DeleteTokenByStorageID(storageID string) error {
	_, err := database.DB.Exec(`DELETE FROM storage_token WHERE storage_id = ?`, storageID)
	return err
}
