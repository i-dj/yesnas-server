package system

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const defaultHardwareInterval = 3 * time.Second

func CollectHardwareSnapshot(ctx context.Context, sampleInterval time.Duration) (HardwareSnapshot, error) {
	if sampleInterval <= 0 {
		sampleInterval = defaultHardwareInterval
	}
	if sampleInterval > 10*time.Second {
		sampleInterval = 10 * time.Second
	}

	ifaces, aliases, err := listVNStatInterfaces(ctx)
	if err != nil || len(ifaces) == 0 {
		ifaces = listSystemInterfaces()
		if aliases == nil {
			aliases = map[string]string{}
		}
	}

	cpuBefore := readCPUSample()
	cpuTelemetryResult := make(chan cpuTelemetry, 1)
	go func() { cpuTelemetryResult <- readCPUTelemetry(ctx) }()
	netBefore := readInterfaceCounterSamples(ifaces)
	select {
	case <-ctx.Done():
		return HardwareSnapshot{}, ctx.Err()
	case <-time.After(sampleInterval):
	}
	cpuAfter := readCPUSample()
	telemetry := <-cpuTelemetryResult
	netAfter := readInterfaceCounterSamples(ifaces)

	disks, err := listSystemDisksDetails(ctx)
	if err != nil {
		return HardwareSnapshot{}, err
	}

	networkInterfaces := make([]NetworkInterfaceStatus, 0, len(ifaces))
	for _, name := range ifaces {
		networkInterfaces = append(networkInterfaces, buildNetworkInterfaceStatus(name, aliases[name], netBefore[name], netAfter[name], sampleInterval))
	}

	return HardwareSnapshot{
		System:            collectHardwareSystemInfo(),
		CPU:               collectCPUStatus(cpuBefore, cpuAfter, telemetry),
		Motherboard:       collectMotherboardStatus(),
		Memory:            collectMemoryStatus(),
		Disks:             disks.Items,
		GPUs:              collectHardwareGPUs(ctx),
		NetworkInterfaces: networkInterfaces,
		CheckedAt:         time.Now().UTC().Format(time.RFC3339),
	}, nil
}

func collectHardwareSystemInfo() HardwareSystemInfo {
	hostname, _ := os.Hostname()
	hostname = strings.TrimSpace(hostname)
	deviceName := hostname
	if deviceName == "" {
		deviceName = readFirstNonEmptyFile(
			"/etc/hostname",
			"/proc/sys/kernel/hostname",
		)
	}
	return HardwareSystemInfo{
		DeviceName:    deviceName,
		Hostname:      hostname,
		OSVersion:     readOSVersion(),
		KernelVersion: readKernelVersion(),
		UptimeSeconds: readUptimeSeconds(),
	}
}

func collectMotherboardStatus() MotherboardStatus {
	return MotherboardStatus{
		Manufacturer: readFirstNonEmptyFile("/sys/class/dmi/id/board_vendor"),
		Product:      readFirstNonEmptyFile("/sys/class/dmi/id/board_name", "/sys/class/dmi/id/product_name"),
		Version:      readFirstNonEmptyFile("/sys/class/dmi/id/board_version", "/sys/class/dmi/id/product_version"),
		Serial:       readFirstNonEmptyFile("/sys/class/dmi/id/board_serial", "/sys/class/dmi/id/product_serial"),
	}
}

func readOSVersion() string {
	content, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return runtime.GOOS
	}
	for _, line := range strings.Split(string(content), "\n") {
		if !strings.HasPrefix(line, "PRETTY_NAME=") {
			continue
		}
		return strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "PRETTY_NAME=")), `"`)
	}
	return runtime.GOOS
}

func readKernelVersion() string {
	if value := readFirstNonEmptyFile("/proc/sys/kernel/osrelease"); value != "" {
		return value
	}
	return runtime.GOOS
}

func readFirstNonEmptyFile(paths ...string) string {
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		value := strings.TrimSpace(string(content))
		if value != "" && value != "None" {
			return value
		}
	}
	return ""
}

func collectHardwareGPUs(ctx context.Context) []HardwareGPUStatus {
	if items := collectHardwareGPUsFromNVIDIA(ctx); len(items) > 0 {
		return items
	}
	return collectHardwareGPUsFromLSPCI(ctx)
}

func collectHardwareGPUsFromNVIDIA(ctx context.Context) []HardwareGPUStatus {
	path, err := exec.LookPath("nvidia-smi")
	if err != nil {
		return nil
	}
	cmd := exec.CommandContext(ctx, path, "--query-gpu=name,temperature.gpu,utilization.gpu,memory.used,memory.total,power.draw", "--format=csv,noheader,nounits")
	output, err := cmd.Output()
	if err != nil {
		return nil
	}
	items := []HardwareGPUStatus{}
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := splitCSVLine(line)
		if len(fields) < 6 {
			continue
		}
		memUsedMiB := parseFloatDefault(fields[3], 0)
		memTotalMiB := parseFloatDefault(fields[4], 0)
		items = append(items, HardwareGPUStatus{
			Name:             strings.TrimSpace(fields[0]),
			Vendor:           "NVIDIA",
			TemperatureC:     parseOptionalFloat(fields[1]),
			UsagePercent:     parseOptionalFloat(fields[2]),
			MemoryUsedBytes:  uint64(memUsedMiB * 1024 * 1024),
			MemoryTotalBytes: uint64(memTotalMiB * 1024 * 1024),
			PowerW:           parseOptionalFloat(fields[5]),
		})
	}
	return items
}

func collectHardwareGPUsFromLSPCI(ctx context.Context) []HardwareGPUStatus {
	path, err := exec.LookPath("lspci")
	if err != nil {
		return nil
	}
	cmd := exec.CommandContext(ctx, path)
	output, err := cmd.Output()
	if err != nil {
		return nil
	}
	items := []HardwareGPUStatus{}
	for _, line := range strings.Split(string(output), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		lower := strings.ToLower(trimmed)
		if !strings.Contains(lower, "vga compatible controller") &&
			!strings.Contains(lower, "3d controller") &&
			!strings.Contains(lower, "display controller") {
			continue
		}
		name := trimmed
		if idx := strings.Index(trimmed, ": "); idx >= 0 {
			name = strings.TrimSpace(trimmed[idx+2:])
		}
		items = append(items, HardwareGPUStatus{
			Name:   name,
			Vendor: detectGPUVendor(name),
		})
	}
	return items
}

func detectGPUVendor(name string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.Contains(lower, "nvidia"):
		return "NVIDIA"
	case strings.Contains(lower, "intel"):
		return "Intel"
	case strings.Contains(lower, "amd"), strings.Contains(lower, "advanced micro devices"), strings.Contains(lower, "radeon"):
		return "AMD"
	default:
		return ""
	}
}
