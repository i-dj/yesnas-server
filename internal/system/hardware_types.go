package system

import pkgdisks "nas-server/pkg/disks"

type HardwareSnapshot struct {
	System            HardwareSystemInfo       `json:"system"`
	CPU               CPUStatus                `json:"cpu"`
	Motherboard       MotherboardStatus        `json:"motherboard"`
	Memory            MemoryStatus             `json:"memory"`
	Disks             []pkgdisks.DiskInfo      `json:"disks"`
	GPUs              []HardwareGPUStatus      `json:"gpus"`
	NetworkInterfaces []NetworkInterfaceStatus `json:"networkInterfaces"`
	CheckedAt         string                   `json:"checkedAt"`
}

type HardwareSystemInfo struct {
	DeviceName    string `json:"deviceName"`
	Hostname      string `json:"hostname"`
	OSVersion     string `json:"osVersion"`
	KernelVersion string `json:"kernelVersion"`
	UptimeSeconds int64  `json:"uptimeSeconds"`
}

type MotherboardStatus struct {
	Manufacturer string `json:"manufacturer"`
	Product      string `json:"product"`
	Version      string `json:"version"`
	Serial       string `json:"serial"`
}

type HardwareGPUStatus struct {
	Name             string   `json:"name"`
	Vendor           string   `json:"vendor"`
	UsagePercent     *float64 `json:"usagePercent,omitempty"`
	TemperatureC     *float64 `json:"temperatureC,omitempty"`
	MemoryUsedBytes  uint64   `json:"memoryUsedBytes"`
	MemoryTotalBytes uint64   `json:"memoryTotalBytes"`
	PowerW           *float64 `json:"powerW,omitempty"`
}
