//go:build linux

package linuxplatform

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"nas-server/internal/storagepool"
	pkgdisks "nas-server/pkg/disks"
	commandrunner "nas-server/pkg/shell"
)

const defaultSectorSize = 512

var diskStatsCache = struct {
	sync.Mutex
	byPath map[string]diskRateSnapshot
}{
	byPath: make(map[string]diskRateSnapshot),
}

var diskRateSampler sync.Once

var diskRateCache = struct {
	sync.RWMutex
	byKernelName map[string]diskRateValue
}{
	byKernelName: make(map[string]diskRateValue),
}

type diskRateSnapshot struct {
	readBytes  uint64
	writeBytes uint64
	readOps    uint64
	writeOps   uint64
	sampledAt  time.Time
}

type diskRateValue struct {
	readBytesPerSec  float64
	writeBytesPerSec float64
	readOpsPerSec    float64
	writeOpsPerSec   float64
	sampledAt        time.Time
}

type blockStat struct {
	readOps    uint64
	readBytes  uint64
	writeOps   uint64
	writeBytes uint64
}

type smartctlResponse struct {
	ModelName                     string              `json:"model_name"`
	ModelFamily                   string              `json:"model_family"`
	SerialNumber                  string              `json:"serial_number"`
	RotationRate                  int                 `json:"rotation_rate"`
	Temperature                   smartTemperature    `json:"temperature"`
	SmartStatus                   smartStatus         `json:"smart_status"`
	PowerOnTime                   smartPowerOnTime    `json:"power_on_time"`
	PowerCycleCount               uint64              `json:"power_cycle_count"`
	ATASmartAttributes            ataSmartAttributes  `json:"ata_smart_attributes"`
	NVMeSmartHealthInformationLog nvmeSmartHealthInfo `json:"nvme_smart_health_information_log"`
	Messages                      []smartMessage      `json:"messages"`
}

type smartTemperature struct {
	Current int `json:"current"`
}

type smartStatus struct {
	Passed bool `json:"passed"`
}

type smartPowerOnTime struct {
	Hours uint64 `json:"hours"`
}

type ataSmartAttributes struct {
	Table []ataSmartAttribute `json:"table"`
}

type ataSmartAttribute struct {
	Name  string          `json:"name"`
	Value int             `json:"value"`
	Raw   ataSmartRawData `json:"raw"`
}

type ataSmartRawData struct {
	Value uint64 `json:"value"`
}

type nvmeSmartHealthInfo struct {
	Temperature     int    `json:"temperature"`
	PercentageUsed  uint64 `json:"percentage_used"`
	UnsafeShutdowns uint64 `json:"unsafe_shutdowns"`
	PowerCycles     uint64 `json:"power_cycles"`
}

type smartMessage struct {
	String string `json:"string"`
}

func ListDisks(ctx context.Context) (pkgdisks.DiskList, error) {
	startDiskRateSampler()

	storagePools, err := storagepool.List()
	if err != nil {
		return pkgdisks.DiskList{}, fmt.Errorf("load storage pools: %w", err)
	}
	poolUsageByDevice := buildPoolUsageMap(storagePools)

	result, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "lsblk",
		"-J",
		"-o", "NAME,KNAME,PATH,TYPE,SIZE,FSTYPE,LABEL,UUID,MODEL,SERIAL,VENDOR,TRAN,MOUNTPOINT,MOUNTPOINTS,RM,HOTPLUG,RO")
	if err != nil {
		return pkgdisks.DiskList{}, fmt.Errorf("run lsblk: %w", err)
	}

	var payload lsblkResponse
	if err := json.Unmarshal([]byte(result.Stdout), &payload); err != nil {
		return pkgdisks.DiskList{}, fmt.Errorf("parse lsblk output: %w", err)
	}

	items := make([]pkgdisks.DiskInfo, 0)
	for _, device := range payload.Blockdevices {
		if shouldIgnoreBlockDevice(device) || device.Type != "disk" {
			continue
		}
		info := buildDiskInfo(ctx, device, poolUsageByDevice)
		items = append(items, info)
	}
	slices.SortFunc(items, func(a, b pkgdisks.DiskInfo) int {
		return strings.Compare(a.Path, b.Path)
	})

	return pkgdisks.DiskList{
		Items:       items,
		GeneratedAt: time.Now().UTC(),
	}, nil
}

func buildDiskInfo(ctx context.Context, device lsblkDevice, poolUsageByDevice map[string][]pkgdisks.DiskUsage) pkgdisks.DiskInfo {
	sampledAt := time.Now().UTC()
	info := pkgdisks.DiskInfo{
		Path:        device.Path,
		Name:        device.Name,
		KernelName:  device.KName,
		Size:        device.Size,
		Model:       strings.TrimSpace(device.Model),
		Serial:      strings.TrimSpace(device.Serial),
		Vendor:      strings.TrimSpace(device.Vendor),
		Transport:   strings.TrimSpace(device.Tran),
		FsType:      strings.TrimSpace(device.FsType),
		Label:       strings.TrimSpace(device.Label),
		UUID:        strings.TrimSpace(device.UUID),
		Mountpoints: normalizedMountpoints(device),
		Removable:   device.RM,
		Hotplug:     device.Hotplug,
		ReadOnly:    device.RO,
		HasChildren: len(device.Children) > 0,
		SampledAt:   sampledAt,
	}

	if stats, err := readBlockStats(device.KName); err == nil {
		info.ReadBytesTotal = stats.readBytes
		info.WriteBytesTotal = stats.writeBytes
		info.ReadOpsTotal = stats.readOps
		info.WriteOpsTotal = stats.writeOps
	} else {
		info.Warnings = append(info.Warnings, "Failed to read block stats: "+err.Error())
	}

	if sizeBytes, err := readBlockDeviceSizeBytes(device.KName); err == nil {
		info.SizeBytes = sizeBytes
	} else {
		info.Warnings = append(info.Warnings, "Failed to read block size: "+err.Error())
	}

	applyBackgroundRates(&info)

	if temp, err := readHWMonTemperature(device.KName); err == nil {
		info.TemperatureC = temp
	}

	if smart, warnings := readSMART(ctx, device.Path); smart != nil {
		applySMART(&info, *smart)
		info.Warnings = append(info.Warnings, warnings...)
	} else if len(warnings) > 0 {
		info.Warnings = append(info.Warnings, warnings...)
	}

	info.Partitions = buildDiskPartitions(device, poolUsageByDevice)
	info.Usages = buildDiskUsages(device, info.Partitions, poolUsageByDevice)
	info.Usage = summarizeUsage(info.Usages)
	info.InUse = len(info.Usages) > 0

	return info
}

func buildDiskPartitions(device lsblkDevice, poolUsageByDevice map[string][]pkgdisks.DiskUsage) []pkgdisks.DiskPartitionInfo {
	partitions := childPartitions(device.Children)
	items := make([]pkgdisks.DiskPartitionInfo, 0, len(partitions))
	for _, partition := range partitions {
		sizeBytes, err := readBlockDeviceSizeBytes(partition.KName)
		if err != nil {
			sizeBytes = 0
		}

		isSystemPartition := deviceIsSystemDisk(partition)
		usages := buildPartitionUsages(partition, poolUsageByDevice)
		items = append(items, pkgdisks.DiskPartitionInfo{
			Path:              partition.Path,
			Name:              partition.Name,
			KernelName:        partition.KName,
			ParentPath:        device.Path,
			Size:              partition.Size,
			SizeBytes:         sizeBytes,
			FsType:            strings.TrimSpace(partition.FsType),
			Label:             strings.TrimSpace(partition.Label),
			UUID:              strings.TrimSpace(partition.UUID),
			Mountpoints:       normalizedMountpoints(partition),
			ReadOnly:          partition.RO,
			HasChildren:       len(partition.Children) > 0,
			Usage:             summarizeUsage(usages),
			Usages:            usages,
			InUse:             len(usages) > 0,
			IsSystemPartition: isSystemPartition,
			IsRaidPartition:   false,
		})
	}
	return items
}

func buildPoolUsageMap(pools []storagepool.StoragePool) map[string][]pkgdisks.DiskUsage {
	result := make(map[string][]pkgdisks.DiskUsage)
	for _, pool := range pools {
		mountpoint := pool.MountPath
		if pool.DataPath != "" {
			mountpoint = pool.DataPath
		}
		for _, device := range pool.Devices {
			result[device.DevicePath] = append(result[device.DevicePath], pkgdisks.DiskUsage{
				Type:            "storage_pool",
				Label:           "storage-pool",
				StoragePoolID:   pool.ID,
				StoragePoolName: pool.Name,
				StorageID:       pool.StorageID,
				DeviceRole:      device.DeviceRole,
				Mountpoint:      mountpoint,
			})
		}
	}
	return result
}

func buildPartitionUsages(device lsblkDevice, poolUsageByDevice map[string][]pkgdisks.DiskUsage) []pkgdisks.DiskUsage {
	usages := make([]pkgdisks.DiskUsage, 0)
	for _, mount := range normalizedMountpoints(device) {
		switch mount {
		case "/":
			usages = append(usages, pkgdisks.DiskUsage{Type: "system", Label: "system-root", Mountpoint: mount})
		case "/boot":
			usages = append(usages, pkgdisks.DiskUsage{Type: "system", Label: "system-boot", Mountpoint: mount})
		case "/boot/efi":
			usages = append(usages, pkgdisks.DiskUsage{Type: "system", Label: "system-efi", Mountpoint: mount})
		}
	}
	usages = append(usages, poolUsageByDevice[device.Path]...)
	return dedupeUsages(usages)
}

func buildDiskUsages(device lsblkDevice, partitions []pkgdisks.DiskPartitionInfo, poolUsageByDevice map[string][]pkgdisks.DiskUsage) []pkgdisks.DiskUsage {
	usages := make([]pkgdisks.DiskUsage, 0)
	usages = append(usages, poolUsageByDevice[device.Path]...)
	for _, mount := range normalizedMountpoints(device) {
		switch mount {
		case "/":
			usages = append(usages, pkgdisks.DiskUsage{Type: "system", Label: "system-root", Mountpoint: mount})
		case "/boot":
			usages = append(usages, pkgdisks.DiskUsage{Type: "system", Label: "system-boot", Mountpoint: mount})
		case "/boot/efi":
			usages = append(usages, pkgdisks.DiskUsage{Type: "system", Label: "system-efi", Mountpoint: mount})
		}
	}
	for _, partition := range partitions {
		usages = append(usages, partition.Usages...)
	}
	return dedupeUsages(usages)
}

func dedupeUsages(items []pkgdisks.DiskUsage) []pkgdisks.DiskUsage {
	if len(items) == 0 {
		return []pkgdisks.DiskUsage{}
	}
	seen := make(map[string]struct{}, len(items))
	result := make([]pkgdisks.DiskUsage, 0, len(items))
	for _, item := range items {
		key := strings.Join([]string{item.Type, item.Label, item.Mountpoint, item.StoragePoolID, item.StorageID, item.DeviceRole}, "|")
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, item)
	}
	return result
}

func summarizeUsage(usages []pkgdisks.DiskUsage) string {
	if len(usages) == 0 {
		return "unused"
	}
	if len(usages) == 1 {
		return usages[0].Type
	}
	return "mixed"
}

func readBlockStats(kernelName string) (blockStat, error) {
	statsPath := filepath.Join("/sys/block", kernelName, "stat")
	content, err := os.ReadFile(statsPath)
	if err != nil {
		return blockStat{}, err
	}

	fields := strings.Fields(string(content))
	if len(fields) < 7 {
		return blockStat{}, fmt.Errorf("unexpected stat format")
	}

	readOps, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil {
		return blockStat{}, err
	}
	readSectors, err := strconv.ParseUint(fields[2], 10, 64)
	if err != nil {
		return blockStat{}, err
	}
	writeOps, err := strconv.ParseUint(fields[4], 10, 64)
	if err != nil {
		return blockStat{}, err
	}
	writeSectors, err := strconv.ParseUint(fields[6], 10, 64)
	if err != nil {
		return blockStat{}, err
	}

	sectorSize := readSectorSize(kernelName)
	return blockStat{
		readOps:    readOps,
		readBytes:  readSectors * sectorSize,
		writeOps:   writeOps,
		writeBytes: writeSectors * sectorSize,
	}, nil
}

func readSectorSize(kernelName string) uint64 {
	content, err := os.ReadFile(filepath.Join("/sys/block", kernelName, "queue", "hw_sector_size"))
	if err != nil {
		content, err = os.ReadFile(filepath.Join("/sys/block", kernelName, "queue", "logical_block_size"))
		if err != nil {
			return defaultSectorSize
		}
	}

	size, err := strconv.ParseUint(strings.TrimSpace(string(content)), 10, 64)
	if err != nil || size == 0 {
		return defaultSectorSize
	}
	return size
}

func calculateRate(current, previous uint64, elapsed float64) float64 {
	if current < previous || elapsed <= 0 {
		return 0
	}
	return float64(current-previous) / elapsed
}

func applyBackgroundRates(info *pkgdisks.DiskInfo) {
	diskRateCache.RLock()
	rate, ok := diskRateCache.byKernelName[info.KernelName]
	diskRateCache.RUnlock()
	if !ok {
		return
	}

	info.ReadBytesPerSec = rate.readBytesPerSec
	info.WriteBytesPerSec = rate.writeBytesPerSec
	info.ReadOpsPerSec = rate.readOpsPerSec
	info.WriteOpsPerSec = rate.writeOpsPerSec
	if rate.sampledAt.After(info.SampledAt) {
		info.SampledAt = rate.sampledAt
	}
}

func startDiskRateSampler() {
	diskRateSampler.Do(func() {
		go func() {
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()

			sampleDiskRates()
			for range ticker.C {
				sampleDiskRates()
			}
		}()
	})
}

func sampleDiskRates() {
	entries, err := os.ReadDir("/sys/block")
	if err != nil {
		return
	}

	now := time.Now()
	nextSnapshots := make(map[string]diskRateSnapshot, len(entries))
	nextRates := make(map[string]diskRateValue, len(entries))

	diskStatsCache.Lock()
	for _, entry := range entries {
		kernelName := entry.Name()
		stats, err := readBlockStats(kernelName)
		if err != nil {
			continue
		}

		current := diskRateSnapshot{
			readBytes:  stats.readBytes,
			writeBytes: stats.writeBytes,
			readOps:    stats.readOps,
			writeOps:   stats.writeOps,
			sampledAt:  now,
		}
		nextSnapshots[kernelName] = current

		prev, ok := diskStatsCache.byPath[kernelName]
		if !ok {
			continue
		}

		elapsed := now.Sub(prev.sampledAt).Seconds()
		if elapsed <= 0 {
			continue
		}

		nextRates[kernelName] = diskRateValue{
			readBytesPerSec:  calculateRate(current.readBytes, prev.readBytes, elapsed),
			writeBytesPerSec: calculateRate(current.writeBytes, prev.writeBytes, elapsed),
			readOpsPerSec:    calculateRate(current.readOps, prev.readOps, elapsed),
			writeOpsPerSec:   calculateRate(current.writeOps, prev.writeOps, elapsed),
			sampledAt:        now,
		}
	}
	diskStatsCache.byPath = nextSnapshots
	diskStatsCache.Unlock()

	diskRateCache.Lock()
	for kernelName, rate := range nextRates {
		diskRateCache.byKernelName[kernelName] = rate
	}
	diskRateCache.Unlock()
}

func readSMART(ctx context.Context, path string) (*smartctlResponse, []string) {
	result, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "smartctl", "-j", "-H", "-A", path)
	if err != nil && strings.TrimSpace(result.Stdout) == "" {
		return nil, []string{"SMART probe failed: " + err.Error()}
	}

	if strings.TrimSpace(result.Stdout) == "" {
		return nil, nil
	}

	var payload smartctlResponse
	if unmarshalErr := json.Unmarshal([]byte(result.Stdout), &payload); unmarshalErr != nil {
		return nil, []string{"Failed to parse SMART output: " + unmarshalErr.Error()}
	}

	warnings := make([]string, 0, len(payload.Messages))
	for _, message := range payload.Messages {
		message.String = strings.TrimSpace(message.String)
		if message.String != "" {
			warnings = append(warnings, message.String)
		}
	}
	return &payload, warnings
}

func applySMART(info *pkgdisks.DiskInfo, smart smartctlResponse) {
	info.SMARTAvailable = true
	info.SMARTPassed = &smart.SmartStatus.Passed
	if smart.SmartStatus.Passed {
		info.Health = "passed"
	} else {
		info.Health = "failed"
	}

	if smart.RotationRate > 0 {
		rotation := smart.RotationRate
		info.RotationRateRPM = &rotation
	}
	if smart.PowerOnTime.Hours > 0 {
		hours := smart.PowerOnTime.Hours
		info.PowerOnHours = &hours
	}
	if smart.PowerCycleCount > 0 {
		count := smart.PowerCycleCount
		info.PowerCycleCount = &count
		info.PowerOnCount = &count
	} else if smart.NVMeSmartHealthInformationLog.PowerCycles > 0 {
		count := smart.NVMeSmartHealthInformationLog.PowerCycles
		info.PowerCycleCount = &count
		info.PowerOnCount = &count
	}
	if unsafeShutdowns := detectUnsafeShutdownCount(smart); unsafeShutdowns != nil {
		info.UnsafeShutdownCount = unsafeShutdowns
	}
	if healthPercent := detectHealthPercent(smart); healthPercent != nil {
		info.HealthPercent = healthPercent
	}

	if info.TemperatureC == nil {
		if temp := detectTemperature(smart); temp != nil {
			info.TemperatureC = temp
		}
	}
}

func readHWMonTemperature(kernelName string) (*int, error) {
	patterns := []string{
		filepath.Join("/sys/class/block", kernelName, "device", "hwmon", "hwmon*", "temp*_input"),
		filepath.Join("/sys/class/block", kernelName, "device", "device", "hwmon", "hwmon*", "temp*_input"),
	}

	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			continue
		}
		for _, match := range matches {
			content, err := os.ReadFile(match)
			if err != nil {
				continue
			}
			raw := strings.TrimSpace(string(content))
			if raw == "" {
				continue
			}
			value, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || value <= 0 {
				continue
			}
			celsius := int(value)
			if celsius >= 1000 {
				celsius /= 1000
			}
			if celsius > 0 && celsius < 200 {
				return &celsius, nil
			}
		}
	}

	return nil, fmt.Errorf("no hwmon temperature found")
}

func detectTemperature(smart smartctlResponse) *int {
	if smart.Temperature.Current > 0 {
		value := smart.Temperature.Current
		return &value
	}
	if smart.NVMeSmartHealthInformationLog.Temperature > 0 {
		value := smart.NVMeSmartHealthInformationLog.Temperature
		return &value
	}

	for _, attr := range smart.ATASmartAttributes.Table {
		name := strings.ToLower(attr.Name)
		switch name {
		case "temperature_celsius", "airflow_temperature_cel", "temperature_internal", "drive_temperature":
			value := int(attr.Raw.Value)
			if value <= 0 {
				value = attr.Value
			}
			if value > 0 {
				return &value
			}
		}
	}

	return nil
}

func detectUnsafeShutdownCount(smart smartctlResponse) *uint64 {
	if smart.NVMeSmartHealthInformationLog.UnsafeShutdowns > 0 {
		value := smart.NVMeSmartHealthInformationLog.UnsafeShutdowns
		return &value
	}
	for _, attr := range smart.ATASmartAttributes.Table {
		name := strings.ToLower(strings.TrimSpace(attr.Name))
		switch name {
		case "unsafe_shutdown_count", "unsafe_shutdowns", "unexpected_power_loss", "power_loss_protection_failure":
			value := attr.Raw.Value
			return &value
		}
	}
	return nil
}

func detectHealthPercent(smart smartctlResponse) *int {
	if smart.NVMeSmartHealthInformationLog.PercentageUsed > 0 {
		value := 100 - int(smart.NVMeSmartHealthInformationLog.PercentageUsed)
		return intPtr(clampInt(value, 0, 100))
	}
	for _, attr := range smart.ATASmartAttributes.Table {
		name := strings.ToLower(strings.TrimSpace(attr.Name))
		switch name {
		case "percent_lifetime_remain", "ssd_life_left", "remaining_lifetime_perc", "remaining_lifetime_percent", "available_reserved_space":
			value := attr.Value
			if attr.Raw.Value > 0 && attr.Raw.Value <= 100 {
				value = int(attr.Raw.Value)
			}
			return intPtr(clampInt(value, 0, 100))
		case "media_wearout_indicator":
			return intPtr(clampInt(attr.Value, 0, 100))
		case "percentage_used", "wear_leveling_count", "wear_leveling_count_delta":
			value := int(attr.Raw.Value)
			if value <= 0 {
				value = 100 - attr.Value
			}
			return intPtr(clampInt(100-value, 0, 100))
		}
	}
	if smart.SmartStatus.Passed {
		return intPtr(100)
	}
	return intPtr(0)
}

func intPtr(value int) *int {
	return &value
}

func clampInt(value int, min int, max int) int {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

func deviceIsSystemDisk(device lsblkDevice) bool {
	for _, mount := range normalizedMountpoints(device) {
		switch mount {
		case "/", "/boot", "/boot/efi":
			return true
		}
	}
	for _, child := range device.Children {
		if deviceIsSystemDisk(child) {
			return true
		}
	}
	return false
}

func deviceHasActiveRaidUsage(device lsblkDevice, storageMounts []string) bool {
	deviceType := strings.ToLower(strings.TrimSpace(device.Type))

	if isActiveRaidNode(deviceType) {
		if deviceMountsUsed(device, storageMounts) {
			return true
		}
		for _, child := range device.Children {
			if deviceMountsUsed(child, storageMounts) {
				return true
			}
		}
	}

	for _, child := range device.Children {
		if deviceHasActiveRaidUsage(child, storageMounts) {
			return true
		}
	}
	return false
}

func isActiveRaidNode(deviceType string) bool {
	switch deviceType {
	case "md", "raid0", "raid1", "raid4", "raid5", "raid6", "raid10":
		return true
	default:
		return false
	}
}

func deviceMountsUsed(device lsblkDevice, storageMounts []string) bool {
	for _, mount := range normalizedMountpoints(device) {
		cleanMount := filepath.Clean(mount)
		if cleanMount == "" {
			continue
		}
		if cleanMount == "/" || cleanMount == "/boot" || cleanMount == "/boot/efi" {
			return true
		}
		if slices.Contains(storageMounts, cleanMount) {
			return true
		}
	}
	return false
}
