package storagepool

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"nas-server/internal/audit"
)

func (h *Handler) HandleUpdatePool(w http.ResponseWriter, r *http.Request) {
	pool, err := Get(strings.TrimSpace(r.PathValue("poolId")))
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "STORAGE_POOL_NOT_FOUND", "Storage pool not found")
		return
	}

	var req UpdateRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}

	result, err := UpdatePool(r.Context(), pool, req)
	if err != nil {
		audit.UserAction(r.Context(), "storage_pool_update_failed", "update", false, "storage_pool", pool.ID, pool.Name, err.Error(), nil)
		writeAPIError(w, http.StatusBadRequest, "UPDATE_STORAGE_POOL_FAILED", err.Error())
		return
	}

	audit.UserAction(r.Context(), "storage_pool_updated", "update", true, "storage_pool", result.ID, result.Name, "Storage pool updated", map[string]any{
		"autoSnapshotEnabled":  result.AutoSnapshotEnabled,
		"autoSnapshotSchedule": result.AutoSnapshotSchedule,
		"nextAutoSnapshotAt":   result.NextAutoSnapshotAt,
	})
	writeJSON(w, result)
}

func UpdatePool(ctx context.Context, pool *StoragePool, req UpdateRequest) (*StoragePool, error) {
	if pool == nil {
		return nil, fmt.Errorf("storage pool is required")
	}

	schedule, err := normalizeAutoSnapshot(req.AutoSnapshotEnabled, req.AutoSnapshotSchedule)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	next := initialAutoSnapshotNextRun(req.AutoSnapshotEnabled, schedule, now)
	if err := UpdateAutoSnapshotConfig(pool.ID, req.AutoSnapshotEnabled, schedule, next, now); err != nil {
		return nil, fmt.Errorf("update storage pool snapshot settings: %w", err)
	}

	updated, err := Get(pool.ID)
	if err != nil {
		return nil, fmt.Errorf("reload storage pool: %w", err)
	}
	return updated, nil
}
