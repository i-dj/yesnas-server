package docker

import (
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

func (h *Handler) HandleListContainers(w http.ResponseWriter, r *http.Request) {
	items, err := ListContainers(r.Context())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "DOCKER_CONTAINERS_LIST_FAILED", "Failed to list docker containers: "+err.Error())
		return
	}
	if items == nil {
		items = []Container{}
	}
	writeJSON(w, map[string]any{"items": items})
}
