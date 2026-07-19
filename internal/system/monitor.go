package system

import (
	"bufio"
	"context"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type cpuSample struct {
	total uint64
	idle  uint64
}

type cpuTelemetry struct {
	temperatureC *float64
	powerW       *float64
}

type networkSample struct {
	rx int64
	tx int64
}

type diskIOSample struct {
	readBytes  int64
	writeBytes int64
}

func CollectSystemStatus(ctx context.Context, sampleInterval time.Duration) (SystemStatusSnapshot, error) {
	if sampleInterval <= 0 {
		sampleInterval = time.Second
	}
	if sampleInterval > 10*time.Second {
		sampleInterval = 10 * time.Second
	}

	cpuBefore := readCPUSample()
	cpuTelemetryResult := make(chan cpuTelemetry, 1)
	go func() { cpuTelemetryResult <- readCPUTelemetry(ctx) }()
	netBefore := readNetworkSample()
	diskBefore := readDiskIOSample()
	select {
	case <-ctx.Done():
		return SystemStatusSnapshot{}, ctx.Err()
	case <-time.After(sampleInterval):
	}
	cpuAfter := readCPUSample()
	telemetry := <-cpuTelemetryResult
	netAfter := readNetworkSample()
	diskAfter := readDiskIOSample()

	status := SystemStatusSnapshot{
		Status:      collectHealthStatus(),
		SystemDisk:  collectSystemDisk(),
		Load:        collectSystemLoad(),
		DiskIO:      buildDiskIOStatus(diskBefore, diskAfter, sampleInterval),
		Network:     buildNetworkStatus(netBefore, netAfter, sampleInterval),
		FileSharing: collectFileSharingStatus(),
		CPU:         collectCPUStatus(cpuBefore, cpuAfter, telemetry),
		Memory:      collectMemoryStatus(),
		GPU:         collectGPUStatus(ctx),
		CheckedAt:   time.Now().UTC().Format(time.RFC3339),
	}
	return status, nil
}

func collectHealthStatus() SystemHealthStatus {
	uptime := readUptimeSeconds()
	return SystemHealthStatus{
		State:         "healthy",
		UptimeSeconds: uptime,
	}
}

func collectSystemDisk() DiskStatus {
	const mountPath = "/"
	var stat syscall.Statfs_t
	if err := syscall.Statfs(mountPath, &stat); err != nil {
		return DiskStatus{MountPath: mountPath, Health: "unknown"}
	}
	blockSize := uint64(stat.Bsize)
	total := stat.Blocks * blockSize
	free := stat.Bavail * blockSize
	used := total - free
	usagePercent := percentage(used, total)
	health := "healthy"
	if usagePercent >= 90 {
		health = "warning"
	}
	return DiskStatus{
		MountPath:    mountPath,
		TotalBytes:   total,
		UsedBytes:    used,
		FreeBytes:    free,
		UsagePercent: usagePercent,
		Health:       health,
	}
}

func collectSystemLoad() SystemLoadStatus {
	content, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return SystemLoadStatus{}
	}
	fields := strings.Fields(string(content))
	if len(fields) < 3 {
		return SystemLoadStatus{}
	}
	return SystemLoadStatus{
		Load1:  round1(parseFloatDefault(fields[0], 0)),
		Load5:  round1(parseFloatDefault(fields[1], 0)),
		Load15: round1(parseFloatDefault(fields[2], 0)),
	}
}

func readUptimeSeconds() int64 {
	content, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(content))
	if len(fields) == 0 {
		return 0
	}
	value, _ := strconv.ParseFloat(fields[0], 64)
	return int64(value)
}

func readCPUSample() cpuSample {
	content, err := os.ReadFile("/proc/stat")
	if err != nil {
		return cpuSample{}
	}
	scanner := bufio.NewScanner(strings.NewReader(string(content)))
	if !scanner.Scan() {
		return cpuSample{}
	}
	fields := strings.Fields(scanner.Text())
	if len(fields) < 5 || fields[0] != "cpu" {
		return cpuSample{}
	}
	var total uint64
	var idle uint64
	for i := 1; i < len(fields); i++ {
		value, _ := strconv.ParseUint(fields[i], 10, 64)
		total += value
		if i == 4 || i == 5 {
			idle += value
		}
	}
	return cpuSample{total: total, idle: idle}
}

func buildCPUUsage(before, after cpuSample) float64 {
	totalDelta := after.total - before.total
	idleDelta := after.idle - before.idle
	if totalDelta == 0 || idleDelta > totalDelta {
		return 0
	}
	return round1(float64(totalDelta-idleDelta) * 100 / float64(totalDelta))
}

func collectCPUStatus(before, after cpuSample, telemetry cpuTelemetry) CPUStatus {
	model, cores, threads, freq := readCPUInfo()
	if threads == 0 {
		threads = runtime.NumCPU()
	}
	if cores == 0 {
		cores = threads
	}
	temp := telemetry.temperatureC
	if temp == nil {
		temp = readFirstFloatFromGlob([]string{
			"/sys/class/thermal/thermal_zone*/temp",
			"/sys/class/hwmon/hwmon*/temp*_input",
		}, 1000)
	}
	power := telemetry.powerW
	if power == nil {
		power = readFirstFloatFromGlob([]string{
			"/sys/class/hwmon/hwmon*/power*_input",
		}, 1000000)
	}
	status := CPUStatus{
		Model:        model,
		UsagePercent: buildCPUUsage(before, after),
		FrequencyGHz: freq,
		TemperatureC: temp,
		Cores:        cores,
		Threads:      threads,
		PowerW:       power,
	}
	if status.Model == "" {
		status.Model = "CPU"
	}
	return status
}

func readCPUInfo() (string, int, int, *float64) {
	content, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return "", 0, runtime.NumCPU(), nil
	}
	model := ""
	coreIDs := map[string]struct{}{}
	processorCount := 0
	var mhz float64
	currentPhysicalID := "0"
	scanner := bufio.NewScanner(strings.NewReader(string(content)))
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		switch key {
		case "model name":
			if model == "" {
				model = value
			}
		case "physical id":
			currentPhysicalID = value
		case "core id":
			coreIDs[currentPhysicalID+":"+value] = struct{}{}
		case "processor":
			processorCount++
		case "cpu MHz":
			if mhz == 0 {
				mhz, _ = strconv.ParseFloat(value, 64)
			}
		}
	}
	var freq *float64
	if mhz > 0 {
		value := round1(mhz / 1000)
		freq = &value
	}
	return model, len(coreIDs), processorCount, freq
}

func collectMemoryStatus() MemoryStatus {
	values := readMemInfo()
	total := values["MemTotal"] * 1024
	available := values["MemAvailable"] * 1024
	if available == 0 {
		available = values["MemFree"] * 1024
	}
	used := total - available
	usagePercent := percentage(used, total)
	pressure := readMemoryPressure()
	if pressure == 0 {
		pressure = usagePercent
	}
	memoryType, speedMHz, manufacturer, partNumber := readMemoryHardwareInfo()
	return MemoryStatus{
		TotalBytes:      total,
		UsedBytes:       used,
		AvailableBytes:  available,
		UsagePercent:    usagePercent,
		PressurePercent: round1(pressure),
		Type:            memoryType,
		SpeedMHz:        speedMHz,
		Manufacturer:    manufacturer,
		PartNumber:      partNumber,
	}
}

func readMemoryHardwareInfo() (string, *int, string, string) {
	if memoryType, speedMHz, manufacturer, partNumber := readMemoryHardwareInfoFromDMI(); memoryType != "" || speedMHz != nil || manufacturer != "" || partNumber != "" {
		return memoryType, speedMHz, manufacturer, partNumber
	}
	return "", nil, "", ""
}

func readMemoryHardwareInfoFromDMI() (string, *int, string, string) {
	output, err := runMemoryInfoCommand(
		[]string{"sudo", "-n", "dmidecode", "-t", "memory"},
		[]string{"dmidecode", "-t", "memory"},
	)
	if err != nil || strings.TrimSpace(output) == "" {
		return "", nil, "", ""
	}
	return parseDMIMemoryInfo(output)
}

func runMemoryInfoCommand(commands ...[]string) (string, error) {
	for _, parts := range commands {
		if len(parts) == 0 {
			continue
		}
		path, err := exec.LookPath(parts[0])
		if err != nil {
			continue
		}
		cmd := exec.Command(path, parts[1:]...)
		output, err := cmd.Output()
		if err != nil {
			continue
		}
		return string(output), nil
	}
	return "", os.ErrNotExist
}

func parseDMIMemoryInfo(content string) (string, *int, string, string) {
	memoryType := ""
	speeds := map[int]int{}
	manufacturers := map[string]int{}
	partNumbers := map[string]int{}
	inDevice := false
	for _, rawLine := range strings.Split(content, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "Memory Device" {
			inDevice = true
			continue
		}
		if line == "" {
			inDevice = false
			continue
		}
		if !inDevice {
			continue
		}
		switch {
		case strings.HasPrefix(line, "Type:"):
			value := strings.TrimSpace(strings.TrimPrefix(line, "Type:"))
			if value != "" && value != "Unknown" && value != "RAM" && memoryType == "" {
				memoryType = value
			}
		case strings.HasPrefix(line, "Manufacturer:"):
			addMemoryTextCount(manufacturers, strings.TrimSpace(strings.TrimPrefix(line, "Manufacturer:")))
		case strings.HasPrefix(line, "Part Number:"):
			addMemoryTextCount(partNumbers, strings.TrimSpace(strings.TrimPrefix(line, "Part Number:")))
		case strings.HasPrefix(line, "Configured Memory Speed:"):
			addMemorySpeedCount(speeds, strings.TrimSpace(strings.TrimPrefix(line, "Configured Memory Speed:")))
		case strings.HasPrefix(line, "Speed:"):
			addMemorySpeedCount(speeds, strings.TrimSpace(strings.TrimPrefix(line, "Speed:")))
		}
	}
	return memoryType, mostCommonMemorySpeed(speeds), mostCommonMemoryText(manufacturers), mostCommonMemoryText(partNumbers)
}

func addMemorySpeedCount(speeds map[int]int, raw string) {
	if raw == "" || raw == "Unknown" {
		return
	}
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return
	}
	value, err := strconv.Atoi(fields[0])
	if err != nil || value <= 0 {
		return
	}
	speeds[value]++
}

func addMemoryTextCount(values map[string]int, raw string) {
	value := strings.TrimSpace(raw)
	if value == "" || value == "Unknown" || value == "Not Specified" || value == "NO DIMM" {
		return
	}
	values[value]++
}

func mostCommonMemorySpeed(speeds map[int]int) *int {
	if len(speeds) == 0 {
		return nil
	}
	bestValue := 0
	bestCount := -1
	for value, count := range speeds {
		if count > bestCount || (count == bestCount && value > bestValue) {
			bestValue = value
			bestCount = count
		}
	}
	if bestValue <= 0 {
		return nil
	}
	result := bestValue
	return &result
}

func mostCommonMemoryText(values map[string]int) string {
	if len(values) == 0 {
		return ""
	}
	bestValue := ""
	bestCount := -1
	for value, count := range values {
		if count > bestCount || (count == bestCount && (bestValue == "" || value < bestValue)) {
			bestValue = value
			bestCount = count
		}
	}
	return bestValue
}

func readMemInfo() map[string]uint64 {
	result := map[string]uint64{}
	content, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return result
	}
	scanner := bufio.NewScanner(strings.NewReader(string(content)))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		key := strings.TrimSuffix(fields[0], ":")
		value, _ := strconv.ParseUint(fields[1], 10, 64)
		result[key] = value
	}
	return result
}

func readMemoryPressure() float64 {
	content, err := os.ReadFile("/proc/pressure/memory")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(content), "\n") {
		if !strings.HasPrefix(line, "some ") {
			continue
		}
		for _, field := range strings.Fields(line) {
			if strings.HasPrefix(field, "avg10=") {
				value, _ := strconv.ParseFloat(strings.TrimPrefix(field, "avg10="), 64)
				return value
			}
		}
	}
	return 0
}

func readNetworkSample() networkSample {
	content, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return networkSample{}
	}
	var sample networkSample
	for _, line := range strings.Split(string(content), "\n") {
		parts := strings.Split(line, ":")
		if len(parts) != 2 {
			continue
		}
		iface := strings.TrimSpace(parts[0])
		if iface == "lo" || strings.HasPrefix(iface, "docker") || strings.HasPrefix(iface, "veth") {
			continue
		}
		fields := strings.Fields(parts[1])
		if len(fields) < 16 {
			continue
		}
		rx, _ := strconv.ParseInt(fields[0], 10, 64)
		tx, _ := strconv.ParseInt(fields[8], 10, 64)
		sample.rx += rx
		sample.tx += tx
	}
	return sample
}

func readDiskIOSample() diskIOSample {
	content, err := os.ReadFile("/proc/diskstats")
	if err != nil {
		return diskIOSample{}
	}
	var sample diskIOSample
	for _, line := range strings.Split(string(content), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 10 {
			continue
		}
		name := fields[2]
		if !includeDiskStatsDevice(name) {
			continue
		}
		readSectors, err := strconv.ParseInt(fields[5], 10, 64)
		if err != nil {
			continue
		}
		writeSectors, err := strconv.ParseInt(fields[9], 10, 64)
		if err != nil {
			continue
		}
		sample.readBytes += readSectors * 512
		sample.writeBytes += writeSectors * 512
	}
	return sample
}

func includeDiskStatsDevice(name string) bool {
	switch {
	case strings.HasPrefix(name, "loop"),
		strings.HasPrefix(name, "ram"),
		strings.HasPrefix(name, "zram"),
		strings.HasPrefix(name, "fd"):
		return false
	case strings.HasPrefix(name, "nvme") && strings.Contains(name, "p"):
		return false
	case strings.HasPrefix(name, "mmcblk") && strings.Contains(name, "p"):
		return false
	}
	last := name[len(name)-1]
	if last >= '0' && last <= '9' &&
		(strings.HasPrefix(name, "sd") || strings.HasPrefix(name, "vd") || strings.HasPrefix(name, "xvd")) {
		return false
	}
	return true
}

func buildNetworkStatus(before, after networkSample, interval time.Duration) NetworkStatus {
	seconds := interval.Seconds()
	if seconds <= 0 {
		seconds = 1
	}
	download := int64(float64(after.rx-before.rx) / seconds)
	upload := int64(float64(after.tx-before.tx) / seconds)
	if download < 0 {
		download = 0
	}
	if upload < 0 {
		upload = 0
	}
	return NetworkStatus{
		DownloadBytesPerSec: download,
		UploadBytesPerSec:   upload,
	}
}

func buildDiskIOStatus(before, after diskIOSample, interval time.Duration) DiskIOStatus {
	seconds := interval.Seconds()
	if seconds <= 0 {
		seconds = 1
	}
	read := int64(float64(after.readBytes-before.readBytes) / seconds)
	write := int64(float64(after.writeBytes-before.writeBytes) / seconds)
	if read < 0 {
		read = 0
	}
	if write < 0 {
		write = 0
	}
	return DiskIOStatus{
		ReadBytesPerSec:  read,
		WriteBytesPerSec: write,
	}
}

func collectFileSharingStatus() FileSharingStatus {
	services := FileSharingServiceCounts{}
	for _, entry := range readEstablishedTCPPorts("/proc/net/tcp") {
		addSharingPort(&services, entry)
	}
	for _, entry := range readEstablishedTCPPorts("/proc/net/tcp6") {
		addSharingPort(&services, entry)
	}
	total := services.SMB + services.WebDAV + services.NFS
	return FileSharingStatus{
		OnlineUsers: total,
		Services:    services,
	}
}

func addSharingPort(counts *FileSharingServiceCounts, port int) {
	switch port {
	case 139, 445:
		counts.SMB++
	case 80, 443, 5005, 5006, 8080:
		counts.WebDAV++
	case 2049:
		counts.NFS++
	}
}

func readEstablishedTCPPorts(path string) []int {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	seen := map[string]struct{}{}
	var ports []int
	for _, line := range strings.Split(string(content), "\n")[1:] {
		fields := strings.Fields(line)
		if len(fields) < 4 || fields[3] != "01" {
			continue
		}
		local := fields[1]
		remote := fields[2]
		localParts := strings.Split(local, ":")
		if len(localParts) != 2 {
			continue
		}
		port64, err := strconv.ParseInt(localParts[1], 16, 32)
		if err != nil {
			continue
		}
		key := strconv.FormatInt(port64, 10) + "/" + remote
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		ports = append(ports, int(port64))
	}
	return ports
}

func collectGPUStatus(ctx context.Context) *GPUStatus {
	path, err := exec.LookPath("nvidia-smi")
	if err != nil {
		return nil
	}
	cmd := exec.CommandContext(ctx, path, "--query-gpu=name,temperature.gpu,utilization.gpu,memory.used,memory.total,power.draw", "--format=csv,noheader,nounits")
	output, err := cmd.Output()
	if err != nil {
		return nil
	}
	line := strings.TrimSpace(strings.Split(string(output), "\n")[0])
	fields := splitCSVLine(line)
	if len(fields) < 6 {
		return nil
	}
	temp := parseOptionalFloat(fields[1])
	usage := parseOptionalFloat(fields[2])
	memUsedMiB := parseFloatDefault(fields[3], 0)
	memTotalMiB := parseFloatDefault(fields[4], 0)
	power := parseOptionalFloat(fields[5])
	status := &GPUStatus{
		Name:             strings.TrimSpace(fields[0]),
		UsagePercent:     usage,
		TemperatureC:     temp,
		MemoryUsedBytes:  uint64(memUsedMiB * 1024 * 1024),
		MemoryTotalBytes: uint64(memTotalMiB * 1024 * 1024),
		PowerW:           power,
	}
	return status
}

func readFirstFloatFromGlob(patterns []string, divisor float64) *float64 {
	for _, pattern := range patterns {
		matches, _ := filepath.Glob(pattern)
		for _, path := range matches {
			content, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			value, err := strconv.ParseFloat(strings.TrimSpace(string(content)), 64)
			if err != nil || value <= 0 {
				continue
			}
			if divisor > 0 {
				value = value / divisor
			}
			value = round1(value)
			return &value
		}
	}
	return nil
}

func splitCSVLine(line string) []string {
	parts := strings.Split(line, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func parseOptionalFloat(value string) *float64 {
	if strings.TrimSpace(value) == "" || strings.EqualFold(strings.TrimSpace(value), "[not supported]") {
		return nil
	}
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		return nil
	}
	parsed = round1(parsed)
	return &parsed
}

func parseFloatDefault(value string, fallback float64) float64 {
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		return fallback
	}
	return parsed
}

func percentage(used uint64, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return round1(float64(used) * 100 / float64(total))
}

func round1(value float64) float64 {
	return math.Round(value*10) / 10
}
