package storage

type Type string

const (
	Local   Type = "local"
	Cloud   Type = "cloud"
	SMB     Type = "smb"
	FTP     Type = "ftp"
	WebDAV  Type = "webdav"
	NFS     Type = "nfs"
	S3      Type = "s3"
	Google  Type = "google"
	Dropbox Type = "dropbox"
)

type Provider string

const (
	ProviderGoogleDrive Provider = "google_drive"
	ProviderOneDrive    Provider = "onedrive"
	ProviderBaiduPan    Provider = "baidu_pan"
	ProviderDropbox     Provider = "dropbox"
	ProviderS3          Provider = "s3"
	ProviderWebDAV      Provider = "webdav"
	ProviderSMB         Provider = "smb"
	ProviderNFS         Provider = "nfs"
)

type Status string

const (
	StatusOnline  Status = "online"
	StatusOffline Status = "offline"
	StatusError   Status = "error"
)

type Storage struct {
	ID          string `db:"id" json:"id"`
	Name        string `db:"name" json:"name"`
	Location    string `db:"location" json:"location"`
	MountPath   string `db:"mount_path" json:"mountPath"`
	Type        Type   `db:"type" json:"type"`
	Provider    string `db:"provider" json:"provider"`
	Host        string `db:"host" json:"host"`
	Port        int    `db:"port" json:"port"`
	URL         string `db:"url" json:"url"`
	Username    string `db:"username" json:"username"`
	Password    string `db:"password" json:"-"`
	Token       string `db:"token" json:"-"`
	Domain      string `db:"domain" json:"domain"`
	ShareName   string `db:"share_name" json:"shareName"`
	RootPath    string `db:"root_path" json:"rootPath"`
	ExtraConfig string `db:"extra_config" json:"extraConfig"`
	Status      Status `db:"status" json:"status"`
	TotalSize   int64  `db:"total_size" json:"totalSize"`
	FreeSize    int64  `db:"free_size" json:"freeSize"`
	UpdatedAt   string `db:"updated_at" json:"updatedAt"`
}

type Token struct {
	ID           string  `db:"id" json:"id"`
	StorageID    string  `db:"storage_id" json:"storageId"`
	TokenType    string  `db:"token_type" json:"tokenType"`
	AccessToken  string  `db:"access_token" json:"-"`
	RefreshToken string  `db:"refresh_token" json:"-"`
	Expiry       *string `db:"expiry" json:"expiry,omitempty"`
	Scope        string  `db:"scope" json:"scope"`
	RawJSON      string  `db:"raw_json" json:"-"`
	CreatedAt    string  `db:"created_at" json:"createdAt"`
	UpdatedAt    *string `db:"updated_at" json:"updatedAt,omitempty"`
}
