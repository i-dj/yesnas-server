package disks

import "time"

type DiskList struct {
	Items       []DiskInfo `json:"items"`
	GeneratedAt time.Time  `json:"generatedAt"`
}

type DiskInfo struct {
	Path             string              `json:"path"`
	Name             string              `json:"name"`
	KernelName       string              `json:"kernelName"`
	Size             string              `json:"size"`
	SizeBytes        uint64              `json:"sizeBytes"`
	Model            string              `json:"model,omitempty"`
	Serial           string              `json:"serial,omitempty"`
	Vendor           string              `json:"vendor,omitempty"`
	Transport        string              `json:"transport,omitempty"`
	FsType           string              `json:"fsType,omitempty"`
	Label            string              `json:"label,omitempty"`
	UUID             string              `json:"uuid,omitempty"`
	Mountpoints      []string            `json:"mountpoints,omitempty"`
	Removable        bool                `json:"removable"`
	Hotplug          bool                `json:"hotplug"`
	ReadOnly         bool                `json:"readOnly"`
	HasChildren      bool                `json:"hasChildren"`
	Usage            string              `json:"usage"`
	Usages           []DiskUsage         `json:"usages,omitempty"`
	InUse            bool                `json:"inUse"`
	TemperatureC     *int                `json:"temperatureC,omitempty"`
	Health           string              `json:"health,omitempty"`
	SMARTAvailable   bool                `json:"smartAvailable"`
	SMARTPassed      *bool               `json:"smartPassed,omitempty"`
	PowerOnHours     *uint64             `json:"powerOnHours,omitempty"`
	PowerCycleCount  *uint64             `json:"powerCycleCount,omitempty"`
	RotationRateRPM  *int                `json:"rotationRateRpm,omitempty"`
	ReadBytesTotal   uint64              `json:"readBytesTotal"`
	WriteBytesTotal  uint64              `json:"writeBytesTotal"`
	ReadOpsTotal     uint64              `json:"readOpsTotal"`
	WriteOpsTotal    uint64              `json:"writeOpsTotal"`
	ReadBytesPerSec  float64             `json:"readBytesPerSec"`
	WriteBytesPerSec float64             `json:"writeBytesPerSec"`
	ReadOpsPerSec    float64             `json:"readOpsPerSec"`
	WriteOpsPerSec   float64             `json:"writeOpsPerSec"`
	Partitions       []DiskPartitionInfo `json:"partitions,omitempty"`
	SampledAt        time.Time           `json:"sampledAt"`
	Warnings         []string            `json:"warnings,omitempty"`
}

type DiskPartitionInfo struct {
	Path              string      `json:"path"`
	Name              string      `json:"name"`
	KernelName        string      `json:"kernelName"`
	ParentPath        string      `json:"parentPath,omitempty"`
	Size              string      `json:"size"`
	SizeBytes         uint64      `json:"sizeBytes"`
	FsType            string      `json:"fsType,omitempty"`
	Label             string      `json:"label,omitempty"`
	UUID              string      `json:"uuid,omitempty"`
	Mountpoints       []string    `json:"mountpoints,omitempty"`
	ReadOnly          bool        `json:"readOnly"`
	HasChildren       bool        `json:"hasChildren"`
	Usage             string      `json:"usage"`
	Usages            []DiskUsage `json:"usages,omitempty"`
	InUse             bool        `json:"inUse"`
	IsSystemPartition bool        `json:"isSystemPartition"`
	IsRaidPartition   bool        `json:"isRaidPartition"`
}

type DiskUsage struct {
	Type            string `json:"type"`
	Label           string `json:"label,omitempty"`
	Mountpoint      string `json:"mountpoint,omitempty"`
	StoragePoolID   string `json:"storagePoolId,omitempty"`
	StoragePoolName string `json:"storagePoolName,omitempty"`
	StorageID       string `json:"storageId,omitempty"`
	DeviceRole      string `json:"deviceRole,omitempty"`
}

type lsblkResponse struct {
	Blockdevices []lsblkDevice `json:"blockdevices"`
}

type lsblkDevice struct {
	Name        string        `json:"name"`
	KName       string        `json:"kname"`
	Path        string        `json:"path"`
	Type        string        `json:"type"`
	Size        string        `json:"size"`
	FsType      string        `json:"fstype"`
	Label       string        `json:"label"`
	UUID        string        `json:"uuid"`
	Model       string        `json:"model"`
	Serial      string        `json:"serial"`
	Vendor      string        `json:"vendor"`
	Tran        string        `json:"tran"`
	Mountpoint  string        `json:"mountpoint"`
	Mountpoints []interface{} `json:"mountpoints"`
	RM          bool          `json:"rm"`
	Hotplug     bool          `json:"hotplug"`
	RO          bool          `json:"ro"`
	Children    []lsblkDevice `json:"children"`
}
