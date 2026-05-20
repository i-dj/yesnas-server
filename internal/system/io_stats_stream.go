package system

import (
	"net/http"

	"nas-server/internal/storage"
	monitor "nas-server/pkg/iostats"
)

func (h *Handler) HandleStorageIOStatsStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeAPIError(w, http.StatusInternalServerError, "SSE_NOT_SUPPORTED", "SSE is not supported by this server")
		return
	}

	storageID := r.PathValue("storage")
	item, err := storage.Get(storageID)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "STORAGE_NOT_FOUND", "Storage not found: "+storageID)
		return
	}

	provider := monitor.NewProvider()
	prepareSSE(w)
	writeSSEEvent(w, flusher, "ready", map[string]any{
		"sampleIntervalSeconds": 1,
		"scope":                 "storage",
		"storageId":             storageID,
	})

	lastErrorCode := ""
	for {
		select {
		case <-r.Context().Done():
			return
		default:
		}

		stats, err := provider.Sample(r.Context(), *item, IOStatsSampleInterval)
		if err != nil {
			code, message, _ := MapStatsError(err)
			if code != lastErrorCode {
				if !writeSSEEvent(w, flusher, "error", APIError{Code: code, Message: message}) {
					return
				}
				lastErrorCode = code
			}
			if code == "STATS_NOT_SUPPORTED" {
				return
			}
			continue
		}

		lastErrorCode = ""
		if !writeSSEEvent(w, flusher, "io-stats", StripDebugStats(stats, r.URL.Query().Get("debug") == "1")) {
			return
		}
	}
}

func (h *Handler) HandleAllStorageIOStatsStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeAPIError(w, http.StatusInternalServerError, "SSE_NOT_SUPPORTED", "SSE is not supported by this server")
		return
	}

	prepareSSE(w)
	writeSSEEvent(w, flusher, "ready", map[string]any{
		"sampleIntervalSeconds": 1,
		"scope":                 "all-storages",
	})

	for {
		select {
		case <-r.Context().Done():
			return
		default:
		}

		storages, err := storage.List()
		if err != nil {
			if !writeSSEEvent(w, flusher, "error", APIError{
				Code:    "STORAGE_LIST_FAILED",
				Message: "Failed to load storages: " + err.Error(),
			}) {
				return
			}
			continue
		}

		if !writeSSEEvent(w, flusher, "io-stats", CollectStorageStatsBatch(r.Context(), storages, r.URL.Query().Get("debug") == "1")) {
			return
		}
	}
}
