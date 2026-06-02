package sharing

type Protocol string

const (
	ProtocolSMB    Protocol = "smb"
	ProtocolFTP    Protocol = "ftp"
	ProtocolWebDAV Protocol = "webdav"
	ProtocolNFS    Protocol = "nfs"
)

type ShareStatus string

const (
	ShareStatusEnabled  ShareStatus = "enabled"
	ShareStatusDisabled ShareStatus = "disabled"
)

type Share struct {
	ID                 string      `db:"id" json:"id"`
	Name               string      `db:"name" json:"name"`
	StoragePoolID      string      `db:"storage_pool_id" json:"storagePoolId"`
	Path               string      `db:"path" json:"path"`
	ProtocolsJSON      string      `db:"protocols" json:"-"`
	UserIDsJSON        string      `db:"user_ids" json:"-"`
	ClientNetworksJSON string      `db:"client_networks" json:"-"`
	Status             ShareStatus `db:"status" json:"status"`
	Enabled            bool        `json:"enabled"`
	Protocols          []Protocol  `json:"protocols"`
	UserIDs            []string    `json:"userIds"`
	Users              []ShareUser `json:"users"`
	ClientNetworks     []string    `json:"clientNetworks"`
	CreatedAt          string      `db:"created_at" json:"createdAt"`
	UpdatedAt          *string     `db:"updated_at" json:"updatedAt,omitempty"`
}

type ShareUser struct {
	ID          string `db:"id" json:"id"`
	Username    string `db:"username" json:"username"`
	DisplayName string `db:"display_name" json:"displayName"`
	Avatar      string `db:"avatar" json:"avatar"`
	Status      string `db:"status" json:"status"`
}

type UpsertShareRequest struct {
	Name           string     `json:"name"`
	StoragePoolID  string     `json:"storagePoolId"`
	Path           string     `json:"path"`
	Status         string     `json:"status,omitempty"`
	Enabled        *bool      `json:"enabled,omitempty"`
	Protocols      []Protocol `json:"protocols"`
	UserIDs        []string   `json:"userIds"`
	ClientNetworks []string   `json:"clientNetworks"`
}

type ProtocolSummary struct {
	Protocol Protocol `json:"protocol"`
	Enabled  bool     `json:"enabled"`
	ShareURL string   `json:"shareUrl,omitempty"`
	Count    int      `json:"count"`
}

type ProtocolService struct {
	Protocol    Protocol `json:"protocol"`
	ServiceName string   `json:"serviceName"`
	Active      bool     `json:"active"`
	Status      string   `json:"status"`
	ShareURL    string   `json:"shareUrl"`
	Port        int      `json:"port"`
	ShareCount  int      `json:"shareCount"`
}

type ProtocolActionRequest struct {
	Action string `json:"action"`
}
