package storagepool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"nas-server/internal/storage"
	commandrunner "nas-server/pkg/shell"
)

var snapshotNameSanitizer = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

func (h *Handler) HandleListSnapshots(w http.ResponseWriter, r *http.Request) {
	pool, err := Get(strings.TrimSpace(r.PathValue("poolId")))
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "STORAGE_POOL_NOT_FOUND", "Storage pool not found")
		return
	}
	items, warnings := ListSnapshots(r.Context(), pool)
	writeJSON(w, map[string]any{
		"items":       items,
		"warnings":    warnings,
		"generatedAt": time.Now(),
	})
}

func (h *Handler) HandleCreateSnapshot(w http.ResponseWriter, r *http.Request) {
	pool, err := Get(strings.TrimSpace(r.PathValue("poolId")))
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "STORAGE_POOL_NOT_FOUND", "Storage pool not found")
		return
	}
	var req CreateSnapshotRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	snapshot, err := CreateSnapshot(r.Context(), pool, req)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "CREATE_STORAGE_POOL_SNAPSHOT_FAILED", err.Error())
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, snapshot)
}

func (h *Handler) HandleDeleteSnapshot(w http.ResponseWriter, r *http.Request) {
	pool, err := Get(strings.TrimSpace(r.PathValue("poolId")))
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "STORAGE_POOL_NOT_FOUND", "Storage pool not found")
		return
	}
	result, err := DeleteSnapshot(r.Context(), pool, strings.TrimSpace(r.PathValue("snapshotId")))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "DELETE_STORAGE_POOL_SNAPSHOT_FAILED", err.Error())
		return
	}
	writeJSON(w, result)
}

func (h *Handler) HandleRestoreSnapshot(w http.ResponseWriter, r *http.Request) {
	pool, err := Get(strings.TrimSpace(r.PathValue("poolId")))
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "STORAGE_POOL_NOT_FOUND", "Storage pool not found")
		return
	}

	req := RestoreSnapshotRequest{}
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeAPIError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
			return
		}
	}

	result, err := RestoreSnapshot(r.Context(), pool, strings.TrimSpace(r.PathValue("snapshotId")), req)
	if err != nil {
		switch {
		case errors.Is(err, errFormatPasswordRequired):
			writeAPIError(w, http.StatusBadRequest, "RESTORE_PASSWORD_REQUIRED", "Password is required")
		case errors.Is(err, errFormatPasswordInvalid):
			writeAPIError(w, http.StatusForbidden, "RESTORE_PASSWORD_INVALID", "Password verification failed")
		case errors.Is(err, errFormatPasswordNotConfigured):
			writeAPIError(w, http.StatusServiceUnavailable, "RESTORE_PASSWORD_NOT_CONFIGURED", "Restore password is not configured")
		default:
			writeAPIError(w, http.StatusBadRequest, "RESTORE_STORAGE_POOL_SNAPSHOT_FAILED", err.Error())
		}
		return
	}
	writeJSON(w, result)
}

func ListSnapshots(ctx context.Context, pool *StoragePool) ([]Snapshot, []string) {
	snapshots, warnings := readBtrfsSnapshots(ctx, pool.MountPath)
	merged := mergePoolSnapshots(pool.ID, pool.MountPath, snapshots)
	sizeWarnings := fillSnapshotSizes(ctx, pool.MountPath, merged)
	return merged, append(warnings, sizeWarnings...)
}

func CreateSnapshot(ctx context.Context, pool *StoragePool, req CreateSnapshotRequest) (*Snapshot, error) {
	displayName := strings.TrimSpace(req.Name)
	pathName := sanitizeSnapshotName(req.Name)
	if pathName == "" {
		pathName = time.Now().Format("20060102-150405")
	}
	if displayName == "" {
		displayName = pathName
	}
	sourcePath, err := resolveSnapshotSourcePath(pool.MountPath, req.SourcePath)
	if err != nil {
		return nil, err
	}
	readOnly := true
	if req.ReadOnly != nil {
		readOnly = *req.ReadOnly
	}
	snapshotsDir := filepath.Join(pool.MountPath, ".snapshots")
	if _, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "mkdir", "-p", snapshotsDir); err != nil {
		return nil, fmt.Errorf("create snapshots directory: %w", err)
	}
	targetPath := filepath.Join(snapshotsDir, pathName)
	if _, err := os.Stat(targetPath); err == nil {
		return nil, fmt.Errorf("snapshot already exists: %s", pathName)
	}
	args := []string{"subvolume", "snapshot"}
	if readOnly {
		args = append(args, "-r")
	}
	args = append(args, sourcePath, targetPath)
	if _, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "btrfs", args...); err != nil {
		return nil, fmt.Errorf("create btrfs snapshot: %w", err)
	}
	now := time.Now()
	meta := SnapshotRecord{
		PoolID:      pool.ID,
		Name:        displayName,
		Path:        targetPath,
		SourcePath:  sourcePath,
		IsReadOnly:  readOnly,
		Description: strings.TrimSpace(req.Description),
		CreatedBy:   snapshotCreatedBy(req.CreatedBy),
		UpdatedAt:   &now,
	}
	if err := AddSnapshot(meta); err != nil {
		return nil, fmt.Errorf("save snapshot metadata: %w", err)
	}
	registered, warnings := readBtrfsSnapshots(ctx, pool.MountPath)
	merged := mergePoolSnapshots(pool.ID, pool.MountPath, registered)
	for _, item := range merged {
		if item.Path == normalizeSnapshotPath(pool.MountPath, targetPath) {
			if len(warnings) > 0 && item.Description == "" {
				item.Description = strings.TrimSpace(req.Description)
			}
			return &item, nil
		}
	}
	createdAt := now
	return &Snapshot{
		Name:        displayName,
		Path:        targetPath,
		SourcePath:  sourcePath,
		Description: strings.TrimSpace(req.Description),
		IsReadOnly:  readOnly,
		Registered:  true,
		CreatedBy:   snapshotCreatedBy(req.CreatedBy),
		CreatedAt:   &createdAt,
	}, nil
}

func snapshotCreatedBy(value string) string {
	createdBy := strings.TrimSpace(value)
	if createdBy == "" {
		return "api"
	}
	return createdBy
}

func DeleteSnapshot(ctx context.Context, pool *StoragePool, snapshotID string) (map[string]any, error) {
	snapshotMeta, err := GetSnapshotRecord(pool.ID, strings.TrimSpace(snapshotID))
	if err != nil {
		return nil, err
	}
	if _, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "btrfs", "subvolume", "delete", snapshotMeta.Path); err != nil {
		return nil, fmt.Errorf("delete snapshot: %w", err)
	}
	if err := DeleteSnapshotRecord(pool.ID, snapshotMeta.ID); err != nil {
		return nil, fmt.Errorf("delete snapshot metadata: %w", err)
	}
	return map[string]any{"id": snapshotMeta.ID, "deleted": true}, nil
}

func RestoreSnapshot(ctx context.Context, pool *StoragePool, snapshotID string, req RestoreSnapshotRequest) (map[string]any, error) {
	if err := verifyFormatPassword(req.Password); err != nil {
		return nil, err
	}
	snapshotMeta, err := GetSnapshotRecord(pool.ID, strings.TrimSpace(snapshotID))
	if err != nil {
		return nil, err
	}
	if !isMountpointActive(pool.MountPath) {
		return nil, fmt.Errorf("storage pool is offline")
	}

	dataPath := pool.DataPath
	if strings.TrimSpace(dataPath) == "" {
		dataPath = filepath.Join(pool.MountPath, "data")
	}
	if strings.TrimSpace(snapshotMeta.SourcePath) == "" {
		return nil, fmt.Errorf("snapshot source path is empty")
	}
	if filepath.Clean(snapshotMeta.SourcePath) != filepath.Clean(dataPath) {
		return nil, fmt.Errorf("snapshot source path does not match pool data path")
	}

	backupPath := ""
	if req.CreateBackup {
		backupPath = filepath.Join(pool.MountPath, ".snapshots", "restore-backup-"+time.Now().Format("20060102-150405"))
		if _, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "btrfs", "subvolume", "snapshot", "-r", dataPath, backupPath); err != nil {
			return nil, fmt.Errorf("create restore backup snapshot: %w", err)
		}
		now := time.Now()
		backupMeta := SnapshotRecord{
			PoolID:      pool.ID,
			Name:        filepath.Base(backupPath),
			Path:        backupPath,
			SourcePath:  dataPath,
			IsReadOnly:  true,
			Description: "automatic backup before restore",
			CreatedBy:   "restore",
			UpdatedAt:   &now,
		}
		if err := AddSnapshot(backupMeta); err != nil {
			return nil, fmt.Errorf("save restore backup snapshot metadata: %w", err)
		}
	}

	if _, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "btrfs", "subvolume", "delete", dataPath); err != nil {
		return nil, fmt.Errorf("delete current data subvolume: %w", err)
	}
	if _, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "btrfs", "subvolume", "snapshot", snapshotMeta.Path, dataPath); err != nil {
		return nil, fmt.Errorf("restore snapshot to data subvolume: %w", err)
	}

	totalSize, freeSize := statFilesystem(dataPath)
	if err := storage.UpdateRuntime(
		pool.StorageID,
		dataPath,
		storage.StatusOnline,
		totalSize,
		freeSize,
		storage.BuildExtraConfig(map[string]any{
			"poolName":   pool.Name,
			"raidLevel":  pool.RaidLevel,
			"filesystem": pool.Filesystem,
		}),
	); err != nil {
		return nil, fmt.Errorf("update storage runtime: %w", err)
	}

	return map[string]any{
		"id":         snapshotMeta.ID,
		"restored":   true,
		"name":       snapshotMeta.Name,
		"targetPath": dataPath,
		"backupPath": backupPath,
	}, nil
}

func resolveSnapshotSourcePath(poolMountPath string, requested string) (string, error) {
	sourcePath := strings.TrimSpace(requested)
	if sourcePath == "" {
		if poolMountPath != "" {
			dataPath := filepath.Join(poolMountPath, "data")
			if info, err := os.Stat(dataPath); err == nil && info.IsDir() {
				return dataPath, nil
			}
		}
		return poolMountPath, nil
	}
	if !filepath.IsAbs(sourcePath) {
		sourcePath = filepath.Join(poolMountPath, sourcePath)
	}
	cleanPoolPath := filepath.Clean(poolMountPath)
	cleanSourcePath := filepath.Clean(sourcePath)
	if cleanSourcePath != cleanPoolPath && !strings.HasPrefix(cleanSourcePath, cleanPoolPath+string(os.PathSeparator)) {
		return "", fmt.Errorf("snapshot source path must stay inside pool mount path")
	}
	return cleanSourcePath, nil
}

func sanitizeSnapshotName(name string) string {
	clean := strings.TrimSpace(name)
	clean = strings.ToLower(snapshotNameSanitizer.ReplaceAllString(clean, "-"))
	clean = strings.Trim(clean, "-.")
	return clean
}
