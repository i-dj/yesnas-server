package users

import (
	"context"
	"encoding/json"
	"net/http"

	"nas-server/pkg/httpx"
)

type Handler struct{}

var (
	writeJSON        = httpx.WriteJSON
	writeAPIError    = httpx.WriteAPIError
	AfterUserChanged func(context.Context) error
)

func NewHandler() *Handler {
	return &Handler{}
}

func (h *Handler) HandleList(w http.ResponseWriter, r *http.Request) {
	items, err := List()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "USERS_LIST_FAILED", "Failed to list users: "+err.Error())
		return
	}
	if items == nil {
		items = []User{}
	}
	writeJSON(w, map[string]any{"items": items})
}

func (h *Handler) HandleCreate(w http.ResponseWriter, r *http.Request) {
	var req CreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	item, password, err := Create(req)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "USER_CREATE_FAILED", err.Error())
		return
	}
	if err := SyncSambaAccount(r.Context(), *item, password, false); err != nil {
		_ = UpdateSMBState(item.ID, SMBStatusError)
		writeAPIError(w, http.StatusInternalServerError, "USER_SMB_SYNC_FAILED", err.Error())
		return
	}
	item, _ = Get(item.ID)
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, item)
}

func (h *Handler) HandleUpdate(w http.ResponseWriter, r *http.Request) {
	var req UpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	item, password, err := Update(r.PathValue("userId"), req)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "USER_UPDATE_FAILED", err.Error())
		return
	}
	if password != "" {
		enabled := item.SMBStatus == string(SMBStatusActive)
		if err := SyncSambaAccount(r.Context(), *item, password, enabled); err != nil {
			_ = UpdateSMBState(item.ID, SMBStatusError)
			writeAPIError(w, http.StatusInternalServerError, "USER_SMB_SYNC_FAILED", err.Error())
			return
		}
		item, _ = Get(item.ID)
	}
	if (password != "" || req.Status != nil) && AfterUserChanged != nil {
		if err := AfterUserChanged(r.Context()); err != nil {
			writeAPIError(w, http.StatusInternalServerError, "SMB_APPLY_FAILED", err.Error())
			return
		}
	}
	writeJSON(w, item)
}

func (h *Handler) HandleDelete(w http.ResponseWriter, r *http.Request) {
	item, err := Get(r.PathValue("userId"))
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "USER_NOT_FOUND", "User not found")
		return
	}
	_ = DisableSambaAccount(r.Context(), *item)
	if err := Delete(item.ID); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "USER_DELETE_FAILED", "Failed to delete user: "+err.Error())
		return
	}
	if AfterUserChanged != nil {
		if err := AfterUserChanged(r.Context()); err != nil {
			writeAPIError(w, http.StatusInternalServerError, "SMB_APPLY_FAILED", err.Error())
			return
		}
	}
	writeJSON(w, map[string]any{"deleted": true, "id": item.ID})
}
