package storagepool

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"nas-server/internal/audit"
	commandrunner "nas-server/pkg/shell"
)

func (h *Handler) HandleReplaceDevice(w http.ResponseWriter, r *http.Request) {
	pool, err := Get(strings.TrimSpace(r.PathValue("poolId")))
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "STORAGE_POOL_NOT_FOUND", "Storage pool not found")
		return
	}

	var req ReplaceDeviceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}

	result, err := ReplaceDevice(r.Context(), pool, req)
	if err != nil {
		switch {
		case err == errFormatPasswordRequired:
			writeAPIError(w, http.StatusBadRequest, "REPLACE_DEVICE_PASSWORD_REQUIRED", "Password is required")
		case err == errFormatPasswordInvalid:
			writeAPIError(w, http.StatusForbidden, "REPLACE_DEVICE_PASSWORD_INVALID", "Password verification failed")
		case err == errFormatPasswordNotConfigured:
			writeAPIError(w, http.StatusServiceUnavailable, "REPLACE_DEVICE_PASSWORD_NOT_CONFIGURED", "Replace device password is not configured")
		default:
			audit.UserAction(r.Context(), "storage_pool_replace_device_failed", "replace_device", false, "storage_pool", pool.ID, pool.Name, err.Error(), nil)
			writeAPIError(w, http.StatusBadRequest, "REPLACE_STORAGE_POOL_DEVICE_FAILED", err.Error())
		}
		return
	}
	audit.UserAction(r.Context(), "storage_pool_device_replaced", "replace_device", true, "storage_pool", pool.ID, pool.Name, "Storage pool device replaced", result)
	writeJSON(w, result)
}

func ReplaceDevice(ctx context.Context, pool *StoragePool, req ReplaceDeviceRequest) (map[string]any, error) {
	if err := verifyFormatPassword(req.Password); err != nil {
		return nil, err
	}
	if pool == nil {
		return nil, fmt.Errorf("storage pool is required")
	}
	if !isMountpointActive(pool.MountPath) {
		return nil, fmt.Errorf("storage pool is offline")
	}
	if strings.TrimSpace(pool.Filesystem) != "btrfs" {
		return nil, fmt.Errorf("replace device is only supported for btrfs pools")
	}

	oldDevicePath := strings.TrimSpace(req.OldDevicePath)
	newDevicePath := strings.TrimSpace(req.NewDevicePath)
	if oldDevicePath == "" || newDevicePath == "" {
		return nil, fmt.Errorf("oldDevicePath and newDevicePath are required")
	}
	if oldDevicePath == newDevicePath {
		return nil, fmt.Errorf("oldDevicePath and newDevicePath must be different")
	}

	oldIndex := -1
	for i := range pool.Devices {
		if strings.TrimSpace(pool.Devices[i].DevicePath) == oldDevicePath {
			oldIndex = i
			break
		}
	}
	if oldIndex < 0 {
		return nil, fmt.Errorf("old device is not part of the storage pool: %s", oldDevicePath)
	}

	if registered, err := HasDevice(newDevicePath); err != nil {
		return nil, fmt.Errorf("check new device registration: %w", err)
	} else if registered {
		return nil, fmt.Errorf("new device is already registered in another storage pool: %s", newDevicePath)
	}

	if err := forcePreparePoolDevices(ctx, []string{newDevicePath}); err != nil {
		return nil, err
	}

	newDevices, err := resolvePoolDevices(ctx, []string{newDevicePath})
	if err != nil {
		return nil, err
	}
	newDevice := newDevices[0]
	sourceArg, err := resolveReplaceSourceArg(ctx, pool, oldDevicePath)
	if err != nil {
		return nil, err
	}

	if _, err := commandrunner.RunWithOptions(
		ctx,
		commandrunner.Options{UseSudo: true},
		"btrfs",
		"replace",
		"start",
		"-B",
		sourceArg,
		newDevicePath,
		pool.MountPath,
	); err != nil {
		return nil, fmt.Errorf("replace btrfs device: %w", err)
	}

	oldRecord := pool.Devices[oldIndex]
	oldRecord.DevicePath = newDevice.DevicePath
	oldRecord.DeviceName = newDevice.DeviceName
	oldRecord.KernelName = newDevice.KernelName
	oldRecord.ParentPath = newDevice.ParentPath
	oldRecord.SizeBytes = newDevice.SizeBytes
	oldRecord.SizeHuman = newDevice.SizeHuman
	oldRecord.Model = newDevice.Model
	oldRecord.Serial = newDevice.Serial
	oldRecord.Vendor = newDevice.Vendor
	oldRecord.Transport = newDevice.Transport
	oldRecord.DeviceRole = newDevice.DeviceRole

	if err := UpdateDevice(oldRecord); err != nil {
		return nil, fmt.Errorf("update storage pool device record: %w", err)
	}
	if err := ResetBenchmarkResult(pool.ID, time.Now().UTC()); err != nil {
		return nil, fmt.Errorf("reset benchmark result: %w", err)
	}

	reloaded, err := Get(pool.ID)
	if err != nil {
		return nil, fmt.Errorf("reload storage pool: %w", err)
	}

	return map[string]any{
		"id":             pool.ID,
		"replaced":       true,
		"oldDevicePath":  oldDevicePath,
		"sourceArg":      sourceArg,
		"newDevicePath":  newDevicePath,
		"filesystemPath": pool.MountPath,
		"pool":           buildResponse(ctx, *reloaded),
	}, nil
}

func resolveReplaceSourceArg(ctx context.Context, pool *StoragePool, oldDevicePath string) (string, error) {
	oldDeviceAvailable := isPoolReplaceSourceAvailable(oldDevicePath)
	result, err := commandrunner.RunWithOptions(
		ctx,
		commandrunner.Options{UseSudo: true},
		"btrfs",
		"filesystem",
		"show",
		pool.MountPath,
	)
	if err != nil {
		return "", fmt.Errorf("inspect btrfs devices: %w", err)
	}

	type btrfsDevice struct {
		devid   string
		path    string
		missing bool
	}
	devices := make([]btrfsDevice, 0)
	for _, line := range strings.Split(result.Stdout, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 2 || fields[0] != "devid" {
			continue
		}
		device := btrfsDevice{devid: fields[1]}
		for i := 2; i < len(fields); i++ {
			if fields[i] != "path" || i+1 >= len(fields) {
				continue
			}
			device.path = fields[i+1]
			if device.path == "missing" {
				device.missing = true
			}
			break
		}
		devices = append(devices, device)
	}

	for _, device := range devices {
		if device.path == oldDevicePath {
			if oldDeviceAvailable {
				return oldDevicePath, nil
			}
			if _, err := strconv.Atoi(device.devid); err == nil {
				return device.devid, nil
			}
		}
	}

	missingDevices := make([]btrfsDevice, 0)
	for _, device := range devices {
		if device.missing || device.path == "" {
			missingDevices = append(missingDevices, device)
			continue
		}
	}

	if len(missingDevices) == 1 {
		if _, err := strconv.Atoi(missingDevices[0].devid); err == nil {
			return missingDevices[0].devid, nil
		}
	}

	if oldDeviceAvailable {
		return oldDevicePath, nil
	}

	return "", fmt.Errorf("unable to resolve replace source for offline device %s", oldDevicePath)
}

func isPoolReplaceSourceAvailable(devicePath string) bool {
	cleanPath := strings.TrimSpace(devicePath)
	if cleanPath == "" {
		return false
	}
	if _, err := os.Stat(cleanPath); err != nil {
		return false
	}
	kernelName := filepath.Base(cleanPath)
	if _, err := os.Stat(filepath.Join("/sys/class/block", kernelName)); err != nil {
		return false
	}
	return true
}
