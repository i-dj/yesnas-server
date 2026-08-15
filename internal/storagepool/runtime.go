package storagepool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"nas-server/internal/storage"
	commandrunner "nas-server/pkg/shell"
)

func BuildResponses(ctx context.Context, pools []StoragePool) []Response {
	items := make([]Response, 0, len(pools))
	for _, pool := range pools {
		if isCloudPoolRecord(pool) {
			continue
		}
		items = append(items, buildResponse(ctx, pool))
	}
	return items
}

func buildResponse(ctx context.Context, pool StoragePool) Response {
	now := time.Now().UTC()
	resp := Response{StoragePool: pool, Kind: "local", Provider: "btrfs", Status: string(storage.StatusOffline), Health: "offline", Snapshots: []Snapshot{}, Warnings: []string{}, LastCheckedAt: now}
	mounted := isMountpointActive(pool.MountPath)
	usagePath := pool.DataPath
	if strings.TrimSpace(usagePath) == "" {
		usagePath = pool.MountPath
	}
	resp.Devices = enrichPoolDevices(ctx, pool, resp.Devices, mounted)
	resp.Mounted = mounted
	if mounted {
		resp.Status = string(storage.StatusOnline)
		resp.Health = "healthy"
	}
	applyPoolDeviceStatus(&resp, pool)
	logicalTotal, logicalUsed, logicalFree, statErr := statFilesystemUsage(usagePath)
	if statErr != nil && strings.TrimSpace(usagePath) != strings.TrimSpace(pool.MountPath) {
		logicalTotal, logicalUsed, logicalFree, statErr = statFilesystemUsage(pool.MountPath)
	}
	if usage, warnings := readBtrfsUsage(ctx, pool.MountPath); usage != nil {
		resp.FilesystemUUID = usage.FilesystemUUID
		resp.DataProfile = usage.DataProfile
		resp.MetadataProfile = usage.MetadataProfile
		resp.SystemProfile = usage.SystemProfile
		resp.Warnings = append(resp.Warnings, warnings...)
		if statErr == nil {
			if usage.HasDataUsedBytes {
				resp.UsedBytes = usage.DataUsedBytes
			} else {
				resp.UsedBytes = logicalUsed
			}
			if usage.EstimatedFreeBytes > 0 {
				resp.FreeBytes = usage.EstimatedFreeBytes
			} else {
				resp.FreeBytes = logicalFree
			}
			resp.TotalBytes = resp.UsedBytes + resp.FreeBytes
			resp.UsagePercent = calculateUsagePercent(resp.UsedBytes, resp.TotalBytes)
		} else if statErr != nil && mounted {
			resp.Warnings = append(resp.Warnings, "failed to read logical filesystem usage: "+statErr.Error())
		}
	} else {
		if statErr == nil {
			resp.TotalBytes = logicalTotal
			resp.FreeBytes = logicalFree
			resp.UsedBytes = logicalUsed
			resp.UsagePercent = calculateUsagePercent(resp.UsedBytes, logicalTotal)
		} else if mounted {
			resp.Warnings = append(resp.Warnings, "failed to read filesystem usage: "+statErr.Error())
		}
	}
	snapshots, warnings := readBtrfsSnapshots(ctx, pool.MountPath)
	resp.Snapshots = mergePoolSnapshots(pool.ID, pool.MountPath, snapshots)
	resp.Warnings = append(resp.Warnings, fillSnapshotSizes(ctx, pool.MountPath, resp.Snapshots)...)
	resp.SnapshotCount = len(resp.Snapshots)
	resp.Warnings = append(resp.Warnings, warnings...)
	if len(resp.Warnings) > 0 && mounted {
		resp.Health = "warning"
	}
	if !mounted {
		resp.Status = string(storage.StatusOffline)
		resp.Health = "offline"
	}
	return resp
}

func BuildCloudResponses(ctx context.Context, items []storage.Storage) []Response {
	responses := make([]Response, 0, len(items))
	for _, item := range items {
		if !isCloudStorage(item) && !isNetworkStorage(item) {
			continue
		}
		warnings := []string{}
		cloudPool := cloudStoragePool(item)
		if err := UpsertCloudPoolRecord(cloudPool); err != nil {
			warnings = append(warnings, "failed to save cloud storage pool record: "+err.Error())
		} else if savedPool, err := Get(item.ID); err == nil {
			cloudPool = *savedPool
		}
		if cloudPool.Devices == nil {
			cloudPool.Devices = []PoolDevice{}
		}
		response := buildCloudResponse(item)
		response.StoragePool = cloudPool
		response.Warnings = append(response.Warnings, warnings...)
		responses = append(responses, response)
	}
	return responses
}

func isCloudStorage(item storage.Storage) bool {
	if item.Type == storage.Cloud {
		return true
	}
	return isCloudProvider(item.Provider)
}

func isNetworkStorage(item storage.Storage) bool {
	if storage.IsNetworkProvider(item.Provider) {
		return true
	}
	switch item.Type {
	case storage.FTP, storage.WebDAV, storage.SMB, storage.NFS:
		return true
	default:
		return false
	}
}

func isCloudPoolRecord(pool StoragePool) bool {
	return isCloudProvider(pool.Filesystem) || storage.IsNetworkProvider(pool.Filesystem)
}

func isCloudProvider(provider string) bool {
	switch strings.TrimSpace(provider) {
	case string(storage.ProviderGoogleDrive), string(storage.ProviderOneDrive), string(storage.ProviderDropbox):
		return true
	default:
		return false
	}
}

func refreshCloudUsage(ctx context.Context, item *storage.Storage) []string {
	if item == nil {
		return nil
	}
	if isCloudProvider(item.Provider) {
		if _, err := storage.RefreshGoogleDriveUsage(ctx, item); err != nil {
			return []string{"failed to read cloud storage usage: " + err.Error()}
		}
	}
	return nil
}

func buildCloudResponse(item storage.Storage) Response {
	now := time.Now().UTC()
	mounted := item.Status == storage.StatusOnline
	if strings.HasPrefix(strings.TrimSpace(item.MountPath), "/") {
		mounted = isMountpointActive(item.MountPath)
	}
	status := string(item.Status)
	health := deriveCloudHealth(item.Status)
	if mounted {
		status = string(storage.StatusOnline)
		health = "healthy"
	} else if item.Status == storage.StatusOnline {
		status = string(storage.StatusOnline)
		health = "warning"
	} else if item.Status == storage.StatusError {
		status = string(storage.StatusError)
		health = "error"
	} else {
		status = string(storage.StatusOffline)
		health = "offline"
	}

	warnings := []string{}
	if item.Status == storage.StatusOnline && !mounted {
		if isNetworkStorage(item) {
			warnings = append(warnings, "network storage is online, but the local mount is not active")
		} else {
			warnings = append(warnings, "cloud storage is online, but the local mount is not active")
		}
	}

	kind := "cloud"
	if isNetworkStorage(item) {
		kind = "network"
	}
	resp := Response{
		StoragePool:   cloudStoragePool(item),
		Kind:          kind,
		Provider:      item.Provider,
		RootPath:      item.RootPath,
		AccountEmail:  item.Username,
		Status:        status,
		Health:        health,
		Mounted:       mounted,
		TotalBytes:    uint64(clampNonNegativeInt64(item.TotalSize)),
		FreeBytes:     uint64(clampNonNegativeInt64(item.FreeSize)),
		UsedBytes:     uint64(clampNonNegativeInt64(item.TotalSize - item.FreeSize)),
		UsagePercent:  calculateUsagePercent(uint64(clampNonNegativeInt64(item.TotalSize-item.FreeSize)), uint64(clampNonNegativeInt64(item.TotalSize))),
		SnapshotCount: 0,
		Snapshots:     []Snapshot{},
		Warnings:      warnings,
		LastCheckedAt: now,
	}
	return resp
}

func cloudStoragePool(item storage.Storage) StoragePool {
	return StoragePool{
		ID:         item.ID,
		StorageID:  item.ID,
		Name:       item.Name,
		Filesystem: string(item.Provider),
		RaidLevel:  "single",
		MountPath:  item.MountPath,
		DataPath:   item.MountPath,
		Devices:    []PoolDevice{},
		CreatedAt:  parseStorageTime(item.UpdatedAt),
	}
}

func deriveCloudHealth(status storage.Status) string {
	switch status {
	case storage.StatusOnline:
		return "healthy"
	case storage.StatusError:
		return "error"
	default:
		return "offline"
	}
}

func parseStorageTime(value string) time.Time {
	if strings.TrimSpace(value) == "" {
		return time.Time{}
	}
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05Z07:00",
	}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

func clampNonNegativeInt64(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

func enrichPoolDevices(ctx context.Context, pool StoragePool, devices []PoolDevice, poolMounted bool) []PoolDevice {
	for i := range devices {
		devices[i].Health = readPoolDeviceHealth(ctx, devices[i].DevicePath)
		devices[i].State = derivePoolDeviceState(devices[i], poolMounted)
	}
	return devices
}

func applyPoolDeviceStatus(resp *Response, pool StoragePool) {
	if resp == nil {
		return
	}
	raidLevel := strings.ToLower(strings.TrimSpace(pool.RaidLevel))
	if raidLevel == "single" {
		for _, device := range resp.Devices {
			if device.State == "FAILED" {
				resp.Health = "failed"
				return
			}
			if device.State == "OFFLINE" || device.State == "UNKNOWN" {
				resp.Status = string(storage.StatusOffline)
				resp.Health = "offline"
				return
			}
		}
		return
	}

	hasOffline := false
	hasFailed := false
	for _, device := range resp.Devices {
		switch device.State {
		case "FAILED":
			hasFailed = true
		case "OFFLINE", "UNKNOWN":
			hasOffline = true
		}
	}

	if hasFailed {
		resp.Health = "degraded"
		return
	}
	if hasOffline {
		if resp.Mounted {
			resp.Status = "degraded"
			resp.Health = "degraded"
		} else {
			resp.Status = string(storage.StatusOffline)
			resp.Health = "offline"
		}
	}
}

func derivePoolDeviceState(device PoolDevice, poolMounted bool) string {
	devicePath := strings.TrimSpace(device.DevicePath)
	if devicePath == "" {
		return "UNKNOWN"
	}
	if _, err := os.Stat(devicePath); err != nil {
		if os.IsNotExist(err) {
			return "OFFLINE"
		}
		return "UNKNOWN"
	}
	if device.Health == "failed" {
		return "FAILED"
	}
	if !poolMounted {
		return "OFFLINE"
	}
	return "ONLINE"
}

func readPoolDeviceHealth(ctx context.Context, devicePath string) string {
	if strings.TrimSpace(devicePath) == "" {
		return "unknown"
	}
	result, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "smartctl", "-j", "-H", devicePath)
	if err != nil {
		return "unknown"
	}
	var payload struct {
		SmartStatus struct {
			Passed bool `json:"passed"`
		} `json:"smart_status"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &payload); err != nil {
		return "unknown"
	}
	if payload.SmartStatus.Passed {
		return "passed"
	}
	return "failed"
}

func readBtrfsUsage(ctx context.Context, mountPath string) (*BtrfsUsage, []string) {
	if !isMountpointActive(mountPath) {
		return nil, nil
	}
	result, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "btrfs", "filesystem", "usage", "-b", mountPath)
	if err != nil {
		return nil, []string{"failed to read btrfs usage: " + err.Error()}
	}
	usage := &BtrfsUsage{}
	warnings := make([]string, 0)
	for _, line := range strings.Split(result.Stdout, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "UUID:"):
			usage.FilesystemUUID = strings.TrimSpace(strings.TrimPrefix(line, "UUID:"))
		case strings.HasPrefix(line, "Device size:"):
			usage.DeviceSizeBytes = parseFirstUint(strings.TrimSpace(strings.TrimPrefix(line, "Device size:")))
		case strings.HasPrefix(line, "Used:"):
			usage.PhysicalUsedBytes = parseFirstUint(strings.TrimSpace(strings.TrimPrefix(line, "Used:")))
		case strings.HasPrefix(line, "Free (estimated):"):
			usage.EstimatedFreeBytes = parseFirstUint(strings.TrimSpace(strings.TrimPrefix(line, "Free (estimated):")))
		case strings.HasPrefix(line, "Data,"):
			usage.DataProfile = parseProfileFromUsageLine(line)
		case strings.HasPrefix(line, "Metadata,"):
			usage.MetadataProfile = parseProfileFromUsageLine(line)
		case strings.HasPrefix(line, "System,"):
			usage.SystemProfile = parseProfileFromUsageLine(line)
		}
	}
	dfResult, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "btrfs", "filesystem", "df", "-b", mountPath)
	if err != nil {
		warnings = append(warnings, "failed to read btrfs data usage: "+err.Error())
		return usage, warnings
	}
	for _, line := range strings.Split(dfResult.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "Data,") {
			continue
		}
		if usage.DataProfile == "" {
			usage.DataProfile = parseProfileFromDFLine(line)
		}
		usage.HasDataUsedBytes = true
		usage.DataUsedBytes += parseUsedValueFromDFLine(line)
	}
	return usage, warnings
}

func readBtrfsSnapshots(ctx context.Context, mountPath string) ([]Snapshot, []string) {
	items, warnings := readBtrfsSubvolumesWithArgs(ctx, mountPath, "-s")
	filtered := make([]Snapshot, 0, len(items))
	for _, item := range items {
		normalizedPath := normalizeSnapshotPath(mountPath, item.Path)
		if !isManagedSnapshotPath(normalizedPath) {
			continue
		}
		item.Path = normalizedPath
		item.Name = filepath.Base(normalizedPath)
		filtered = append(filtered, item)
	}
	return filtered, warnings
}

func readBtrfsSubvolumes(ctx context.Context, mountPath string) ([]Snapshot, []string) {
	return readBtrfsSubvolumesWithArgs(ctx, mountPath)
}

func readBtrfsSubvolumesWithArgs(ctx context.Context, mountPath string, extraArgs ...string) ([]Snapshot, []string) {
	if !isMountpointActive(mountPath) {
		return []Snapshot{}, nil
	}
	args := []string{"subvolume", "list"}
	args = append(args, extraArgs...)
	args = append(args, mountPath)
	result, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "btrfs", args...)
	if err != nil {
		return []Snapshot{}, []string{"failed to read btrfs subvolumes: " + err.Error()}
	}
	items := make([]Snapshot, 0)
	for _, line := range strings.Split(strings.TrimSpace(result.Stdout), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		items = append(items, parseSnapshotLine(line))
	}
	return items, nil
}

func mergePoolSnapshots(poolID string, mountPath string, systemSnapshots []Snapshot) []Snapshot {
	metadata, err := ListSnapshotRecords(poolID)
	if err != nil {
		return systemSnapshots
	}
	byPath := make(map[string]SnapshotRecord, len(metadata))
	for _, item := range metadata {
		normalizedPath := normalizeSnapshotPath(mountPath, item.Path)
		item.Path = normalizedPath
		byPath[normalizedPath] = item
	}
	merged := make([]Snapshot, 0, len(systemSnapshots))
	seen := make(map[string]struct{}, len(systemSnapshots))
	for _, snapshot := range systemSnapshots {
		snapshot.Path = normalizeSnapshotPath(mountPath, snapshot.Path)
		snapshot.Name = filepath.Base(snapshot.Path)
		if meta, ok := byPath[snapshot.Path]; ok {
			snapshot.MetadataID = meta.ID
			snapshot.SourcePath = meta.SourcePath
			snapshot.Name = meta.Name
			snapshot.Description = meta.Description
			snapshot.CreatedBy = meta.CreatedBy
			createdAt := meta.CreatedAt
			snapshot.CreatedAt = &createdAt
			snapshot.UpdatedAt = meta.UpdatedAt
			snapshot.IsReadOnly = meta.IsReadOnly
			snapshot.Registered = true
			if meta.SystemSnapshotID == 0 || meta.SystemGeneration == 0 {
				_ = UpdateSnapshotSystemFields(meta.ID, snapshot.SystemID, snapshot.Gen)
			}
		}
		merged = append(merged, snapshot)
		seen[snapshot.Path] = struct{}{}
	}
	for _, meta := range metadata {
		normalizedPath := normalizeSnapshotPath(mountPath, meta.Path)
		if _, ok := seen[normalizedPath]; ok {
			continue
		}
		createdAt := meta.CreatedAt
		merged = append(merged, Snapshot{
			MetadataID:  meta.ID,
			SystemID:    meta.SystemSnapshotID,
			Gen:         meta.SystemGeneration,
			Path:        normalizedPath,
			Name:        meta.Name,
			SourcePath:  meta.SourcePath,
			Description: meta.Description,
			CreatedBy:   meta.CreatedBy,
			CreatedAt:   &createdAt,
			UpdatedAt:   meta.UpdatedAt,
			IsReadOnly:  meta.IsReadOnly,
			Registered:  true,
		})
	}
	return merged
}

func fillSnapshotSizes(ctx context.Context, mountPath string, snapshots []Snapshot) []string {
	warnings := make([]string, 0)
	for i := range snapshots {
		targetPath := snapshots[i].Path
		if targetPath == "" || targetPath == "." {
			continue
		}
		absolutePath := filepath.Join(mountPath, filepath.FromSlash(targetPath))
		result, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "btrfs", "filesystem", "du", "-s", "--raw", absolutePath)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("failed to read snapshot size for %s: %v", snapshots[i].Name, err))
			continue
		}
		snapshots[i].SizeBytes = parseSnapshotSizeBytes(result.Stdout)
	}
	return warnings
}

func parseSnapshotSizeBytes(output string) uint64 {
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(strings.ToLower(line), "total") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if value, err := strconv.ParseUint(fields[0], 10, 64); err == nil {
			return value
		}
	}
	return 0
}

func normalizeSnapshotPath(mountPath string, path string) string {
	cleanPath := filepath.Clean(strings.TrimSpace(path))
	cleanMountPath := filepath.Clean(strings.TrimSpace(mountPath))
	if cleanPath == "." || cleanPath == "" {
		return path
	}
	if cleanMountPath != "." && cleanMountPath != "" {
		if cleanPath == cleanMountPath {
			return "."
		}
		if rel, err := filepath.Rel(cleanMountPath, cleanPath); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.ToSlash(cleanPath)
}

func isManagedSnapshotPath(path string) bool {
	clean := filepath.ToSlash(filepath.Clean(strings.TrimSpace(path)))
	if clean == "" || clean == "." {
		return false
	}
	return clean == ".snapshots" || strings.HasPrefix(clean, ".snapshots/")
}

func sortSubvolumesDeepestFirst(items []Snapshot) {
	sort.Slice(items, func(i, j int) bool {
		leftDepth := strings.Count(filepath.Clean(items[i].Path), string(os.PathSeparator))
		rightDepth := strings.Count(filepath.Clean(items[j].Path), string(os.PathSeparator))
		if leftDepth == rightDepth {
			return items[i].Path > items[j].Path
		}
		return leftDepth > rightDepth
	})
}

func parseSnapshotLine(line string) Snapshot {
	snapshot := Snapshot{Path: line, Name: filepath.Base(line)}
	fields := strings.Fields(line)
	for i := 0; i < len(fields); i++ {
		switch fields[i] {
		case "ID":
			if i+1 < len(fields) {
				snapshot.SystemID = parseFirstUint(fields[i+1])
			}
		case "gen":
			if i+1 < len(fields) {
				snapshot.Gen = parseFirstUint(fields[i+1])
			}
		case "top", "top_level":
			if i+1 < len(fields) {
				snapshot.TopLevel = parseFirstUint(fields[i+1])
			}
		case "path":
			if i+1 < len(fields) {
				snapshot.Path = strings.Join(fields[i+1:], " ")
				snapshot.Name = filepath.Base(snapshot.Path)
				return snapshot
			}
		}
	}
	return snapshot
}

func parseProfileFromUsageLine(line string) string {
	prefix, _, found := strings.Cut(line, ":")
	if !found {
		return ""
	}
	parts := strings.Split(prefix, ",")
	if len(parts) < 2 {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

func parseProfileFromDFLine(line string) string {
	prefix, _, found := strings.Cut(line, ":")
	if !found {
		return ""
	}
	parts := strings.Split(prefix, ",")
	if len(parts) < 2 {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

func parseFirstUint(value string) uint64 {
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return 0
	}
	number, _ := strconv.ParseUint(strings.TrimSpace(fields[0]), 10, 64)
	return number
}

func parseUsedValueFromDFLine(line string) uint64 {
	_, rest, found := strings.Cut(line, ":")
	if !found {
		return 0
	}
	for _, segment := range strings.Split(rest, ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(segment), "=")
		if !ok || strings.TrimSpace(key) != "used" {
			continue
		}
		return parseBtrfsSizeValue(strings.TrimSpace(value))
	}
	return 0
}

func parseBtrfsSizeValue(value string) uint64 {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if number, err := strconv.ParseUint(value, 10, 64); err == nil {
		return number
	}
	var numeric float64
	var unit string
	if _, err := fmt.Sscanf(value, "%f%s", &numeric, &unit); err != nil {
		return 0
	}
	multiplier := float64(1)
	switch strings.ToUpper(strings.TrimSpace(unit)) {
	case "KIB":
		multiplier = 1024
	case "MIB":
		multiplier = 1024 * 1024
	case "GIB":
		multiplier = 1024 * 1024 * 1024
	case "TIB":
		multiplier = 1024 * 1024 * 1024 * 1024
	case "PIB":
		multiplier = 1024 * 1024 * 1024 * 1024 * 1024
	}
	return uint64(numeric * multiplier)
}

func statFilesystemUsage(path string) (uint64, uint64, uint64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, 0, 0, err
	}
	total := uint64(stat.Blocks) * uint64(stat.Bsize)
	used := uint64(stat.Blocks-stat.Bfree) * uint64(stat.Bsize)
	free := uint64(stat.Bavail) * uint64(stat.Bsize)
	return total, used, free, nil
}

func statFilesystem(path string) (int64, int64) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, 0
	}
	total := int64(stat.Blocks) * int64(stat.Bsize)
	free := int64(stat.Bavail) * int64(stat.Bsize)
	return total, free
}

func isMountpointActive(mountPath string) bool {
	if mountPath == "" {
		return false
	}
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == mountPath {
			return true
		}
	}
	return false
}

func calculateUsagePercent(used uint64, total uint64) int {
	if total == 0 {
		return 0
	}
	return int((used*100 + total/2) / total)
}

func formatBytesIEC(value uint64) string {
	const unit = 1024
	if value < unit {
		return fmt.Sprintf("%d B", value)
	}
	div, exp := uint64(unit), 0
	for n := value / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(value)/float64(div), "KMGTPE"[exp])
}

func formatBytesRate(value float64) string {
	if value <= 0 {
		return ""
	}
	const unit = 1024.0
	if value < unit {
		return fmt.Sprintf("%.0f B/s", value)
	}
	units := []string{"KiB/s", "MiB/s", "GiB/s", "TiB/s"}
	current := value / unit
	for i, label := range units {
		if current < unit || i == len(units)-1 {
			return fmt.Sprintf("%.2f %s", current, label)
		}
		current /= unit
	}
	return fmt.Sprintf("%.2f TiB/s", current)
}
