package docker

type Container struct {
	ID               string           `json:"id"`
	Name             string           `json:"name"`
	Image            string           `json:"image"`
	State            string           `json:"state"`
	Status           string           `json:"status"`
	Running          bool             `json:"running"`
	CreatedAt        string           `json:"createdAt"`
	StartedAt        string           `json:"startedAt,omitempty"`
	FinishedAt       string           `json:"finishedAt,omitempty"`
	UptimeSeconds    int64            `json:"uptimeSeconds"`
	CPUPercent       float64          `json:"cpuPercent"`
	MemoryUsageBytes uint64           `json:"memoryUsageBytes"`
	MemoryLimitBytes uint64           `json:"memoryLimitBytes"`
	MemoryPercent    float64          `json:"memoryPercent"`
	NetworkRxBytes   uint64           `json:"networkRxBytes"`
	NetworkTxBytes   uint64           `json:"networkTxBytes"`
	Ports            []ContainerPort  `json:"ports"`
	Mounts           []ContainerMount `json:"mounts"`
}

type ContainerPort struct {
	IP            string `json:"ip,omitempty"`
	HostPort      int    `json:"hostPort,omitempty"`
	ContainerPort int    `json:"containerPort"`
	Protocol      string `json:"protocol"`
}

type ContainerMount struct {
	Type        string `json:"type"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Mode        string `json:"mode,omitempty"`
	ReadWrite   bool   `json:"readWrite"`
	Name        string `json:"name,omitempty"`
}
