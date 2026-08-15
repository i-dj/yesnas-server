package docker

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

func (h *Handler) HandleListImages(w http.ResponseWriter, r *http.Request) {
	items, err := ListImages(r.Context())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "DOCKER_IMAGES_LIST_FAILED", "Failed to list docker images: "+err.Error())
		return
	}
	if items == nil {
		items = []Image{}
	}
	writeJSON(w, map[string]any{"items": items})
}

func (h *Handler) HandleListNetworks(w http.ResponseWriter, r *http.Request) {
	items, err := ListNetworks(r.Context())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "DOCKER_NETWORKS_LIST_FAILED", "Failed to list docker networks: "+err.Error())
		return
	}
	if items == nil {
		items = []Network{}
	}
	writeJSON(w, map[string]any{"items": items})
}

func (h *Handler) HandleListVolumes(w http.ResponseWriter, r *http.Request) {
	items, err := ListVolumes(r.Context())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "DOCKER_VOLUMES_LIST_FAILED", "Failed to list docker volumes: "+err.Error())
		return
	}
	if items == nil {
		items = []Volume{}
	}
	writeJSON(w, map[string]any{"items": items})
}

func (h *Handler) HandleListComposeProjects(w http.ResponseWriter, r *http.Request) {
	items, err := ListComposeProjects(r.Context())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "DOCKER_COMPOSE_LIST_FAILED", "Failed to list docker compose projects: "+err.Error())
		return
	}
	if items == nil {
		items = []ComposeProject{}
	}
	writeJSON(w, map[string]any{"items": items})
}

func (h *Handler) HandlePullImageStream(w http.ResponseWriter, r *http.Request) {
	var req ImagePullRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "DOCKER_IMAGE_PULL_INVALID_REQUEST", "Invalid pull request: "+err.Error())
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeAPIError(w, http.StatusInternalServerError, "SSE_NOT_SUPPORTED", "SSE is not supported by this server")
		return
	}
	httpx.PrepareSSE(w)
	emit := func(event ImagePullEvent) bool {
		return httpx.WriteSSEEvent(w, flusher, "docker.image.pull", event)
	}

	if err := PullImageStream(r.Context(), PullImageStreamOptions{Command: req.Command}, emit); err != nil {
		httpx.WriteSSEEvent(w, flusher, "error", httpx.APIError{
			Code:    "DOCKER_IMAGE_PULL_FAILED",
			Message: err.Error(),
		})
	}
}
