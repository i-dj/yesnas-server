package storagepool

import "time"

type CreateRequest struct {
	Name           string   `json:"name"`
	RaidLevel      string   `json:"raidLevel"`
	DevicePaths    []string `json:"paths"`
	CacheDiskPaths []string `json:"cacheDiskPaths,omitempty"`
}

type DeleteRequest struct {
	WipeDevices bool `json:"wipeDevices"`
}

type FormatRequest struct {
	Password string `json:"password"`
}

type ReplaceDeviceRequest struct {
	Password      string `json:"password"`
	OldDevicePath string `json:"oldDevicePath"`
	NewDevicePath string `json:"newDevicePath"`
}

type BenchmarkRequest struct {
	SizeGiB int `json:"sizeGiB,omitempty"`
}

type CreateSnapshotRequest struct {
	Name        string `json:"name"`
	SourcePath  string `json:"sourcePath,omitempty"`
	Description string `json:"description,omitempty"`
	ReadOnly    *bool  `json:"readOnly,omitempty"`
}

type RestoreSnapshotRequest struct {
	Password     string `json:"password"`
	CreateBackup bool `json:"createBackup,omitempty"`
}

type LSBLKResponse struct {
	Blockdevices []LSBLKDevice `json:"blockdevices"`
}

type LSBLKDevice struct {
	Name        string        `json:"name"`
	KernelName  string        `json:"kname"`
	Path        string        `json:"path"`
	ParentName  string        `json:"pkname"`
	Type        string        `json:"type"`
	Size        string        `json:"size"`
	Model       string        `json:"model"`
	Serial      string        `json:"serial"`
	Vendor      string        `json:"vendor"`
	Transport   string        `json:"tran"`
	Mountpoint  *string       `json:"mountpoint"`
	Mountpoints []string      `json:"mountpoints"`
	Children    []LSBLKDevice `json:"children"`
}

type Response struct {
	StoragePool
	Kind            string     `json:"kind"`
	Provider        string     `json:"provider,omitempty"`
	RootPath        string     `json:"rootPath,omitempty"`
	AccountEmail    string     `json:"accountEmail,omitempty"`
	Status          string     `json:"status"`
	Health          string     `json:"health"`
	Mounted         bool       `json:"mounted"`
	FilesystemUUID  string     `json:"filesystemUuid,omitempty"`
	TotalBytes      uint64     `json:"totalBytes"`
	UsedBytes       uint64     `json:"usedBytes"`
	FreeBytes       uint64     `json:"freeBytes"`
	UsagePercent    int        `json:"usagePercent"`
	DataProfile     string     `json:"dataProfile,omitempty"`
	MetadataProfile string     `json:"metadataProfile,omitempty"`
	SystemProfile   string     `json:"systemProfile,omitempty"`
	SnapshotCount   int        `json:"snapshotCount"`
	Snapshots       []Snapshot `json:"snapshots"`
	Warnings        []string   `json:"warnings"`
	LastCheckedAt   time.Time  `json:"lastCheckedAt"`
}

type BenchmarkResult struct {
	PoolID                string    `json:"poolId"`
	Path                  string    `json:"path"`
	SizeBytes             int64     `json:"sizeBytes"`
	WriteSpeedBytesPerSec float64   `json:"writeSpeedBytesPerSec"`
	ReadSpeedBytesPerSec  float64   `json:"readSpeedBytesPerSec"`
	TestedAt              time.Time `json:"testedAt"`
}

type BenchmarkProgress struct {
	PoolID                  string    `json:"poolId"`
	Stage                   string    `json:"stage"`
	SizeGiB                 int       `json:"sizeGiB"`
	TotalBytes              int64     `json:"totalBytes"`
	CompletedBytes          int64     `json:"completedBytes"`
	RemainingBytes          int64     `json:"remainingBytes"`
	Percent                 float64   `json:"percent"`
	CurrentSpeedBytesPerSec float64   `json:"currentSpeedBytesPerSec"`
	ElapsedSeconds          float64   `json:"elapsedSeconds"`
	UpdatedAt               time.Time `json:"updatedAt"`
}

type Snapshot struct {
	MetadataID  string     `json:"id,omitempty"`
	SystemID    uint64     `json:"systemSnapshotId,omitempty"`
	Gen         uint64     `json:"systemGeneration,omitempty"`
	TopLevel    uint64     `json:"topLevel,omitempty"`
	Path        string     `json:"path"`
	Name        string     `json:"name"`
	SourcePath  string     `json:"sourcePath,omitempty"`
	SizeBytes   uint64     `json:"sizeBytes"`
	Description string     `json:"description"`
	CreatedBy   string     `json:"createdBy"`
	CreatedAt   *time.Time `json:"createdAt"`
	UpdatedAt   *time.Time `json:"updatedAt,omitempty"`
	IsReadOnly  bool       `json:"isReadOnly"`
	Registered  bool       `json:"registered"`
}

type BtrfsUsage struct {
	FilesystemUUID  string
	TotalBytes      uint64
	UsedBytes       uint64
	FreeBytes       uint64
	DataProfile     string
	MetadataProfile string
	SystemProfile   string
}
