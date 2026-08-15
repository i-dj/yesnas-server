package events

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	dockermodule "nas-server/internal/docker"
	"nas-server/internal/storage"
	storagepool "nas-server/internal/storagepool"
	systemmodule "nas-server/internal/system"
	"nas-server/pkg/httpx"
	monitor "nas-server/pkg/iostats"
)

const defaultInterval = 2 * time.Second

var supportedTopics = map[string]struct{}{
	"system.status":     {},
	"system.hardware":   {},
	"system.network":    {},
	"storage.io":        {},
	"docker.containers": {},
	"storage.benchmark": {},
}

type Handler struct{}

type outboundEvent struct {
	typeName string
	data     any
}

type eventEnvelope struct {
	Version   int       `json:"version"`
	Type      string    `json:"type"`
	ID        string    `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	Data      any       `json:"data"`
}

func NewHandler() *Handler { return &Handler{} }

func (h *Handler) HandleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		httpx.WriteAPIError(w, http.StatusInternalServerError, "SSE_NOT_SUPPORTED", "SSE is not supported by this server")
		return
	}

	topics, err := parseTopics(r)
	if err != nil {
		httpx.WriteAPIError(w, http.StatusBadRequest, "INVALID_EVENT_TOPICS", err.Error())
		return
	}
	interval := parseInterval(r)
	storageID := strings.TrimSpace(r.URL.Query().Get("storageId"))
	poolID := strings.TrimSpace(r.URL.Query().Get("poolId"))
	sizeGiB := 5
	if _, selected := topics["storage.benchmark"]; selected {
		if poolID == "" {
			httpx.WriteAPIError(w, http.StatusBadRequest, "POOL_ID_REQUIRED", "poolId is required for storage.benchmark")
			return
		}
		if _, err := storagepool.ResolveBenchmarkPool(r.Context(), poolID); err != nil {
			httpx.WriteAPIError(w, http.StatusNotFound, "STORAGE_POOL_NOT_FOUND", "Storage pool not found")
			return
		}
		sizeGiB, err = parseSizeGiB(r)
		if err != nil {
			httpx.WriteAPIError(w, http.StatusBadRequest, "INVALID_SIZE_GIB", err.Error())
			return
		}
	}
	if _, selected := topics["storage.io"]; selected && storageID != "" {
		if _, err := storage.Get(storageID); err != nil {
			httpx.WriteAPIError(w, http.StatusNotFound, "STORAGE_NOT_FOUND", "Storage not found: "+storageID)
			return
		}
	}

	httpx.PrepareSSE(w)
	events := make(chan outboundEvent, 32)
	ctx := r.Context()
	for topic := range topics {
		h.startProducer(ctx, topic, interval, storageID, poolID, sizeGiB, r.URL.Query().Get("debug") == "1", events)
	}

	var sequence atomic.Uint64
	write := func(event outboundEvent) bool {
		now := time.Now().UTC()
		envelope := eventEnvelope{
			Version: 1, Type: event.typeName,
			ID:        fmt.Sprintf("evt_%d_%d", now.UnixMilli(), sequence.Add(1)),
			Timestamp: now, Data: event.data,
		}
		return httpx.WriteSSEEvent(w, flusher, "message", envelope)
	}
	if !write(outboundEvent{typeName: "ready", data: map[string]any{
		"topics": topicsList(topics), "sampleIntervalSeconds": int(interval.Seconds()),
	}}) {
		return
	}

	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case event := <-events:
			if !write(event) {
				return
			}
		case <-heartbeat.C:
			if !write(outboundEvent{typeName: "heartbeat", data: map[string]any{}}) {
				return
			}
		}
	}
}

func (h *Handler) startProducer(ctx context.Context, topic string, interval time.Duration, storageID, poolID string, sizeGiB int, debug bool, events chan<- outboundEvent) {
	switch topic {
	case "system.status":
		go repeat(ctx, events, topic, "SYSTEM_STATUS_FAILED", func() (any, error) {
			return systemmodule.CollectSystemStatus(ctx, interval)
		})
	case "system.hardware":
		go produceHardwareSnapshot(ctx, events, topic, interval)
	case "system.network":
		go repeat(ctx, events, topic, "NETWORK_INTERFACES_FAILED", func() (any, error) {
			return systemmodule.CollectNetworkInterfaces(ctx, systemmodule.NetworkRangeRealtime, interval)
		})
	case "docker.containers":
		go produceDockerContainers(ctx, events, topic, interval)
	case "storage.io":
		go produceStorageIO(ctx, events, storageID, debug)
	case "storage.benchmark":
		go produceBenchmark(ctx, events, poolID, sizeGiB)
	}
}

func produceHardwareSnapshot(ctx context.Context, events chan<- outboundEvent, topic string, interval time.Duration) {
	snapshot, err := systemmodule.CollectHardwareSnapshotBasic(ctx)
	if err != nil {
		emit(ctx, events, outboundEvent{"error", httpx.APIError{Code: "HARDWARE_STATUS_FAILED", Message: err.Error()}})
	} else if !emit(ctx, events, outboundEvent{topic, snapshot}) {
		return
	}

	repeat(ctx, events, topic, "HARDWARE_STATUS_FAILED", func() (any, error) {
		return systemmodule.CollectHardwareSnapshot(ctx, interval)
	})
}

func produceDockerContainers(ctx context.Context, events chan<- outboundEvent, topic string, interval time.Duration) {
	items, err := dockermodule.ListContainersBasic(ctx)
	if err != nil {
		emit(ctx, events, outboundEvent{"error", httpx.APIError{Code: "DOCKER_CONTAINERS_LIST_FAILED", Message: err.Error()}})
	} else {
		if items == nil {
			items = []dockermodule.Container{}
		}
		if !emit(ctx, events, outboundEvent{topic, map[string]any{"items": items, "checkedAt": time.Now().UTC()}}) {
			return
		}
	}

	repeatWithDelay(ctx, events, topic, "DOCKER_CONTAINERS_LIST_FAILED", interval, func() (any, error) {
		items, err := dockermodule.ListContainers(ctx)
		if items == nil {
			items = []dockermodule.Container{}
		}
		return map[string]any{"items": items, "checkedAt": time.Now().UTC()}, err
	})
}

func repeat(ctx context.Context, events chan<- outboundEvent, topic, errorCode string, collect func() (any, error)) {
	for ctx.Err() == nil {
		data, err := collect()
		if err != nil {
			emit(ctx, events, outboundEvent{"error", httpx.APIError{Code: errorCode, Message: err.Error()}})
			continue
		}
		if !emit(ctx, events, outboundEvent{topic, data}) {
			return
		}
	}
}

func repeatWithDelay(ctx context.Context, events chan<- outboundEvent, topic, errorCode string, interval time.Duration, collect func() (any, error)) {
	for ctx.Err() == nil {
		data, err := collect()
		if err != nil {
			emit(ctx, events, outboundEvent{"error", httpx.APIError{Code: errorCode, Message: err.Error()}})
		} else if !emit(ctx, events, outboundEvent{topic, data}) {
			return
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func produceStorageIO(ctx context.Context, events chan<- outboundEvent, storageID string, debug bool) {
	if storageID == "" {
		for ctx.Err() == nil {
			storages, err := storage.List()
			if err != nil {
				emit(ctx, events, outboundEvent{"error", httpx.APIError{Code: "STORAGE_LIST_FAILED", Message: err.Error()}})
				continue
			}
			if !emit(ctx, events, outboundEvent{"storage.io", systemmodule.CollectStorageStatsBatch(ctx, storages, debug)}) {
				return
			}
		}
		return
	}
	item, _ := storage.Get(storageID)
	provider := monitor.NewProvider()
	for ctx.Err() == nil {
		stats, err := provider.Sample(ctx, *item, systemmodule.IOStatsSampleInterval)
		if err != nil {
			code, message, _ := systemmodule.MapStatsError(err)
			emit(ctx, events, outboundEvent{"error", httpx.APIError{Code: code, Message: message}})
			if code == "STATS_NOT_SUPPORTED" {
				return
			}
			continue
		}
		if !emit(ctx, events, outboundEvent{"storage.io", systemmodule.StripDebugStats(stats, debug)}) {
			return
		}
	}
}

func produceBenchmark(ctx context.Context, events chan<- outboundEvent, poolID string, sizeGiB int) {
	pool, err := storagepool.ResolveBenchmarkPool(ctx, poolID)
	if err != nil {
		emit(ctx, events, outboundEvent{"error", httpx.APIError{Code: "STORAGE_POOL_NOT_FOUND", Message: "Storage pool not found"}})
		return
	}
	result, err := storagepool.BenchmarkPoolStream(ctx, pool, storagepool.BenchmarkRequest{SizeGiB: sizeGiB}, func(progress storagepool.BenchmarkProgress) bool {
		return emit(ctx, events, outboundEvent{"storage.benchmark.progress", progress})
	})
	if err != nil {
		emit(ctx, events, outboundEvent{"error", httpx.APIError{Code: "BENCHMARK_STORAGE_POOL_FAILED", Message: err.Error()}})
		return
	}
	emit(ctx, events, outboundEvent{"storage.benchmark.completed", result})
}

func emit(ctx context.Context, events chan<- outboundEvent, event outboundEvent) bool {
	select {
	case events <- event:
		return true
	case <-ctx.Done():
		return false
	}
}

func parseTopics(r *http.Request) (map[string]struct{}, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("topics"))
	if raw == "" {
		return nil, fmt.Errorf("topics is required")
	}
	topics := make(map[string]struct{})
	for _, value := range strings.Split(raw, ",") {
		topic := strings.TrimSpace(value)
		if _, ok := supportedTopics[topic]; !ok {
			return nil, fmt.Errorf("unsupported topic: %s", topic)
		}
		topics[topic] = struct{}{}
	}
	return topics, nil
}

func topicsList(topics map[string]struct{}) []string {
	items := make([]string, 0, len(topics))
	for topic := range topics {
		items = append(items, topic)
	}
	sort.Strings(items)
	return items
}

func parseInterval(r *http.Request) time.Duration {
	seconds, err := strconv.Atoi(r.URL.Query().Get("interval"))
	if err != nil || seconds <= 0 {
		return defaultInterval
	}
	if seconds > 10 {
		seconds = 10
	}
	return time.Duration(seconds) * time.Second
}

func parseSizeGiB(r *http.Request) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("sizeGiB"))
	if raw == "" {
		return 5, nil
	}
	size, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("sizeGiB must be an integer")
	}
	if size <= 0 {
		return 0, fmt.Errorf("sizeGiB must be greater than 0")
	}
	return size, nil
}
