package system

import (
	"net/http"
	"strings"
	"time"
)

const defaultNetworkRealtimeInterval = time.Second

func (h *Handler) HandleNetworkInterfaces(w http.ResponseWriter, r *http.Request) {
	networkRange := parseNetworkRange(r)
	if networkRange == NetworkRangeRealtime {
		writeAPIError(w, http.StatusBadRequest, "NETWORK_RANGE_REQUIRES_SSE", "Use /api/v1/events?topics=system.network for realtime network data")
		return
	}
	snapshot, err := CollectNetworkInterfaces(r.Context(), networkRange, defaultNetworkRealtimeInterval)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "NETWORK_INTERFACES_FAILED", "Failed to collect network interfaces: "+err.Error())
		return
	}
	writeJSON(w, snapshot)
}

func parseNetworkRange(r *http.Request) NetworkRange {
	value := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("range")))
	switch value {
	case "", "realtime", "real-time", "now":
		return NetworkRangeRealtime
	case "1h", "hour":
		return NetworkRangeHour
	case "1d", "day":
		return NetworkRangeDay
	case "1w", "week":
		return NetworkRangeWeek
	case "1mo", "1m", "month":
		return NetworkRangeMonth
	default:
		return NetworkRangeRealtime
	}
}
