package disks

import "time"

type CandidateList struct {
	Items       []Candidate `json:"items"`
	GeneratedAt time.Time   `json:"generatedAt"`
}

type Candidate struct {
	Path          string   `json:"path"`
	Name          string   `json:"name"`
	KernelName    string   `json:"kernelName"`
	ParentPath    string   `json:"parentPath,omitempty"`
	CandidateType string   `json:"candidateType"`
	Reason        string   `json:"reason"`
	Eligible      bool     `json:"eligible"`
	NeedsWipe     bool     `json:"needsWipe"`
	Warning       string   `json:"warning,omitempty"`
	HasChildren   bool     `json:"hasChildren"`
	Size          string   `json:"size"`
	SizeBytes     uint64   `json:"sizeBytes"`
	Model         string   `json:"model,omitempty"`
	Serial        string   `json:"serial,omitempty"`
	Vendor        string   `json:"vendor,omitempty"`
	Transport     string   `json:"transport,omitempty"`
	FsType        string   `json:"fsType,omitempty"`
	Label         string   `json:"label,omitempty"`
	UUID          string   `json:"uuid,omitempty"`
	Mountpoints   []string `json:"mountpoints,omitempty"`
	Removable     bool     `json:"removable"`
	Hotplug       bool     `json:"hotplug"`
	ReadOnly      bool     `json:"readOnly"`
}
