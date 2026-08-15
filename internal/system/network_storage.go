package system

import (
	"encoding/json"
	"net/http"

	"nas-server/internal/storage"
)

func (h *Handler) HandleCreateNetworkStorage(w http.ResponseWriter, r *http.Request) {
	var req storage.CreateNetworkRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}

	result, err := storage.CreateNetworkStorage(r.Context(), req)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "NETWORK_STORAGE_CREATE_FAILED", err.Error())
		return
	}

	writeJSON(w, result)
}

func (h *Handler) HandleListSMBShares(w http.ResponseWriter, r *http.Request) {
	var req storage.ListSMBSharesRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}

	result, err := storage.ListSMBShares(r.Context(), req)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "SMB_SHARES_LIST_FAILED", err.Error())
		return
	}

	writeJSON(w, result)
}
