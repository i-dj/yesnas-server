package storagepool

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	commandrunner "nas-server/pkg/shell"
)

const (
	defaultBenchmarkSizeGiB = 5
	benchmarkChunkSize      = 8 * 1024 * 1024
)

func (h *Handler) HandleBenchmarkPoolStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeAPIError(w, http.StatusInternalServerError, "SSE_NOT_SUPPORTED", "SSE is not supported by this server")
		return
	}

	pool, err := Get(r.PathValue("poolId"))
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "STORAGE_POOL_NOT_FOUND", "Storage pool not found")
		return
	}

	sizeGiB, err := parseBenchmarkSizeGiB(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_SIZE_GIB", err.Error())
		return
	}

	prepareSSE(w)
	writeSSEEvent(w, flusher, "ready", map[string]any{
		"poolId":  pool.ID,
		"sizeGiB": sizeGiB,
	})

	result, streamErr := BenchmarkPoolStream(r.Context(), pool, BenchmarkRequest{SizeGiB: sizeGiB}, func(progress BenchmarkProgress) bool {
		return writeSSEEvent(w, flusher, "progress", progress)
	})
	if streamErr != nil {
		writeSSEEvent(w, flusher, "error", map[string]any{
			"code":    "BENCHMARK_STORAGE_POOL_FAILED",
			"message": streamErr.Error(),
		})
		return
	}
	writeSSEEvent(w, flusher, "completed", result)
}

func BenchmarkPoolSync(ctx context.Context, pool *StoragePool, req BenchmarkRequest) (*BenchmarkResult, error) {
	if pool == nil {
		return nil, fmt.Errorf("storage pool is required")
	}
	if !isMountpointActive(pool.MountPath) {
		return nil, fmt.Errorf("storage pool is offline")
	}

	sizeGiB := req.SizeGiB
	sizeGiB = maxBenchmarkSizeGiB(sizeGiB)
	sizeBytes := int64(sizeGiB) * 1024 * 1024 * 1024
	targetDir := pool.DataPath
	if targetDir == "" {
		targetDir = pool.MountPath
	}

	_, free := statFilesystem(targetDir)
	if free > 0 && free <= sizeBytes {
		return nil, fmt.Errorf("insufficient free space for benchmark: need %s, free %s", formatBytesIEC(uint64(sizeBytes)), formatBytesIEC(uint64(free)))
	}

	testPath := filepath.Join(targetDir, ".yesnas-benchmark.tmp")
	_, _ = commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "rm", "-f", testPath)
	defer func() {
		_, _ = commandrunner.RunWithOptions(context.Background(), commandrunner.Options{UseSudo: true}, "rm", "-f", testPath)
	}()

	writeSpeed, err := writeBenchmarkFile(ctx, testPath, sizeBytes)
	if err != nil {
		return nil, err
	}
	readSpeed, err := readBenchmarkFile(ctx, testPath)
	if err != nil {
		return nil, err
	}

	testedAt := time.Now()
	if err := UpdateBenchmarkResult(pool.ID, readSpeed, writeSpeed, testedAt); err != nil {
		return nil, fmt.Errorf("update storage pool benchmark result: %w", err)
	}

	return &BenchmarkResult{
		PoolID:                pool.ID,
		Path:                  targetDir,
		SizeBytes:             sizeBytes,
		WriteSpeedBytesPerSec: writeSpeed,
		ReadSpeedBytesPerSec:  readSpeed,
		TestedAt:              testedAt,
	}, nil
}

func BenchmarkPoolStream(ctx context.Context, pool *StoragePool, req BenchmarkRequest, emit func(BenchmarkProgress) bool) (*BenchmarkResult, error) {
	if pool == nil {
		return nil, fmt.Errorf("storage pool is required")
	}
	if !isMountpointActive(pool.MountPath) {
		return nil, fmt.Errorf("storage pool is offline")
	}

	sizeGiB := maxBenchmarkSizeGiB(req.SizeGiB)
	sizeBytes := int64(sizeGiB) * 1024 * 1024 * 1024
	targetDir := pool.DataPath
	if targetDir == "" {
		targetDir = pool.MountPath
	}

	_, free := statFilesystem(targetDir)
	if free > 0 && free <= sizeBytes {
		return nil, fmt.Errorf("insufficient free space for benchmark: need %s, free %s", formatBytesIEC(uint64(sizeBytes)), formatBytesIEC(uint64(free)))
	}

	testPath := filepath.Join(targetDir, ".yesnas-benchmark.tmp")
	_, _ = commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "rm", "-f", testPath)
	defer func() {
		_, _ = commandrunner.RunWithOptions(context.Background(), commandrunner.Options{UseSudo: true}, "rm", "-f", testPath)
	}()

	writeSpeed, err := runBenchmarkWriteStream(ctx, pool.ID, sizeGiB, testPath, sizeBytes, emit)
	if err != nil {
		return nil, err
	}
	readSpeed, err := runBenchmarkReadStream(ctx, pool.ID, sizeGiB, testPath, sizeBytes, emit)
	if err != nil {
		return nil, err
	}

	testedAt := time.Now()
	if err := UpdateBenchmarkResult(pool.ID, readSpeed, writeSpeed, testedAt); err != nil {
		return nil, fmt.Errorf("update storage pool benchmark result: %w", err)
	}

	return &BenchmarkResult{
		PoolID:                pool.ID,
		Path:                  targetDir,
		SizeBytes:             sizeBytes,
		WriteSpeedBytesPerSec: writeSpeed,
		ReadSpeedBytesPerSec:  readSpeed,
		TestedAt:              testedAt,
	}, nil
}

func writeBenchmarkFile(ctx context.Context, path string, sizeBytes int64) (float64, error) {
	start := time.Now()
	count := sizeBytes / benchmarkChunkSize
	if count <= 0 {
		count = 1
	}
	if _, err := commandrunner.RunWithOptions(
		ctx,
		commandrunner.Options{UseSudo: true, LogStderrOnSuccess: true},
		"dd",
		"if=/dev/zero",
		"of="+path,
		"bs="+strconv.Itoa(benchmarkChunkSize),
		"count="+strconv.FormatInt(count, 10),
		"conv=fdatasync",
		"status=progress",
	); err != nil {
		return 0, fmt.Errorf("create benchmark file: %w", err)
	}
	elapsed := time.Since(start).Seconds()
	if elapsed <= 0 {
		return 0, nil
	}
	return float64(sizeBytes) / elapsed, nil
}

func readBenchmarkFile(ctx context.Context, path string) (float64, error) {
	start := time.Now()
	_, err := commandrunner.RunWithOptions(
		ctx,
		commandrunner.Options{UseSudo: true, LogStderrOnSuccess: true},
		"dd",
		"if="+path,
		"of=/dev/null",
		"bs="+strconv.Itoa(benchmarkChunkSize),
		"iflag=direct",
		"status=progress",
	)
	if err != nil {
		_, fallbackErr := commandrunner.RunWithOptions(
			ctx,
			commandrunner.Options{UseSudo: true, LogStderrOnSuccess: true},
			"dd",
			"if="+path,
			"of=/dev/null",
			"bs="+strconv.Itoa(benchmarkChunkSize),
			"status=progress",
		)
		if fallbackErr != nil {
			return 0, fmt.Errorf("read benchmark file: %w", err)
		}
	}
	elapsed := time.Since(start).Seconds()
	if elapsed <= 0 {
		return 0, nil
	}
	fileInfo, statErr := os.Stat(path)
	if statErr != nil {
		return 0, fmt.Errorf("stat benchmark file: %w", statErr)
	}
	return float64(fileInfo.Size()) / elapsed, nil
}

func maxBenchmarkSizeGiB(value int) int {
	if value <= 0 {
		return defaultBenchmarkSizeGiB
	}
	return value
}

func parseBenchmarkSizeGiB(r *http.Request) (int, error) {
	raw := r.URL.Query().Get("sizeGiB")
	if raw == "" {
		return defaultBenchmarkSizeGiB, nil
	}
	sizeGiB, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("sizeGiB must be an integer")
	}
	if sizeGiB <= 0 {
		return 0, fmt.Errorf("sizeGiB must be greater than 0")
	}
	return sizeGiB, nil
}

func runBenchmarkWriteStream(ctx context.Context, poolID string, sizeGiB int, path string, totalBytes int64, emit func(BenchmarkProgress) bool) (float64, error) {
	const chunkMiB int64 = 256
	chunkBytes := chunkMiB * 1024 * 1024
	completed := int64(0)
	stageStart := time.Now()

	for completed < totalBytes {
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		default:
		}

		currentChunkBytes := chunkBytes
		if remaining := totalBytes - completed; remaining < currentChunkBytes {
			currentChunkBytes = remaining
		}
		countMiB := currentChunkBytes / (1024 * 1024)
		if countMiB <= 0 {
			countMiB = 1
		}

		if _, err := commandrunner.RunWithOptions(
			ctx,
			commandrunner.Options{UseSudo: true},
			"dd",
			"if=/dev/zero",
			"of="+path,
			"bs=1M",
			"count="+strconv.FormatInt(countMiB, 10),
			"oflag=append",
			"conv=notrunc,fdatasync",
			"status=none",
		); err != nil {
			return 0, fmt.Errorf("create benchmark file: %w", err)
		}

		completed += currentChunkBytes
		elapsed := time.Since(stageStart).Seconds()
		currentSpeed := 0.0
		if elapsed > 0 {
			currentSpeed = float64(completed) / elapsed
		}
		if !emit(BenchmarkProgress{
			PoolID:                  poolID,
			Stage:                   "write",
			SizeGiB:                 sizeGiB,
			TotalBytes:              totalBytes,
			CompletedBytes:          completed,
			RemainingBytes:          maxInt64(totalBytes-completed, 0),
			Percent:                 float64(completed) * 100 / float64(totalBytes),
			CurrentSpeedBytesPerSec: currentSpeed,
			ElapsedSeconds:          elapsed,
			UpdatedAt:               time.Now(),
		}) {
			return 0, context.Canceled
		}
	}

	totalElapsed := time.Since(stageStart).Seconds()
	if totalElapsed <= 0 {
		return 0, nil
	}
	return float64(totalBytes) / totalElapsed, nil
}

func runBenchmarkReadStream(ctx context.Context, poolID string, sizeGiB int, path string, totalBytes int64, emit func(BenchmarkProgress) bool) (float64, error) {
	const chunkMiB int64 = 256
	chunkBytes := chunkMiB * 1024 * 1024
	completed := int64(0)
	stageStart := time.Now()
	useDirect := true

	for completed < totalBytes {
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		default:
		}

		currentChunkBytes := chunkBytes
		if remaining := totalBytes - completed; remaining < currentChunkBytes {
			currentChunkBytes = remaining
		}
		skipMiB := completed / (1024 * 1024)
		countMiB := currentChunkBytes / (1024 * 1024)
		if countMiB <= 0 {
			countMiB = 1
		}

		args := []string{
			"if=" + path,
			"of=/dev/null",
			"bs=1M",
			"skip=" + strconv.FormatInt(skipMiB, 10),
			"count=" + strconv.FormatInt(countMiB, 10),
			"status=none",
		}
		if useDirect {
			args = append(args, "iflag=direct")
		}

		if _, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "dd", args...); err != nil {
			if useDirect {
				useDirect = false
				if _, fallbackErr := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "dd",
					"if="+path,
					"of=/dev/null",
					"bs=1M",
					"skip="+strconv.FormatInt(skipMiB, 10),
					"count="+strconv.FormatInt(countMiB, 10),
					"status=none",
				); fallbackErr != nil {
					return 0, fmt.Errorf("read benchmark file: %w", err)
				}
			} else {
				return 0, fmt.Errorf("read benchmark file: %w", err)
			}
		}

		completed += currentChunkBytes
		elapsed := time.Since(stageStart).Seconds()
		currentSpeed := 0.0
		if elapsed > 0 {
			currentSpeed = float64(completed) / elapsed
		}
		if !emit(BenchmarkProgress{
			PoolID:                  poolID,
			Stage:                   "read",
			SizeGiB:                 sizeGiB,
			TotalBytes:              totalBytes,
			CompletedBytes:          completed,
			RemainingBytes:          maxInt64(totalBytes-completed, 0),
			Percent:                 float64(completed) * 100 / float64(totalBytes),
			CurrentSpeedBytesPerSec: currentSpeed,
			ElapsedSeconds:          elapsed,
			UpdatedAt:               time.Now(),
		}) {
			return 0, context.Canceled
		}
	}

	totalElapsed := time.Since(stageStart).Seconds()
	if totalElapsed <= 0 {
		return 0, nil
	}
	return float64(totalBytes) / totalElapsed, nil
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
