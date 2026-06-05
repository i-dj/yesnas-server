package storagepool

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"nas-server/internal/storage"
	commandrunner "nas-server/pkg/shell"
)

func (h *Handler) HandleCreatePool(w http.ResponseWriter, r *http.Request) {
	var req CreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	result, err := CreatePool(r.Context(), req)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "CREATE_STORAGE_POOL_FAILED", err.Error())
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, result)
}

func CreatePool(ctx context.Context, req CreateRequest) (*StoragePool, error) {
	if err := ValidateName(strings.TrimSpace(req.Name)); err != nil {
		return nil, err
	}
	autoSnapshotSchedule, err := normalizeAutoSnapshot(req.AutoSnapshotEnabled, req.AutoSnapshotSchedule)
	if err != nil {
		return nil, err
	}
	if len(req.CacheDiskPaths) > 0 {
		return nil, fmt.Errorf("cache disk is not supported for btrfs pools yet")
	}

	raidLevel, err := normalizeRaidLevel(req.RaidLevel)
	if err != nil {
		return nil, err
	}
	devicePaths, err := normalizeDevicePaths(req.DevicePaths)
	if err != nil {
		return nil, err
	}
	if err := validateRaidDeviceCount(raidLevel, len(devicePaths)); err != nil {
		return nil, err
	}

	mountRoot := envOr("BTRFS_POOL_ROOT", "/srv/yesnas/pools")
	pathName, err := generatePoolPathName(mountRoot, req.Name)
	if err != nil {
		return nil, err
	}
	mountPath := filepath.Join(mountRoot, pathName)
	dataPath := filepath.Join(mountPath, "data")
	if err := ensureCreateAvailable(strings.TrimSpace(req.Name), mountPath, dataPath); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(mountPath, 0o755); err != nil {
		return nil, fmt.Errorf("create mount path: %w", err)
	}

	poolDevices, err := resolvePoolDevices(ctx, devicePaths)
	if err != nil {
		return nil, err
	}
	if err := forcePreparePoolDevices(ctx, devicePaths); err != nil {
		return nil, err
	}

	mkfsArgs := append([]string{"-f", "-L", req.Name, "-d", raidLevel, "-m", raidLevel}, devicePaths...)
	if _, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "mkfs.btrfs", mkfsArgs...); err != nil {
		return nil, fmt.Errorf("mkfs.btrfs failed: %w", err)
	}
	if _, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "mount", "-t", "btrfs", devicePaths[0], mountPath); err != nil {
		return nil, fmt.Errorf("mount btrfs pool failed: %w", err)
	}
	if err := persistPoolMount(ctx, devicePaths[0], mountPath); err != nil {
		return nil, fmt.Errorf("persist fstab entry: %w", err)
	}
	if err := initializePoolLayout(ctx, mountPath, dataPath); err != nil {
		return nil, fmt.Errorf("initialize pool layout: %w", err)
	}

	totalSize, freeSize := statFilesystem(dataPath)
	storageID, err := storage.Add(storage.Storage{
		Name:      req.Name,
		Location:  "local",
		MountPath: dataPath,
		Type:      storage.Local,
		Status:    storage.StatusOnline,
		TotalSize: totalSize,
		FreeSize:  freeSize,
		ExtraConfig: storage.BuildExtraConfig(map[string]any{
			"poolName":   req.Name,
			"raidLevel":  raidLevel,
			"filesystem": "btrfs",
		}),
	})
	if err != nil {
		return nil, fmt.Errorf("create storage record: %w", err)
	}

	now := time.Now()
	nextAutoSnapshotAt := initialAutoSnapshotNextRun(req.AutoSnapshotEnabled, autoSnapshotSchedule, now)
	pool := StoragePool{
		StorageID:            storageID,
		Name:                 req.Name,
		Filesystem:           "btrfs",
		RaidLevel:            raidLevel,
		MountPath:            mountPath,
		DataPath:             dataPath,
		AutoSnapshotEnabled:  req.AutoSnapshotEnabled,
		AutoSnapshotSchedule: autoSnapshotSchedule,
		NextAutoSnapshotAt:   nextAutoSnapshotAt,
		UpdatedAt:            &now,
	}
	if err := Add(pool, poolDevices); err != nil {
		return nil, fmt.Errorf("create storage pool record: %w", err)
	}

	pools, err := List()
	if err != nil {
		return nil, err
	}
	for _, item := range pools {
		if item.StorageID == storageID {
			return &item, nil
		}
	}
	return nil, fmt.Errorf("storage pool created but failed to reload")
}

func normalizeAutoSnapshot(enabled bool, schedule string) (string, error) {
	if !enabled {
		return "", nil
	}
	value := AutoSnapshotSchedule(strings.ToLower(strings.TrimSpace(schedule)))
	switch value {
	case AutoSnapshotScheduleHourly, AutoSnapshotScheduleDaily, AutoSnapshotScheduleMonthly:
		return string(value), nil
	default:
		return "", fmt.Errorf("auto snapshot schedule must be one of: hourly, daily, monthly")
	}
}

func initialAutoSnapshotNextRun(enabled bool, schedule string, now time.Time) *time.Time {
	if !enabled || strings.TrimSpace(schedule) == "" {
		return nil
	}
	next := NextAutoSnapshotTime(now, schedule)
	return &next
}

func NextAutoSnapshotTime(from time.Time, schedule string) time.Time {
	base := from.In(time.Local)
	switch AutoSnapshotSchedule(strings.ToLower(strings.TrimSpace(schedule))) {
	case AutoSnapshotScheduleHourly:
		return base.Truncate(time.Hour).Add(time.Hour)
	case AutoSnapshotScheduleDaily:
		return time.Date(base.Year(), base.Month(), base.Day()+1, 0, 0, 0, 0, base.Location())
	case AutoSnapshotScheduleMonthly:
		return time.Date(base.Year(), base.Month()+1, 1, 0, 0, 0, 0, base.Location())
	default:
		return base.Add(time.Hour)
	}
}

func normalizeRaidLevel(value string) (string, error) {
	level := strings.ToLower(strings.TrimSpace(value))
	switch level {
	case "single", "raid0", "raid1", "raid5", "raidi5", "raid10":
		if level == "raidi5" {
			return "raid5", nil
		}
		return level, nil
	default:
		return "", fmt.Errorf("unsupported raid level: %s", value)
	}
}

func validateRaidDeviceCount(level string, count int) error {
	switch level {
	case "single":
		if count < 1 {
			return fmt.Errorf("single requires at least 1 device")
		}
	case "raid0", "raid1":
		if count < 2 {
			return fmt.Errorf("%s requires at least 2 devices", level)
		}
	case "raid5":
		if count < 3 {
			return fmt.Errorf("raid5 requires at least 3 devices")
		}
	case "raid10":
		if count < 4 || count%2 != 0 {
			return fmt.Errorf("raid10 requires at least 4 devices and an even device count")
		}
	}
	return nil
}

func normalizeDevicePaths(paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("at least one device path is required")
	}
	result := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		clean := filepath.Clean(strings.TrimSpace(path))
		if clean == "" || !strings.HasPrefix(clean, "/dev/") {
			return nil, fmt.Errorf("invalid device path: %s", path)
		}
		if _, ok := seen[clean]; ok {
			return nil, fmt.Errorf("duplicate device path: %s", clean)
		}
		seen[clean] = struct{}{}
		result = append(result, clean)
	}
	return result, nil
}

func generatePoolPathName(mountRoot string, name string) (string, error) {
	candidate := stableNameHash(name)
	mountPath := filepath.Join(mountRoot, candidate)
	dataPath := filepath.Join(mountPath, "data")

	existingMountStorage, err := storage.GetByMountPath(mountPath)
	if err != nil {
		return "", fmt.Errorf("check generated mount path in storage database: %w", err)
	}
	existingDataStorage, err := storage.GetByMountPath(dataPath)
	if err != nil {
		return "", fmt.Errorf("check generated data path in storage database: %w", err)
	}
	if existingMountStorage != nil || existingDataStorage != nil {
		return "", fmt.Errorf("generated path name already exists for pool name: %s", strings.TrimSpace(name))
	}

	if err := cleanupStaleMountPath(mountPath); err != nil {
		return "", err
	}

	if _, err := os.Stat(mountPath); err == nil {
		return "", fmt.Errorf("generated path name already exists for pool name: %s", strings.TrimSpace(name))
	} else if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("check generated mount path: %w", err)
	}
	return candidate, nil
}

func stableNameHash(name string) string {
	hasher := fnv.New64a()
	_, _ = hasher.Write([]byte(strings.TrimSpace(name)))
	return fmt.Sprintf("%x", hasher.Sum64())[:10]
}

func cleanupStaleMountPath(mountPath string) error {
	if mountPath == "" {
		return nil
	}
	if _, err := os.Stat(mountPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("check generated mount path: %w", err)
	}
	if isMountpointActive(mountPath) {
		return nil
	}
	if err := os.RemoveAll(mountPath); err != nil {
		return fmt.Errorf("cleanup stale mount path %s: %w", mountPath, err)
	}
	return nil
}

func ensureCreateAvailable(poolName string, mountPath string, dataPath string) error {
	existingPool, err := GetByName(poolName)
	if err != nil {
		return fmt.Errorf("check existing storage pool by name: %w", err)
	}
	if existingPool != nil {
		return fmt.Errorf("storage pool name already exists: %s", poolName)
	}
	existingStorage, err := storage.GetByMountPath(mountPath)
	if err != nil {
		return fmt.Errorf("check existing storage mount path: %w", err)
	}
	if existingStorage != nil {
		return fmt.Errorf("mount path already exists in storage database: %s (storage=%s)", mountPath, existingStorage.Name)
	}
	existingDataStorage, err := storage.GetByMountPath(dataPath)
	if err != nil {
		return fmt.Errorf("check existing storage data path: %w", err)
	}
	if existingDataStorage != nil {
		return fmt.Errorf("data path already exists in storage database: %s (storage=%s)", dataPath, existingDataStorage.Name)
	}
	return nil
}

func initializePoolLayout(ctx context.Context, mountPath string, dataPath string) error {
	snapshotsPath := filepath.Join(mountPath, ".snapshots")
	if _, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "mkdir", "-p", snapshotsPath); err != nil {
		return fmt.Errorf("create snapshots directory: %w", err)
	}
	if _, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "btrfs", "subvolume", "create", dataPath); err != nil {
		return fmt.Errorf("create data subvolume: %w", err)
	}
	return nil
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
