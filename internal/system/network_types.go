package system

type NetworkRange string

const (
	NetworkRangeRealtime NetworkRange = "realtime"
	NetworkRangeHour     NetworkRange = "1h"
	NetworkRangeDay      NetworkRange = "1d"
	NetworkRangeWeek     NetworkRange = "1w"
	NetworkRangeMonth    NetworkRange = "1mo"
)

type NetworkInterfacesSnapshot struct {
	Range       NetworkRange             `json:"range"`
	StepSeconds int64                    `json:"stepSeconds"`
	Interfaces  []NetworkInterfaceStatus `json:"interfaces"`
	CheckedAt   string                   `json:"checkedAt"`
}

type NetworkInterfaceStatus struct {
	Name      string                `json:"name"`
	Alias     string                `json:"alias,omitempty"`
	MAC       string                `json:"mac,omitempty"`
	OperState string                `json:"operState,omitempty"`
	MTU       int                   `json:"mtu,omitempty"`
	IPs       []string              `json:"ips"`
	Speed     NetworkInterfaceSpeed `json:"speed"`
	Points    []NetworkTrafficPoint `json:"points,omitempty"`
}

type NetworkInterfaceSpeed struct {
	RxBytesPerSec int64 `json:"rxBytesPerSec"`
	TxBytesPerSec int64 `json:"txBytesPerSec"`
}

type NetworkTrafficPoint struct {
	Timestamp       string  `json:"timestamp"`
	RxBytes         uint64  `json:"rxBytes"`
	TxBytes         uint64  `json:"txBytes"`
	RxBytesPerSec   float64 `json:"rxBytesPerSec"`
	TxBytesPerSec   float64 `json:"txBytesPerSec"`
	DurationSeconds int64   `json:"durationSeconds"`
}
