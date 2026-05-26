package smb

import (
	"encoding/json"
	"net/http"

	"nas-server/pkg/httpx"
)

type Handler struct{}

var (
	writeJSON     = httpx.WriteJSON
	writeAPIError = httpx.WriteAPIError
)

func NewHandler() *Handler {
	return &Handler{}
}

func (h *Handler) HandleListShares(w http.ResponseWriter, r *http.Request) {
	items, err := ListShares()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "SMB_SHARES_LIST_FAILED", "Failed to list SMB shares: "+err.Error())
		return
	}
	if items == nil {
		items = []Share{}
	}
	writeJSON(w, map[string]any{"items": items})
}

func (h *Handler) HandleCreateShare(w http.ResponseWriter, r *http.Request) {
	var req UpsertShareRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	item, err := UpsertShare("", req)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "SMB_SHARE_CREATE_FAILED", err.Error())
		return
	}
	if err := ApplyConfig(r.Context()); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "SMB_APPLY_FAILED", err.Error())
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, item)
}

func (h *Handler) HandleUpdateShare(w http.ResponseWriter, r *http.Request) {
	var req UpsertShareRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	item, err := UpsertShare(r.PathValue("shareId"), req)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "SMB_SHARE_UPDATE_FAILED", err.Error())
		return
	}
	if err := ApplyConfig(r.Context()); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "SMB_APPLY_FAILED", err.Error())
		return
	}
	writeJSON(w, item)
}

func (h *Handler) HandleDeleteShare(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("shareId")
	if err := DeleteShare(id); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "SMB_SHARE_DELETE_FAILED", "Failed to delete SMB share: "+err.Error())
		return
	}
	if err := ApplyConfig(r.Context()); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "SMB_APPLY_FAILED", err.Error())
		return
	}
	writeJSON(w, map[string]any{"deleted": true, "id": id})
}

func (h *Handler) HandleApply(w http.ResponseWriter, r *http.Request) {
	if err := ApplyConfig(r.Context()); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "SMB_APPLY_FAILED", err.Error())
		return
	}
	writeJSON(w, map[string]any{"applied": true})
}
