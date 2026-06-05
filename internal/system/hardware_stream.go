package system

import (
	"net/http"
	"strconv"
	"time"
)

func (h *Handler) HandleHardware(w http.ResponseWriter, r *http.Request) {
	interval := hardwareInterval(r)
	snapshot, err := CollectHardwareSnapshot(r.Context(), interval)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "HARDWARE_STATUS_FAILED", "Failed to collect hardware status: "+err.Error())
		return
	}
	writeJSON(w, snapshot)
}

func (h *Handler) HandleHardwareStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeAPIError(w, http.StatusInternalServerError, "SSE_NOT_SUPPORTED", "SSE is not supported by this server")
		return
	}

	interval := hardwareInterval(r)
	prepareSSE(w)
	writeSSEEvent(w, flusher, "ready", map[string]any{
		"sampleIntervalSeconds": int(interval.Seconds()),
		"scope":                 "hardware-status",
	})

	for {
		select {
		case <-r.Context().Done():
			return
		default:
		}

		snapshot, err := CollectHardwareSnapshot(r.Context(), interval)
		if err != nil {
			if !writeSSEEvent(w, flusher, "error", APIError{
				Code:    "HARDWARE_STATUS_FAILED",
				Message: "Failed to collect hardware status: " + err.Error(),
			}) {
				return
			}
			continue
		}
		if !writeSSEEvent(w, flusher, "hardware-status", snapshot) {
			return
		}
	}
}

func hardwareInterval(r *http.Request) time.Duration {
	seconds, err := strconv.Atoi(r.URL.Query().Get("interval"))
	if err != nil || seconds <= 0 {
		return defaultHardwareInterval
	}
	if seconds > 10 {
		seconds = 10
	}
	return time.Duration(seconds) * time.Second
}
