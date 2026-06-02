package system

type SystemStatusSnapshot struct {
	Status       SystemHealthStatus `json:"status"`
	SystemDisk   DiskStatus         `json:"systemDisk"`
	Network      NetworkStatus      `json:"network"`
	FileSharing  FileSharingStatus  `json:"fileSharing"`
	CPU          CPUStatus          `json:"cpu"`
	Memory       MemoryStatus       `json:"memory"`
	GPU          *GPUStatus         `json:"gpu,omitempty"`
	TopProcesses []MonitoredProcess `json:"topProcesses"`
	CheckedAt    string             `json:"checkedAt"`
}

type SystemHealthStatus struct {
	State         string `json:"state"`
	UptimeSeconds int64  `json:"uptimeSeconds"`
}

type DiskStatus struct {
	MountPath    string  `json:"mountPath"`
	TotalBytes   uint64  `json:"totalBytes"`
	UsedBytes    uint64  `json:"usedBytes"`
	FreeBytes    uint64  `json:"freeBytes"`
	UsagePercent float64 `json:"usagePercent"`
	Health       string  `json:"health"`
}

type NetworkStatus struct {
	DownloadBytesPerSec int64 `json:"downloadBytesPerSec"`
	UploadBytesPerSec   int64 `json:"uploadBytesPerSec"`
}

type FileSharingStatus struct {
	OnlineUsers int                      `json:"onlineUsers"`
	Services    FileSharingServiceCounts `json:"services"`
}

type FileSharingServiceCounts struct {
	SMB    int `json:"smb"`
	WebDAV int `json:"webdav"`
	NFS    int `json:"nfs"`
}

type CPUStatus struct {
	Model        string   `json:"model"`
	UsagePercent float64  `json:"usagePercent"`
	FrequencyGHz *float64 `json:"frequencyGhz,omitempty"`
	TemperatureC *float64 `json:"temperatureC,omitempty"`
	Cores        int      `json:"cores"`
	Threads      int      `json:"threads"`
	FanRPM       *int     `json:"fanRpm,omitempty"`
	PowerW       *float64 `json:"powerW,omitempty"`
}

type MemoryStatus struct {
	TotalBytes      uint64  `json:"totalBytes"`
	UsedBytes       uint64  `json:"usedBytes"`
	AvailableBytes  uint64  `json:"availableBytes"`
	UsagePercent    float64 `json:"usagePercent"`
	PressurePercent float64 `json:"pressurePercent"`
}

type GPUStatus struct {
	Name             string   `json:"name"`
	UsagePercent     *float64 `json:"usagePercent,omitempty"`
	TemperatureC     *float64 `json:"temperatureC,omitempty"`
	MemoryUsedBytes  uint64   `json:"memoryUsedBytes"`
	MemoryTotalBytes uint64   `json:"memoryTotalBytes"`
	PowerW           *float64 `json:"powerW,omitempty"`
}

type MonitoredProcess struct {
	Name        string  `json:"name"`
	CPUPercent  float64 `json:"cpuPercent"`
	MemoryBytes uint64  `json:"memoryBytes"`
	Status      string  `json:"status"`
}
