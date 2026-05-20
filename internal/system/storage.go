package system

import (
	"net/http"

	"nas-server/internal/storage"
)

func (h *Handler) HandleStorageList(w http.ResponseWriter, r *http.Request) {
	storages, err := storage.List()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "STORAGE_LIST_FAILED", "获取存储列表失败: "+err.Error())
		return
	}
	if storages == nil {
		storages = []storage.Storage{}
	}
	writeJSON(w, storages)
}
