//go:build linux

package linuxplatform

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"nas-server/internal/storage"
	pkgdisks "nas-server/pkg/disks"
	commandrunner "nas-server/pkg/shell"
)

type lsblkResponse struct {
	Blockdevices []lsblkDevice `json:"blockdevices"`
}

type lsblkDevice struct {
	Name        string        `json:"name"`
	KName       string        `json:"kname"`
	Path        string        `json:"path"`
	Type        string        `json:"type"`
	Size        string        `json:"size"`
	FsType      string        `json:"fstype"`
	Label       string        `json:"label"`
	UUID        string        `json:"uuid"`
	Model       string        `json:"model"`
	Serial      string        `json:"serial"`
	Vendor      string        `json:"vendor"`
	Tran        string        `json:"tran"`
	Mountpoint  string        `json:"mountpoint"`
	Mountpoints []interface{} `json:"mountpoints"`
	RM          bool          `json:"rm"`
	Hotplug     bool          `json:"hotplug"`
	RO          bool          `json:"ro"`
	Children    []lsblkDevice `json:"children"`
}

func ListCandidates(ctx context.Context) (pkgdisks.CandidateList, error) {
	storages, err := storage.List()
	if err != nil {
		return pkgdisks.CandidateList{}, fmt.Errorf("load storages: %w", err)
	}

	storageMounts := make([]string, 0, len(storages))
	for _, storage := range storages {
		if storage.MountPath == "" {
			continue
		}
		storageMounts = append(storageMounts, filepath.Clean(storage.MountPath))
	}

	result, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "lsblk",
		"-J",
		"-o", "NAME,KNAME,PATH,TYPE,SIZE,FSTYPE,LABEL,UUID,MODEL,SERIAL,VENDOR,TRAN,MOUNTPOINT,MOUNTPOINTS,RM,HOTPLUG,RO")
	if err != nil {
		return pkgdisks.CandidateList{}, fmt.Errorf("run lsblk: %w", err)
	}

	var payload lsblkResponse
	if err := json.Unmarshal([]byte(result.Stdout), &payload); err != nil {
		return pkgdisks.CandidateList{}, fmt.Errorf("parse lsblk output: %w", err)
	}

	items := make([]pkgdisks.Candidate, 0)
	for _, device := range payload.Blockdevices {
		items = append(items, collectCandidates(device, storageMounts)...)
	}

	slices.SortFunc(items, func(a, b pkgdisks.Candidate) int {
		return strings.Compare(a.Path, b.Path)
	})

	return pkgdisks.CandidateList{
		Items:       items,
		GeneratedAt: time.Now().UTC(),
	}, nil
}

func collectCandidates(device lsblkDevice, storageMounts []string) []pkgdisks.Candidate {
	if shouldIgnoreBlockDevice(device) || device.Type != "disk" {
		return nil
	}

	partitions := childPartitions(device.Children)
	if len(partitions) == 0 {
		candidate, ok := classifyWholeDisk(device, storageMounts)
		if ok {
			return []pkgdisks.Candidate{candidate}
		}
		return nil
	}

	items := make([]pkgdisks.Candidate, 0, len(partitions))
	for _, partition := range partitions {
		candidate, ok := classifyPartition(partition, device, storageMounts)
		if ok {
			items = append(items, candidate)
		}
	}
	return items
}

func childPartitions(children []lsblkDevice) []lsblkDevice {
	partitions := make([]lsblkDevice, 0, len(children))
	for _, child := range children {
		if child.Type == "part" {
			partitions = append(partitions, child)
		}
	}
	return partitions
}

func shouldIgnoreBlockDevice(device lsblkDevice) bool {
	if device.Path == "" {
		return true
	}
	switch device.Type {
	case "loop", "rom", "md", "raid0", "raid1", "raid4", "raid5", "raid6", "raid10":
		return true
	}
	return false
}

func classifyWholeDisk(device lsblkDevice, storageMounts []string) (pkgdisks.Candidate, bool) {
	sizeBytes, err := readBlockDeviceSizeBytes(device.KName)
	if device.Path == "" || err != nil || sizeBytes == 0 || device.RO {
		return pkgdisks.Candidate{}, false
	}
	if isUsedByStorage(device, storageMounts) {
		return pkgdisks.Candidate{}, false
	}
	if hasMountpoint(device) {
		return pkgdisks.Candidate{}, false
	}
	if hasBlockingChildren(device.Children) {
		return buildCandidate(device, sizeBytes, "", "disk", "disk-has-child-devices", false, true, "Device has child block devices and should be detached or wiped before creating RAID"), true
	}
	if hasKnownFilesystem(device) {
		return buildCandidate(device, sizeBytes, "", "disk", "unmounted-whole-disk-with-filesystem", false, true, "Device has an existing filesystem signature and should be wiped before creating RAID"), true
	}
	return buildCandidate(device, sizeBytes, "", "disk", "unused-whole-disk", true, false, ""), true
}

func classifyPartition(device lsblkDevice, parent lsblkDevice, storageMounts []string) (pkgdisks.Candidate, bool) {
	sizeBytes, err := readBlockDeviceSizeBytes(device.KName)
	if device.Path == "" || err != nil || sizeBytes == 0 || device.RO {
		return pkgdisks.Candidate{}, false
	}
	if isUsedByStorage(device, storageMounts) {
		return pkgdisks.Candidate{}, false
	}
	if hasMountpoint(device) {
		return pkgdisks.Candidate{}, false
	}
	merged := mergePartitionMetadata(device, parent)
	if hasBlockingChildren(device.Children) {
		return buildCandidate(merged, sizeBytes, parent.Path, "partition", "partition-has-child-devices", false, true, "Partition has child block devices and should be detached or wiped before creating RAID"), true
	}
	if hasKnownFilesystem(device) {
		return buildCandidate(merged, sizeBytes, parent.Path, "partition", "unmounted-partition-with-filesystem", false, true, "Partition has an existing filesystem signature and should be wiped before creating RAID"), true
	}
	return buildCandidate(merged, sizeBytes, parent.Path, "partition", "unused-partition", true, false, ""), true
}

func buildCandidate(device lsblkDevice, sizeBytes uint64, parentPath, candidateType, reason string, eligible bool, needsWipe bool, warning string) pkgdisks.Candidate {
	return pkgdisks.Candidate{
		Path:          device.Path,
		Name:          device.Name,
		KernelName:    device.KName,
		ParentPath:    parentPath,
		CandidateType: candidateType,
		Reason:        reason,
		Eligible:      eligible,
		NeedsWipe:     needsWipe,
		Warning:       warning,
		HasChildren:   len(device.Children) > 0,
		Size:          device.Size,
		SizeBytes:     sizeBytes,
		Model:         strings.TrimSpace(device.Model),
		Serial:        strings.TrimSpace(device.Serial),
		Vendor:        strings.TrimSpace(device.Vendor),
		Transport:     strings.TrimSpace(device.Tran),
		FsType:        strings.TrimSpace(device.FsType),
		Label:         strings.TrimSpace(device.Label),
		UUID:          strings.TrimSpace(device.UUID),
		Mountpoints:   normalizedMountpoints(device),
		Removable:     device.RM,
		Hotplug:       device.Hotplug,
		ReadOnly:      device.RO,
	}
}

func mergePartitionMetadata(partition lsblkDevice, parent lsblkDevice) lsblkDevice {
	if strings.TrimSpace(partition.Model) == "" {
		partition.Model = parent.Model
	}
	if strings.TrimSpace(partition.Serial) == "" {
		partition.Serial = parent.Serial
	}
	if strings.TrimSpace(partition.Vendor) == "" {
		partition.Vendor = parent.Vendor
	}
	if strings.TrimSpace(partition.Tran) == "" {
		partition.Tran = parent.Tran
	}
	if !partition.RM {
		partition.RM = parent.RM
	}
	if !partition.Hotplug {
		partition.Hotplug = parent.Hotplug
	}
	return partition
}

func normalizedMountpoints(device lsblkDevice) []string {
	points := make([]string, 0, len(device.Mountpoints)+1)
	if mount := strings.TrimSpace(device.Mountpoint); mount != "" {
		points = append(points, mount)
	}
	for _, raw := range device.Mountpoints {
		value, ok := raw.(string)
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if !slices.Contains(points, value) {
			points = append(points, value)
		}
	}
	return points
}

func hasMountpoint(device lsblkDevice) bool {
	return len(normalizedMountpoints(device)) > 0
}

func hasKnownFilesystem(device lsblkDevice) bool {
	return strings.TrimSpace(device.FsType) != ""
}

func hasBlockingChildren(children []lsblkDevice) bool {
	return len(children) > 0
}

func isUsedByStorage(device lsblkDevice, storageMounts []string) bool {
	for _, mount := range normalizedMountpoints(device) {
		cleanMount := filepath.Clean(mount)
		if slices.Contains(storageMounts, cleanMount) {
			return true
		}
	}
	return false
}
