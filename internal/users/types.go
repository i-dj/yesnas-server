package users

type Status string

const (
	StatusActive   Status = "enabled"
	StatusDisabled Status = "disabled"
)

type User struct {
	ID           string  `db:"id" json:"id"`
	Username     string  `db:"username" json:"username"`
	DisplayName  string  `db:"display_name" json:"displayName"`
	IsAdmin      bool    `db:"is_admin" json:"isAdmin"`
	Avatar       string  `db:"avatar" json:"avatar"`
	PasswordHash string  `db:"password_hash" json:"-"`
	Status       string  `db:"status" json:"status"`
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
