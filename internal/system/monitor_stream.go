package system

import (
	"net/http"
	"strconv"
	"time"
)

const defaultSystemStatusInterval = 2 * time.Second

func (h *Handler) HandleSystemStatus(w http.ResponseWriter, r *http.Request) {
	interval := systemStatusInterval(r)
	snapshot, err := CollectSystemStatus(r.Context(), interval)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "SYSTEM_STATUS_FAILED", "Failed to collect system status: "+err.Error())
		return
	}
	writeJSON(w, snapshot)
}

func (h *Handler) HandleSystemStatusStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeAPIError(w, http.StatusInternalServerError, "SSE_NOT_SUPPORTED", "SSE is not supported by this server")
		return
	}

	interval := systemStatusInterval(r)
	prepareSSE(w)
	writeSSEEvent(w, flusher, "ready", map[string]any{
		"sampleIntervalSeconds": int(interval.Seconds()),
		"scope":                 "system-status",
	})

	for {
		select {
		case <-r.Context().Done():
			return
		default:
		}

		snapshot, err := CollectSystemStatus(r.Context(), interval)
		if err != nil {
			if !writeSSEEvent(w, flusher, "error", APIError{
				Code:    "SYSTEM_STATUS_FAILED",
				Message: "Failed to collect system status: " + err.Error(),
			}) {
				return
			}
			continue
		}
		if !writeSSEEvent(w, flusher, "system-status", snapshot) {
			return
		}
	}
}

func systemStatusInterval(r *http.Request) time.Duration {
	seconds, err := strconv.Atoi(r.URL.Query().Get("interval"))
	if err != nil || seconds <= 0 {
		return defaultSystemStatusInterval
	}
	if seconds > 10 {
		seconds = 10
	}
	return time.Duration(seconds) * time.Second
}
