package system

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"sync"
	"time"

	"nas-server/internal/storage"
	monitor "nas-server/pkg/iostats"
)

const IOStatsSampleInterval = time.Second

func (h *Handler) HandleStorageIOStats(w http.ResponseWriter, r *http.Request) {
	storageID := r.PathValue("storage")
	item, err := storage.Get(storageID)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "STORAGE_NOT_FOUND", "Storage not found: "+storageID)
		return
	}

	provider := monitor.NewProvider()
	stats, err := provider.Sample(r.Context(), *item, IOStatsSampleInterval)
	if err != nil {
		code, message, status := MapStatsError(err)
		writeAPIError(w, status, code, message)
		return
	}
	writeJSON(w, StripDebugStats(stats, r.URL.Query().Get("debug") == "1"))
}

func (h *Handler) HandleAllStorageIOStats(w http.ResponseWriter, r *http.Request) {
	storages, err := storage.List()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "STORAGE_LIST_FAILED", "Failed to load storages: "+err.Error())
		return
	}
	writeJSON(w, CollectStorageStatsBatch(r.Context(), storages, r.URL.Query().Get("debug") == "1"))
}

func StripDebugStats(stats monitor.Stats, debug bool) monitor.Stats {
	if debug {
		return stats
	}
	stats.Debug = nil
	return stats
}

func CollectStorageStatsBatch(ctx context.Context, storages []storage.Storage, debug bool) StorageStatsBatch {
	provider := monitor.NewProvider()
	items := make([]monitor.Stats, 0, len(storages))
	errorsList := make([]StorageStatsError, 0)
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, storage := range storages {
		storage := storage
		wg.Add(1)
		go func() {
			defer wg.Done()

			sampleCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
			defer cancel()

			stats, err := provider.Sample(sampleCtx, storage, IOStatsSampleInterval)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				code, message, _ := MapStatsError(err)
				errorsList = append(errorsList, StorageStatsError{
					StorageID: storage.ID,
					Code:      code,
					Message:   message,
				})
				return
			}
			items = append(items, StripDebugStats(stats, debug))
		}()
	}

	wg.Wait()
	sort.Slice(items, func(i, j int) bool { return items[i].StorageID < items[j].StorageID })
	sort.Slice(errorsList, func(i, j int) bool { return errorsList[i].StorageID < errorsList[j].StorageID })
	return StorageStatsBatch{Items: items, Errors: errorsList}
}

func HumanizeStatsError(err error) string {
	switch {
	case errors.Is(err, monitor.ErrNotSupported):
		return "Real-time IO stats are not supported for this storage on the current platform"
	case errors.Is(err, monitor.ErrTraceBusy):
		return "System trace resources are busy. Stop other fs_usage or ktrace sessions and try again"
	default:
		return err.Error()
	}
}

func MapStatsError(err error) (string, string, int) {
	switch {
	case errors.Is(err, monitor.ErrNotSupported):
		return "STATS_NOT_SUPPORTED", HumanizeStatsError(err), http.StatusNotImplemented
	case errors.Is(err, monitor.ErrTraceBusy):
		return "TRACE_BUSY", HumanizeStatsError(err), http.StatusConflict
	default:
		return "STATS_FAILED", HumanizeStatsError(err), http.StatusInternalServerError
	}
}
