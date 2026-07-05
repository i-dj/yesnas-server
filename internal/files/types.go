package files

type FileNodeType string

const (
	FolderType FileNodeType = "folder"
	FileType   FileNodeType = "file"
)

type FileNode struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Type      FileNodeType    `json:"type"`
	ParentID  string          `json:"parentId,omitempty"`
	UpdatedAt string          `json:"updatedAt,omitempty"`
	Size      int64           `json:"size,omitempty"`
	Extension string          `json:"extension,omitempty"`
	IsHidden  bool            `json:"isHidden"`
	MimeType  string          `json:"mimeType"`
	MediaType string          `json:"mediaType"`
	TagColors []FavoriteColor `json:"tagColors"`
}

type Breadcrumb struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type ResponseData struct {
	Breadcrumbs []Breadcrumb `json:"breadcrumbs"`
	Files       []FileNode   `json:"files"`
}

type TrashNode struct {
	ID           string          `json:"id"`
	StorageID    string          `json:"storageId"`
	Name         string          `json:"name"`
	Type         FileNodeType    `json:"type"`
	ParentID     string          `json:"parentId,omitempty"`
	OriginalPath string          `json:"originalPath"`
	RecyclePath  string          `json:"recyclePath"`
	DeletedAt    string          `json:"deletedAt"`
	ExpiresAt    string          `json:"expiresAt,omitempty"`
	Size         int64           `json:"size,omitempty"`
	Extension    string          `json:"extension,omitempty"`
	IsHidden     bool            `json:"isHidden"`
	MimeType     string          `json:"mimeType"`
	MediaType    string          `json:"mediaType"`
	TagColors    []FavoriteColor `json:"tagColors"`
}

type TrashResponseData struct {
	Breadcrumbs []Breadcrumb `json:"breadcrumbs"`
	Files       []TrashNode  `json:"files"`
}

type CreateFolderRequest struct {
	StorageID string `json:"storageId"`
	ParentID  string `json:"parentId"`
	Name      string `json:"name"`
}

type CreateFolderResponse struct {
	ID       string `json:"id"`
	ParentID string `json:"parentId"`
	Name     string `json:"name"`
}

type RenameFileRequest struct {
	Name string `json:"name"`
}

type MoveFileRequest struct {
	ParentID       string `json:"parentId"`
	Name           string `json:"name,omitempty"`
	ConflictPolicy string `json:"conflictPolicy,omitempty"`
}

type CopyFileRequest struct {
	ParentID       string `json:"parentId"`
	Name           string `json:"name,omitempty"`
	ConflictPolicy string `json:"conflictPolicy,omitempty"`
}

type FileConflictCheckRequest struct {
	ParentID string `json:"parentId"`
	Name     string `json:"name,omitempty"`
}

type FileConflictCheckResponse struct {
	HasConflict bool          `json:"hasConflict"`
	Name        string        `json:"name"`
	ParentID    string        `json:"parentId"`
	TargetID    string        `json:"targetId,omitempty"`
	TargetType  *FileNodeType `json:"targetType,omitempty"`
}

type FileOperationResponse struct {
	ID       string       `json:"id"`
	ParentID string       `json:"parentId"`
	Name     string       `json:"name"`
	Type     FileNodeType `json:"type"`
	Path     string       `json:"path"`
}

type DeleteFileResponse struct {
	ID           string `json:"id"`
	StorageID    string `json:"storageId"`
	OriginalPath string `json:"originalPath"`
	RecyclePath  string `json:"recyclePath"`
	Name         string `json:"name"`
}
