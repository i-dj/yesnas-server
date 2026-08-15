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

type Image struct {
	ID         string `json:"id"`
	Repository string `json:"repository"`
	Tag        string `json:"tag"`
	Digest     string `json:"digest,omitempty"`
	Size       string `json:"size"`
	CreatedAt  string `json:"createdAt,omitempty"`
	Created    string `json:"created,omitempty"`
	Icon       string `json:"icon,omitempty"`
}

type Network struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Driver     string `json:"driver"`
	Scope      string `json:"scope"`
	Internal   bool   `json:"internal"`
	IPv6       bool   `json:"ipv6"`
	Subnet     string `json:"subnet,omitempty"`
	Gateway    string `json:"gateway,omitempty"`
	Containers int    `json:"containers"`
}

type Volume struct {
	Name       string `json:"name"`
	Driver     string `json:"driver"`
	Mountpoint string `json:"mountpoint"`
	Scope      string `json:"scope,omitempty"`
	CreatedAt  string `json:"createdAt,omitempty"`
}

type ComposeProject struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	ConfigFiles string `json:"configFiles,omitempty"`
	WorkingDir  string `json:"workingDir,omitempty"`
	Environment string `json:"environment,omitempty"`
	Services    string `json:"services,omitempty"`
}

type ImagePullRequest struct {
	Command string `json:"command"`
}

type ImagePullEvent struct {
	Stage     string  `json:"stage"`
	Message   string  `json:"message"`
	Percent   float64 `json:"percent,omitempty"`
	ImageRef  string  `json:"imageRef,omitempty"`
	ExitCode  int     `json:"exitCode,omitempty"`
	UpdatedAt string  `json:"updatedAt"`
}
