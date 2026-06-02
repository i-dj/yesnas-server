package sharing

import (
	"context"
	"fmt"
	"strings"

	commandrunner "nas-server/pkg/shell"
)

type protocolServiceSpec struct {
	Protocol    Protocol
	ServiceName string
	Scheme      string
	Port        int
	URL         func(host string) string
}

var protocolServiceSpecs = []protocolServiceSpec{
	{Protocol: ProtocolSMB, ServiceName: "smbd", Port: 445, URL: func(host string) string { return "smb://" + host + ":445" }},
	{Protocol: ProtocolFTP, ServiceName: "proftpd", Port: 21, URL: func(host string) string { return "ftp://" + host + ":21" }},
	{Protocol: ProtocolWebDAV, ServiceName: "apache2", Port: 8088, URL: func(host string) string { return "http://" + host + ":8088" }},
	{Protocol: ProtocolNFS, ServiceName: "nfs-server", Port: 2049, URL: func(host string) string { return host + ":/" }},
}

func ListProtocolServices(ctx context.Context, host string) ([]ProtocolService, error) {
	shares, err := ListShares()
	if err != nil {
		return nil, err
	}
	counts := enabledProtocolCounts(shares)
	services := make([]ProtocolService, 0, len(protocolServiceSpecs))
	for _, spec := range protocolServiceSpecs {
		status := systemdActiveState(ctx, spec.ServiceName)
		services = append(services, ProtocolService{
			Protocol:    spec.Protocol,
			ServiceName: spec.ServiceName,
			Active:      status == "active",
			Status:      status,
			ShareURL:    spec.URL(host),
			Port:        spec.Port,
			ShareCount:  counts[spec.Protocol],
		})
	}
	return services, nil
}

func ControlProtocolService(ctx context.Context, protocol Protocol, action string, host string) (*ProtocolService, error) {
	spec, ok := protocolServiceSpecByProtocol(protocol)
	if !ok {
		return nil, fmt.Errorf("unsupported protocol: %s", protocol)
	}
	action = strings.ToLower(strings.TrimSpace(action))
	switch action {
	case "start", "stop", "restart":
	default:
		return nil, fmt.Errorf("action must be start, stop, or restart")
	}
	if _, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "systemctl", action, spec.ServiceName); err != nil {
		return nil, fmt.Errorf("%s %s: %w", action, spec.ServiceName, err)
	}
	status := systemdActiveState(ctx, spec.ServiceName)
	return &ProtocolService{
		Protocol:    spec.Protocol,
		ServiceName: spec.ServiceName,
		Active:      status == "active",
		Status:      status,
		ShareURL:    spec.URL(host),
		Port:        spec.Port,
		ShareCount:  0,
	}, nil
}

func enabledProtocolCounts(shares []Share) map[Protocol]int {
	counts := map[Protocol]int{}
	for _, share := range shares {
		if share.Status != ShareStatusEnabled {
			continue
		}
		for _, protocol := range share.Protocols {
			counts[protocol]++
		}
	}
	return counts
}

func systemdActiveState(ctx context.Context, serviceName string) string {
	result, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{}, "systemctl", "is-active", serviceName)
	if err != nil {
		status := strings.TrimSpace(result.Stdout)
		if status == "" {
			status = strings.TrimSpace(result.Stderr)
		}
		if status == "" {
			status = "unknown"
		}
		return status
	}
	status := strings.TrimSpace(result.Stdout)
	if status == "" {
		return "unknown"
	}
	return status
}

func protocolServiceSpecByProtocol(protocol Protocol) (protocolServiceSpec, bool) {
	for _, spec := range protocolServiceSpecs {
		if spec.Protocol == protocol {
			return spec, true
		}
	}
	return protocolServiceSpec{}, false
}
