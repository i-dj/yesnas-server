package storagepool

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"nas-server/internal/audit"
	"nas-server/internal/storage"
	commandrunner "nas-server/pkg/shell"
)

func (h *Handler) HandleDeletePool(w http.ResponseWriter, r *http.Request) {
	pool, err := Get(strings.TrimSpace(r.PathValue("poolId")))
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "STORAGE_POOL_NOT_FOUND", "Storage pool not found")
		return
	}
	req := DeleteRequest{WipeDevices: false}
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeAPIError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
			return
		}
	}
	result, err := DeletePool(r.Context(), pool, req)
	if err != nil {
		audit.UserAction(r.Context(), "storage_pool_delete_failed", "delete", false, "storage_pool", pool.ID, pool.Name, err.Error(), nil)
		writeAPIError(w, http.StatusBadRequest, "DELETE_STORAGE_POOL_FAILED", err.Error())
		return
	}
	audit.UserAction(r.Context(), "storage_pool_deleted", "delete", true, "storage_pool", pool.ID, pool.Name, "Storage pool deleted", result)
	writeJSON(w, result)
}

func DeletePool(ctx context.Context, pool *StoragePool, req DeleteRequest) (map[string]any, error) {
	deletedSnapshots := make([]string, 0)
	storageRecord, err := storage.Get(pool.StorageID)
	if err != nil {
		return nil, fmt.Errorf("load storage record: %w", err)
	}
	isCloudPool := storageRecord != nil && isCloudStorage(*storageRecord)
	if req.WipeDevices {
		if isCloudPool {
			return nil, fmt.Errorf("wipe devices is not supported for cloud storage")
		}
		systemSubvolumes, _ := readBtrfsSubvolumes(ctx, pool.MountPath)
		sortSubvolumesDeepestFirst(systemSubvolumes)
		for _, subvolume := range systemSubvolumes {
			if _, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "btrfs", "subvolume", "delete", subvolume.Path); err != nil {
				return nil, fmt.Errorf("delete subvolume %s: %w", subvolume.Path, err)
			}
			deletedSnapshots = append(deletedSnapshots, subvolume.Path)
		}
	}
	if isCloudPool {
		if err := storage.CleanupOAuthBrokerCloudStorage(ctx, storageRecord); err != nil {
			return nil, err
		}
	}
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
	if err := DeleteByID(pool.ID); err != nil {
		return nil, fmt.Errorf("delete storage pool record: %w", err)
	}
	if err := storage.Delete(pool.StorageID); err != nil {
		return nil, fmt.Errorf("delete storage record: %w", err)
	}
	if err := cleanupPoolMountPath(ctx, pool.MountPath); err != nil {
		return nil, fmt.Errorf("cleanup mount path: %w", err)
	}

	releasedDevices := make([]string, 0, len(pool.Devices))
	if req.WipeDevices {
		for _, device := range pool.Devices {
			if _, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "mdadm", "--zero-superblock", "--force", device.DevicePath); err != nil {
				log.Printf("[POOL] continue after zero-superblock failure on %s during delete: %v", device.DevicePath, err)
			}
			if _, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "wipefs", "-af", device.DevicePath); err != nil {
				return nil, fmt.Errorf("wipe device %s: %w", device.DevicePath, err)
			}
			releasedDevices = append(releasedDevices, device.DevicePath)
		}
	}

	return map[string]any{
		"id":               pool.ID,
		"deleted":          true,
		"mountPath":        pool.MountPath,
		"releasedDevices":  releasedDevices,
		"deletedSnapshots": deletedSnapshots,
		"wipedDevices":     req.WipeDevices,
	}, nil
}

func cleanupPoolMountPath(ctx context.Context, mountPath string) error {
	if mountPath == "" {
		return nil
	}
	if isMountpointActive(mountPath) {
		return fmt.Errorf("mount path is still active: %s", mountPath)
	}
	if _, err := os.Stat(mountPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if _, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "rm", "-rf", mountPath); err != nil {
		return err
	}
	return nil
}
