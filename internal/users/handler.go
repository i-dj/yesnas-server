package users

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
		writeAPIError(w, http.StatusBadRequest, "USER_CREATE_FAILED", err.Error())
		return
	}
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
		writeAPIError(w, http.StatusBadRequest, "USER_UPDATE_FAILED", err.Error())
		return
	}
	writeJSON(w, item)
}

func (h *Handler) HandleDelete(w http.ResponseWriter, r *http.Request) {
	item, err := Get(r.PathValue("userId"))
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "USER_NOT_FOUND", "User not found")
		return
	}
	if err := Delete(item.ID); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "USER_DELETE_FAILED", "Failed to delete user: "+err.Error())
		return
	}
	writeJSON(w, map[string]any{"deleted": true, "id": item.ID})
}
