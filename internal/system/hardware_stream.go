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
