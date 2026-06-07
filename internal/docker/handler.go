package docker

import (
	"net/http"
	"strconv"
	"time"

	"nas-server/pkg/httpx"
)

type Handler struct{}

var (
	writeJSON     = httpx.WriteJSON
	writeAPIError = httpx.WriteAPIError
	prepareSSE    = httpx.PrepareSSE
	writeSSEEvent = httpx.WriteSSEEvent
)

const defaultContainerStreamInterval = time.Second

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

func (h *Handler) HandleListContainersStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeAPIError(w, http.StatusInternalServerError, "SSE_NOT_SUPPORTED", "SSE is not supported by this server")
		return
	}

	interval := containerStreamInterval(r)
	prepareSSE(w)
	writeSSEEvent(w, flusher, "ready", map[string]any{
		"sampleIntervalSeconds": int(interval.Seconds()),
		"scope":                 "docker-containers",
	})

	for {
		select {
		case <-r.Context().Done():
			return
		default:
		}

		items, err := ListContainers(r.Context())
		if err != nil {
			if !writeSSEEvent(w, flusher, "error", map[string]any{
				"code":    "DOCKER_CONTAINERS_LIST_FAILED",
				"message": "Failed to list docker containers: " + err.Error(),
			}) {
				return
			}
			continue
		}
		if items == nil {
			items = []Container{}
		}
		if !writeSSEEvent(w, flusher, "docker-containers", map[string]any{
			"items":     items,
			"checkedAt": time.Now().Format(time.RFC3339),
		}) {
			return
		}

		select {
		case <-r.Context().Done():
			return
		case <-time.After(interval):
		}
	}
}

func containerStreamInterval(r *http.Request) time.Duration {
	seconds, err := strconv.Atoi(r.URL.Query().Get("interval"))
	if err != nil || seconds <= 0 {
		return defaultContainerStreamInterval
	}
	if seconds > 10 {
		seconds = 10
	}
	return time.Duration(seconds) * time.Second
}
