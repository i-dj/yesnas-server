package storagepool

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	commandrunner "nas-server/pkg/shell"
)

func resolvePoolDevices(ctx context.Context, paths []string) ([]PoolDevice, error) {
	deviceMap, err := loadPoolDeviceMetadata(ctx, paths)
	if err != nil {
		return nil, err
	}
	resolved := make([]PoolDevice, 0, len(paths))
	for _, path := range paths {
		device, ok := deviceMap[path]
		if !ok {
			return nil, fmt.Errorf("device metadata not found: %s", path)
		}
		resolved = append(resolved, device)
	}
	return resolved, nil
}

func loadPoolDeviceMetadata(ctx context.Context, paths []string) (map[string]PoolDevice, error) {
	cache := make(map[string]LSBLKDevice)
	result := make(map[string]PoolDevice, len(paths))
	for _, path := range paths {
		device, err := inspectPoolDevice(ctx, path, cache)
		if err != nil {
			return nil, err
		}
		result[path] = device
	}
	return result, nil
}

func inspectPoolDevice(ctx context.Context, path string, cache map[string]LSBLKDevice) (PoolDevice, error) {
	device, err := loadSinglePoolDevice(ctx, path, cache)
	if err != nil {
		return PoolDevice{}, err
	}
	parentPath := ""
	model := strings.TrimSpace(device.Model)
	serial := strings.TrimSpace(device.Serial)
	vendor := strings.TrimSpace(device.Vendor)
	transport := strings.TrimSpace(device.Transport)
	if device.Type == "part" && device.ParentName != "" {
		parentPath = filepath.Join("/dev", device.ParentName)
		parent, err := loadSinglePoolDevice(ctx, parentPath, cache)
		if err != nil {
			return PoolDevice{}, err
		}
		if model == "" {
			model = strings.TrimSpace(parent.Model)
		}
		if serial == "" {
			serial = strings.TrimSpace(parent.Serial)
		}
		if vendor == "" {
			vendor = strings.TrimSpace(parent.Vendor)
		}
		if transport == "" {
			transport = strings.TrimSpace(parent.Transport)
		}
	}
	sizeBytes, err := readBlockDeviceSizeBytes(device.KernelName)
	if err != nil {
		return PoolDevice{}, fmt.Errorf("read size for %s: %w", path, err)
	}
	return PoolDevice{
		DevicePath: path,
		DeviceName: device.Name,
		KernelName: device.KernelName,
		ParentPath: parentPath,
		SizeBytes:  sizeBytes,
		SizeHuman:  device.Size,
		Model:      model,
		Serial:     serial,
		Vendor:     vendor,
		Transport:  transport,
		DeviceRole: "data",
	}, nil
}

func loadSinglePoolDevice(ctx context.Context, path string, cache map[string]LSBLKDevice) (LSBLKDevice, error) {
	if device, ok := cache[path]; ok {
		return device, nil
	}
	result, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "lsblk", "-J", "-o", "NAME,KNAME,PATH,PKNAME,TYPE,SIZE,MODEL,SERIAL,VENDOR,TRAN,MOUNTPOINT,MOUNTPOINTS", path)
	if err != nil {
		return LSBLKDevice{}, fmt.Errorf("inspect device %s: %w", path, err)
	}
	var payload LSBLKResponse
	if err := json.Unmarshal([]byte(result.Stdout), &payload); err != nil {
		return LSBLKDevice{}, fmt.Errorf("parse lsblk for %s: %w", path, err)
	}
	if len(payload.Blockdevices) == 0 {
		return LSBLKDevice{}, fmt.Errorf("device not found: %s", path)
	}
	device := payload.Blockdevices[0]
	cache[path] = device
	return device, nil
}

func forcePreparePoolDevices(ctx context.Context, paths []string) error {
	cache := make(map[string]LSBLKDevice)
	for _, path := range paths {
		registered, err := HasDevice(path)
		if err != nil {
			return fmt.Errorf("check device registration for %s: %w", path, err)
		}
		if registered {
			return fmt.Errorf("device %s is already registered in storage pool database", path)
		}
		device, err := loadSinglePoolDevice(ctx, path, cache)
		if err != nil {
			return err
		}
		if poolDeviceIsSystemDisk(device) {
			return fmt.Errorf("device %s is part of the system disk and cannot be wiped", path)
		}
		for _, mountPath := range collectNonSystemMountpoints(device) {
			if _, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "umount", mountPath); err != nil {
				return fmt.Errorf("unmount %s for %s: %w", mountPath, path, err)
			}
		}
		for _, mdPath := range collectChildMDDevices(device) {
			if _, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "mdadm", "--stop", mdPath); err != nil {
				return fmt.Errorf("stop md device %s for %s: %w", mdPath, path, err)
			}
		}
		if _, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "mdadm", "--zero-superblock", "--force", path); err != nil {
			log.Printf("[POOL] continue after zero-superblock failure on %s: %v", path, err)
		}
		if _, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "wipefs", "-af", path); err != nil {
			return fmt.Errorf("wipe %s: %w", path, err)
		}
	}
	return nil
}

func collectNonSystemMountpoints(device LSBLKDevice) []string {
	seen := map[string]struct{}{}
	var result []string
	var walk func(node LSBLKDevice)
	walk = func(node LSBLKDevice) {
		for _, mountpoint := range deviceMountpoints(node) {
			if isSystemMountpoint(mountpoint) {
				continue
			}
			if _, ok := seen[mountpoint]; ok {
				continue
			}
			seen[mountpoint] = struct{}{}
			result = append(result, mountpoint)
		}
		for _, child := range node.Children {
			walk(child)
		}
	}
	walk(device)
	slices.SortFunc(result, func(a, b string) int { return strings.Compare(b, a) })
	return result
}

func deviceMountpoints(device LSBLKDevice) []string {
	points := make([]string, 0, len(device.Mountpoints)+1)
	if device.Mountpoint != nil {
		if mountpoint := strings.TrimSpace(*device.Mountpoint); mountpoint != "" {
			points = append(points, mountpoint)
		}
	}
	for _, raw := range device.Mountpoints {
		if mountpoint := strings.TrimSpace(raw); mountpoint != "" {
			points = append(points, mountpoint)
		}
	}
	return points
}

func poolDeviceIsSystemDisk(device LSBLKDevice) bool {
	if poolLSBLKDeviceHasSystemMount(device) {
		return true
	}
	for _, child := range device.Children {
		if poolDeviceIsSystemDisk(child) {
			return true
		}
	}
	return false
}

func poolLSBLKDeviceHasSystemMount(device LSBLKDevice) bool {
	if device.Mountpoint != nil && isSystemMountpoint(strings.TrimSpace(*device.Mountpoint)) {
		return true
	}
	for _, mountpoint := range device.Mountpoints {
		if isSystemMountpoint(strings.TrimSpace(mountpoint)) {
			return true
		}
	}
	return false
}

func isSystemMountpoint(mountpoint string) bool {
	switch mountpoint {
	case "/", "/boot", "/boot/efi":
		return true
	default:
		return false
	}
}

func collectChildMDDevices(device LSBLKDevice) []string {
	seen := map[string]struct{}{}
	var result []string
	var walk func(children []LSBLKDevice)
	walk = func(children []LSBLKDevice) {
		for _, child := range children {
			if child.Path != "" && isMDLikeDevice(child) {
				if _, ok := seen[child.Path]; !ok {
					seen[child.Path] = struct{}{}
					result = append(result, child.Path)
				}
			}
			if len(child.Children) > 0 {
				walk(child.Children)
			}
		}
	}
	walk(device.Children)
	return result
}

func isMDLikeDevice(device LSBLKDevice) bool {
	if strings.HasPrefix(device.Name, "md") {
		return true
	}
	if device.Type == "md" {
		return true
	}
	return strings.HasPrefix(device.Type, "raid")
}

func readBlockDeviceSizeBytes(kernelName string) (uint64, error) {
	sectorCountRaw, err := os.ReadFile(filepath.Join("/sys/class/block", kernelName, "size"))
	if err != nil {
		return 0, err
	}
	sectorCount, err := strconv.ParseUint(strings.TrimSpace(string(sectorCountRaw)), 10, 64)
	if err != nil {
		return 0, err
	}
	return sectorCount * readPoolSectorSize(kernelName), nil
}

func readPoolSectorSize(kernelName string) uint64 {
	paths := []string{
		filepath.Join("/sys/class/block", kernelName, "queue", "hw_sector_size"),
		filepath.Join("/sys/class/block", kernelName, "queue", "logical_block_size"),
	}
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		size, err := strconv.ParseUint(strings.TrimSpace(string(content)), 10, 64)
		if err == nil && size > 0 {
			return size
		}
	}
	return 512
}
