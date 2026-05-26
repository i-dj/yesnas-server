package users

type Status string

const (
	StatusActive   Status = "active"
	StatusDisabled Status = "disabled"
)

type SMBStatus string

const (
	SMBStatusDisabled SMBStatus = "disabled"
	SMBStatusActive   SMBStatus = "active"
	SMBStatusError    SMBStatus = "error"
)

type User struct {
	ID           string  `db:"id" json:"id"`
	Username     string  `db:"username" json:"username"`
	DisplayName  string  `db:"display_name" json:"displayName"`
	IsAdmin      bool    `db:"is_admin" json:"isAdmin"`
	Avatar       string  `db:"avatar" json:"avatar"`
	PasswordHash string  `db:"password_hash" json:"-"`
	Status       string  `db:"status" json:"status"`
	SMBUsername  string  `db:"smb_username" json:"smbUsername"`
	SMBStatus    string  `db:"smb_status" json:"smbStatus"`
	SMBSyncedAt  *string `db:"smb_synced_at" json:"smbSyncedAt,omitempty"`
	CreatedAt    string  `db:"created_at" json:"createdAt"`
	UpdatedAt    *string `db:"updated_at" json:"updatedAt,omitempty"`
}

type CreateRequest struct {
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
	IsAdmin     bool   `json:"isAdmin,omitempty"`
	Avatar      string `json:"avatar,omitempty"`
	Password    string `json:"password"`
	Status      string `json:"status,omitempty"`
}

type UpdateRequest struct {
	DisplayName *string `json:"displayName,omitempty"`
	IsAdmin     *bool   `json:"isAdmin,omitempty"`
	Avatar      *string `json:"avatar,omitempty"`
	Password    string  `json:"password,omitempty"`
	Status      *string `json:"status,omitempty"`
}
