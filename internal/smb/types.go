package smb

type Share struct {
	ID            string   `db:"id" json:"id"`
	Name          string   `db:"name" json:"name"`
	StoragePoolID string   `db:"storage_pool_id" json:"storagePoolId"`
	Path          string   `db:"path" json:"path"`
	Enabled       bool     `db:"enabled" json:"enabled"`
	Browseable    bool     `db:"browseable" json:"browseable"`
	ReadOnly      bool     `db:"read_only" json:"readOnly"`
	UserIDs       []string `json:"userIds"`
	CreatedAt     string   `db:"created_at" json:"createdAt"`
	UpdatedAt     *string  `db:"updated_at" json:"updatedAt,omitempty"`
}

type ShareUser struct {
	ShareID   string `db:"share_id"`
	UserID    string `db:"user_id"`
	CreatedAt string `db:"created_at"`
}

type UpsertShareRequest struct {
	Name          string   `json:"name"`
	StoragePoolID string   `json:"storagePoolId"`
	Path          string   `json:"path"`
	Enabled       *bool    `json:"enabled,omitempty"`
	Browseable    *bool    `json:"browseable,omitempty"`
	ReadOnly      *bool    `json:"readOnly,omitempty"`
	UserIDs       []string `json:"userIds"`
}
