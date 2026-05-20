package storagepool

import (
	"net/http"

	"nas-server/internal/storage"
)

func (h *Handler) HandleListPools(w http.ResponseWriter, r *http.Request) {
	pools, err := List()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "LIST_STORAGE_POOLS_FAILED", "Failed to list storage pools: "+err.Error())
		return
	}

	storages, err := storage.List()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "LIST_STORAGE_POOLS_FAILED", "Failed to load cloud storages: "+err.Error())
		return
	}

	items := BuildResponses(r.Context(), pools)
	items = append(items, BuildCloudResponses(r.Context(), storages)...)
	writeJSON(w, items)
}
