package storagepool

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"nas-server/internal/audit"
	"nas-server/internal/storage"
	commandrunner "nas-server/pkg/shell"
)

var (
	errFormatPasswordRequired      = errors.New("password is required")
	errFormatPasswordInvalid       = errors.New("password is invalid")
	errFormatPasswordNotConfigured = errors.New("format password is not configured")
)

func (h *Handler) HandleFormatPool(w http.ResponseWriter, r *http.Request) {
	pool, err := Get(strings.TrimSpace(r.PathValue("poolId")))
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "STORAGE_POOL_NOT_FOUND", "Storage pool not found")
		return
	}

	var req FormatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}

	result, err := FormatPool(r.Context(), pool, req)
	if err != nil {
		switch {
		case errors.Is(err, errFormatPasswordRequired):
			writeAPIError(w, http.StatusBadRequest, "FORMAT_PASSWORD_REQUIRED", "Password is required")
		case errors.Is(err, errFormatPasswordInvalid):
			writeAPIError(w, http.StatusForbidden, "FORMAT_PASSWORD_INVALID", "Password verification failed")
		case errors.Is(err, errFormatPasswordNotConfigured):
			writeAPIError(w, http.StatusServiceUnavailable, "FORMAT_PASSWORD_NOT_CONFIGURED", "Format password is not configured")
		default:
			audit.UserAction(r.Context(), "storage_pool_format_failed", "format", false, "storage_pool", pool.ID, pool.Name, err.Error(), nil)
			writeAPIError(w, http.StatusBadRequest, "FORMAT_STORAGE_POOL_FAILED", err.Error())
		}
		return
	}
	audit.UserAction(r.Context(), "storage_pool_formatted", "format", true, "storage_pool", pool.ID, pool.Name, "Storage pool formatted", result)
	writeJSON(w, result)
}

func FormatPool(ctx context.Context, pool *StoragePool, req FormatRequest) (map[string]any, error) {
	if err := verifyFormatPassword(req.Password); err != nil {
		return nil, err
	}
	if pool == nil {
		return nil, fmt.Errorf("storage pool is required")
	}
	if len(pool.Devices) == 0 {
		return nil, fmt.Errorf("storage pool has no devices")
	}

	raidLevel, err := normalizeRaidLevel(pool.RaidLevel)
	if err != nil {
		return nil, err
	}

	devicePaths := make([]string, 0, len(pool.Devices))
	for _, device := range pool.Devices {
		devicePath := strings.TrimSpace(device.DevicePath)
		if devicePath == "" {
			return nil, fmt.Errorf("pool device path is empty")
		}
		devicePaths = append(devicePaths, devicePath)
	}

	log.Printf("[POOL-FORMAT] start pool=%s name=%q devices=%v", pool.ID, pool.Name, devicePaths)

	if isMountpointActive(pool.MountPath) {
		if _, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "umount", pool.MountPath); err != nil {
			return nil, fmt.Errorf("unmount pool: %w", err)
		}
	}
	if err := removePoolMountUnit(ctx, pool.MountPath); err != nil {
		return nil, fmt.Errorf("remove mount unit: %w", err)
	}
	if err := removeLegacyPoolMountFromFstab(ctx, pool.MountPath); err != nil {
		return nil, fmt.Errorf("remove legacy fstab entry: %w", err)
	}

	for _, devicePath := range devicePaths {
		if _, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "mdadm", "--zero-superblock", "--force", devicePath); err != nil {
			log.Printf("[POOL-FORMAT] continue after zero-superblock failure on %s: %v", devicePath, err)
		}
		if _, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "wipefs", "-af", devicePath); err != nil {
			return nil, fmt.Errorf("wipe device %s: %w", devicePath, err)
		}
	}

	if err := os.MkdirAll(pool.MountPath, 0o755); err != nil {
		return nil, fmt.Errorf("create mount path: %w", err)
	}

	mkfsArgs := append([]string{"-f", "-L", pool.Name, "-d", raidLevel, "-m", raidLevel}, devicePaths...)
	if _, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "mkfs.btrfs", mkfsArgs...); err != nil {
		return nil, fmt.Errorf("mkfs.btrfs failed: %w", err)
	}
	if err := persistPoolMount(ctx, devicePaths[0], pool.MountPath); err != nil {
		return nil, fmt.Errorf("persist systemd mount unit: %w", err)
	}
	if err := initializePoolLayout(ctx, pool.MountPath, pool.DataPath); err != nil {
		return nil, fmt.Errorf("initialize pool layout: %w", err)
	}

	if err := DeleteSnapshotRecordsByPool(pool.ID); err != nil {
		return nil, fmt.Errorf("delete snapshot metadata: %w", err)
	}

	formattedAt := time.Now().UTC()
	if err := ResetBenchmarkResult(pool.ID, formattedAt); err != nil {
		return nil, fmt.Errorf("reset benchmark result: %w", err)
	}

	totalSize, freeSize := statFilesystem(pool.DataPath)
	if err := storage.UpdateRuntime(
		pool.StorageID,
		pool.DataPath,
		storage.StatusOnline,
		totalSize,
		freeSize,
		storage.BuildExtraConfig(map[string]any{
			"poolName":   pool.Name,
			"raidLevel":  raidLevel,
			"filesystem": pool.Filesystem,
		}),
	); err != nil {
		return nil, fmt.Errorf("update storage runtime: %w", err)
	}

	reloaded, err := Get(pool.ID)
	if err != nil {
		return nil, fmt.Errorf("reload storage pool: %w", err)
	}

	log.Printf("[POOL-FORMAT] success pool=%s name=%q", pool.ID, pool.Name)

	return map[string]any{
		"id":          pool.ID,
		"formatted":   true,
		"formattedAt": formattedAt,
		"pool":        buildResponse(ctx, *reloaded),
	}, nil
}

func verifyFormatPassword(password string) error {
	if password == "" {
		return errFormatPasswordRequired
	}

	if subtle.ConstantTimeCompare([]byte(password), []byte("123")) != 1 {
		return errFormatPasswordInvalid
	}
	return nil
}
