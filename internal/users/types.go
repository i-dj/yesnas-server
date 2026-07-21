package users

type Status string

const (
	StatusActive   Status = "enabled"
	StatusDisabled Status = "disabled"
)

type User struct {
	ID           string   `db:"id" json:"id"`
	Username     string   `db:"username" json:"username"`
	DisplayName  string   `db:"display_name" json:"displayName"`
	IsAdmin      bool     `db:"is_admin" json:"isAdmin"`
	Avatar       string   `db:"avatar" json:"avatar"`
	PasswordHash string   `db:"password_hash" json:"-"`
	Status       string   `db:"status" json:"status"`
	CreatedAt    string   `db:"created_at" json:"createdAt"`
	UpdatedAt    *string  `db:"updated_at" json:"updatedAt,omitempty"`
	Groups       []Group  `db:"-" json:"groups"`
	GroupIDs     []string `db:"-" json:"groupIds"`
}

type Group struct {
	ID          string  `db:"id" json:"id"`
	Name        string  `db:"name" json:"name"`
	Description string  `db:"description" json:"description"`
	CreatedAt   string  `db:"created_at" json:"createdAt"`
	UpdatedAt   *string `db:"updated_at" json:"updatedAt,omitempty"`
	UserCount   int     `db:"user_count" json:"userCount"`
}

type CreateRequest struct {
	Username    string   `json:"username"`
	DisplayName string   `json:"displayName"`
	IsAdmin     bool     `json:"isAdmin,omitempty"`
	Avatar      string   `json:"avatar,omitempty"`
	Password    string   `json:"password"`
	Status      string   `json:"status,omitempty"`
	GroupIDs    []string `json:"groupIds,omitempty"`
}

type UpdateRequest struct {
	DisplayName *string  `json:"displayName,omitempty"`
	IsAdmin     *bool    `json:"isAdmin,omitempty"`
	Avatar      *string  `json:"avatar,omitempty"`
	Password    string   `json:"password,omitempty"`
	Status      *string  `json:"status,omitempty"`
	GroupIDs    []string `json:"groupIds,omitempty"`
}

type UpdateMyProfileRequest struct {
	DisplayName *string `json:"displayName,omitempty"`
	Avatar      *string `json:"avatar,omitempty"`
}

type UpdateMyPasswordRequest struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

type CreateGroupRequest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type UpdateGroupRequest struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
}
