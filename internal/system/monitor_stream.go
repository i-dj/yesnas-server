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
