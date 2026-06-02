package system

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

const defaultNetworkRealtimeInterval = time.Second

func (h *Handler) HandleNetworkInterfaces(w http.ResponseWriter, r *http.Request) {
	networkRange := parseNetworkRange(r)
	if networkRange == NetworkRangeRealtime {
		writeAPIError(w, http.StatusBadRequest, "NETWORK_RANGE_REQUIRES_SSE", "Use /api/v1/system/network/stream for realtime network data")
		return
	}
	snapshot, err := CollectNetworkInterfaces(r.Context(), networkRange, defaultNetworkRealtimeInterval)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "NETWORK_INTERFACES_FAILED", "Failed to collect network interfaces: "+err.Error())
		return
	}
	writeJSON(w, snapshot)
}

func (h *Handler) HandleNetworkInterfacesStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeAPIError(w, http.StatusInternalServerError, "SSE_NOT_SUPPORTED", "SSE is not supported by this server")
		return
	}
	interval := networkRealtimeInterval(r)
	prepareSSE(w)
	writeSSEEvent(w, flusher, "ready", map[string]any{
		"sampleIntervalSeconds": int(interval.Seconds()),
		"scope":                 "network-interfaces",
	})

	for {
		select {
		case <-r.Context().Done():
			return
		default:
		}
		snapshot, err := CollectNetworkInterfaces(r.Context(), NetworkRangeRealtime, interval)
		if err != nil {
			if !writeSSEEvent(w, flusher, "error", APIError{
				Code:    "NETWORK_INTERFACES_FAILED",
				Message: "Failed to collect network interfaces: " + err.Error(),
			}) {
				return
			}
			continue
		}
		if !writeSSEEvent(w, flusher, "network-interfaces", snapshot) {
			return
		}
	}
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

func networkRealtimeInterval(r *http.Request) time.Duration {
	seconds, err := strconv.Atoi(r.URL.Query().Get("interval"))
	if err != nil || seconds <= 0 {
		return defaultNetworkRealtimeInterval
	}
	if seconds > 10 {
		seconds = 10
	}
	return time.Duration(seconds) * time.Second
}
