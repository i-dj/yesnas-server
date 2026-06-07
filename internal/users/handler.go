package users

import (
	"encoding/json"
	"net/http"

	"nas-server/internal/audit"
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
	item, _, err := Create(req)
	if err != nil {
		audit.UserAction(r.Context(), "user_create_failed", "create", false, "user", "", req.Username, err.Error(), nil)
		writeAPIError(w, http.StatusBadRequest, "USER_CREATE_FAILED", err.Error())
		return
	}
	audit.UserAction(r.Context(), "user_created", "create", true, "user", item.ID, item.Username, "User created", map[string]any{
		"displayName": item.DisplayName,
		"isAdmin":     item.IsAdmin,
		"status":      item.Status,
	})
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, item)
}

func (h *Handler) HandleUpdate(w http.ResponseWriter, r *http.Request) {
	var req UpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	item, _, err := Update(r.PathValue("userId"), req)
	if err != nil {
		audit.UserAction(r.Context(), "user_update_failed", "update", false, "user", r.PathValue("userId"), "", err.Error(), nil)
		writeAPIError(w, http.StatusBadRequest, "USER_UPDATE_FAILED", err.Error())
		return
	}
	audit.UserAction(r.Context(), "user_updated", "update", true, "user", item.ID, item.Username, "User updated", map[string]any{
		"displayName": item.DisplayName,
		"isAdmin":     item.IsAdmin,
		"status":      item.Status,
	})
	writeJSON(w, item)
}

func (h *Handler) HandleDelete(w http.ResponseWriter, r *http.Request) {
	item, err := Get(r.PathValue("userId"))
	if err != nil {
		audit.UserAction(r.Context(), "user_delete_failed", "delete", false, "user", r.PathValue("userId"), "", "User not found", nil)
		writeAPIError(w, http.StatusNotFound, "USER_NOT_FOUND", "User not found")
		return
	}
	if item.IsAdmin {
		adminCount, err := CountAdmins()
		if err != nil {
			audit.UserAction(r.Context(), "user_delete_failed", "delete", false, "user", item.ID, item.Username, "Failed to check administrator count: "+err.Error(), nil)
			writeAPIError(w, http.StatusInternalServerError, "USER_DELETE_FAILED", "Failed to check administrator count: "+err.Error())
			return
		}
		if adminCount <= 1 {
			audit.UserDeleteBlocked(r.Context(), item.ID, item.Username, "Cannot delete the last administrator")
			writeAPIError(w, http.StatusConflict, "LAST_ADMIN_DELETE_FORBIDDEN", "Cannot delete the last administrator")
			return
		}
	}
	if err := Delete(item.ID); err != nil {
		audit.UserAction(r.Context(), "user_delete_failed", "delete", false, "user", item.ID, item.Username, "Failed to delete user: "+err.Error(), nil)
		writeAPIError(w, http.StatusInternalServerError, "USER_DELETE_FAILED", "Failed to delete user: "+err.Error())
		return
	}
	audit.UserAction(r.Context(), "user_deleted", "delete", true, "user", item.ID, item.Username, "User deleted", nil)
	writeJSON(w, map[string]any{"deleted": true, "id": item.ID})
}
