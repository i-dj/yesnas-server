package storagepool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"nas-server/internal/storage"
	commandrunner "nas-server/pkg/shell"
)

const (
	defaultBenchmarkSizeGiB = 5
	benchmarkChunkSize      = 8 * 1024 * 1024
	cloudBenchmarkSizeBytes = 256 * 1024 * 1024
)

func ResolveBenchmarkPool(ctx context.Context, poolID string) (*StoragePool, error) {
	pool, err := Get(poolID)
	if err == nil {
		return pool, nil
	}

	storageRecord, storageErr := storage.Get(poolID)
	if storageErr != nil || storageRecord == nil {
		return nil, err
	}
	if !isCloudStorage(*storageRecord) {
		return nil, err
	}
	return &StoragePool{
		ID:         storageRecord.ID,
		StorageID:  storageRecord.ID,
		Name:       storageRecord.Name,
		Filesystem: storageRecord.Provider,
		RaidLevel:  "single",
		MountPath:  storageRecord.MountPath,
		DataPath:   storageRecord.MountPath,
		Devices:    []PoolDevice{},
	}, nil
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
	commandOptions := benchmarkCommandOptions(pool)
	_, _ = commandrunner.RunWithOptions(ctx, commandOptions, "rm", "-f", testPath)
	defer func() {
		_, _ = commandrunner.RunWithOptions(context.Background(), commandOptions, "rm", "-f", testPath)
	}()

	writeSpeed, err := writeBenchmarkFile(ctx, testPath, sizeBytes, commandOptions)
	if err != nil {
		return nil, err
	}
	readSpeed, err := readBenchmarkFile(ctx, testPath, commandOptions)
	if err != nil {
		return nil, err
	}

	testedAt := time.Now().UTC()
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
	sizeGiB := maxBenchmarkSizeGiB(req.SizeGiB)
	sizeBytes := int64(sizeGiB) * 1024 * 1024 * 1024
	if isCloudBenchmarkPool(pool) {
		return BenchmarkCloudPoolStream(ctx, pool, sizeGiB, cloudBenchmarkSizeBytes, emit)
	}
	if !isMountpointActive(pool.MountPath) {
		return nil, fmt.Errorf("storage pool is offline")
	}

	targetDir := pool.DataPath
	if targetDir == "" {
		targetDir = pool.MountPath
	}

	_, free := statFilesystem(targetDir)
	if free > 0 && free <= sizeBytes {
		return nil, fmt.Errorf("insufficient free space for benchmark: need %s, free %s", formatBytesIEC(uint64(sizeBytes)), formatBytesIEC(uint64(free)))
	}

	testPath := filepath.Join(targetDir, ".yesnas-benchmark.tmp")
	commandOptions := benchmarkCommandOptions(pool)
	_, _ = commandrunner.RunWithOptions(ctx, commandOptions, "rm", "-f", testPath)
	defer func() {
		_, _ = commandrunner.RunWithOptions(context.Background(), commandOptions, "rm", "-f", testPath)
	}()

	writeSpeed, err := runBenchmarkWriteStream(ctx, pool.ID, sizeGiB, testPath, sizeBytes, commandOptions, emit)
	if err != nil {
		return nil, err
	}
	readSpeed, err := runBenchmarkReadStream(ctx, pool.ID, sizeGiB, testPath, sizeBytes, commandOptions, emit)
	if err != nil {
		return nil, err
	}

	testedAt := time.Now().UTC()
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

func BenchmarkCloudPoolStream(ctx context.Context, pool *StoragePool, sizeGiB int, sizeBytes int64, emit func(BenchmarkProgress) bool) (*BenchmarkResult, error) {
	storageRecord, err := storage.Get(pool.StorageID)
	if err != nil {
		return nil, fmt.Errorf("load cloud storage: %w", err)
	}
	if !isCloudStorage(*storageRecord) {
		return nil, fmt.Errorf("cloud benchmark is not supported for provider %s", storageRecord.Provider)
	}
	if err := UpsertCloudPoolRecord(cloudStoragePool(*storageRecord)); err != nil {
		return nil, fmt.Errorf("save cloud storage pool record: %w", err)
	}
	result, err := storage.BenchmarkGoogleDrive(ctx, storageRecord, sizeBytes, func(progress storage.CloudBenchmarkProgress) bool {
		elapsed := time.Since(progress.StartedAt).Seconds()
		percent := 0.0
		if progress.TotalBytes > 0 {
			percent = float64(progress.CompletedBytes) * 100 / float64(progress.TotalBytes)
		}
		return emit(BenchmarkProgress{
			PoolID:                  pool.ID,
			Stage:                   progress.Stage,
			SizeGiB:                 sizeGiB,
			TotalBytes:              progress.TotalBytes,
			CompletedBytes:          progress.CompletedBytes,
			RemainingBytes:          maxInt64(progress.TotalBytes-progress.CompletedBytes, 0),
			Percent:                 percent,
			CurrentSpeedBytesPerSec: progress.CurrentSpeedBytesPerSec,
			ElapsedSeconds:          elapsed,
			UpdatedAt:               time.Now().UTC(),
		})
	})
	if err != nil {
		return nil, err
	}

	testedAt := time.Now().UTC()
	if err := UpdateBenchmarkResult(pool.ID, result.ReadSpeedBytesPerSec, result.WriteSpeedBytesPerSec, testedAt); err != nil {
		return nil, fmt.Errorf("update cloud storage pool benchmark result: %w", err)
	}
	return &BenchmarkResult{
		PoolID:                pool.ID,
		Path:                  result.RemotePath,
		SizeBytes:             result.SizeBytes,
		WriteSpeedBytesPerSec: result.WriteSpeedBytesPerSec,
		ReadSpeedBytesPerSec:  result.ReadSpeedBytesPerSec,
		TestedAt:              testedAt,
	}, nil
}

func isCloudBenchmarkPool(pool *StoragePool) bool {
	return pool != nil && isCloudProvider(pool.Filesystem)
}

func benchmarkCommandOptions(pool *StoragePool) commandrunner.Options {
	if pool != nil && pool.Filesystem != "btrfs" {
		return commandrunner.Options{}
	}
	return commandrunner.Options{UseSudo: true}
}

func writeBenchmarkFile(ctx context.Context, path string, sizeBytes int64, commandOptions commandrunner.Options) (float64, error) {
	start := time.Now()
	count := sizeBytes / benchmarkChunkSize
	if count <= 0 {
		count = 1
	}
	commandOptions.LogStderrOnSuccess = true
	if _, err := commandrunner.RunWithOptions(
		ctx,
		commandOptions,
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

func readBenchmarkFile(ctx context.Context, path string, commandOptions commandrunner.Options) (float64, error) {
	start := time.Now()
	commandOptions.LogStderrOnSuccess = true
	_, err := commandrunner.RunWithOptions(
		ctx,
		commandOptions,
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
			commandOptions,
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

func runBenchmarkWriteStream(ctx context.Context, poolID string, sizeGiB int, path string, totalBytes int64, commandOptions commandrunner.Options, emit func(BenchmarkProgress) bool) (float64, error) {
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
			commandOptions,
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
			UpdatedAt:               time.Now().UTC(),
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

func runBenchmarkReadStream(ctx context.Context, poolID string, sizeGiB int, path string, totalBytes int64, commandOptions commandrunner.Options, emit func(BenchmarkProgress) bool) (float64, error) {
	const chunkMiB int64 = 256
	chunkBytes := chunkMiB * 1024 * 1024
	completed := int64(0)
	stageStart := time.Now()
	useDirect := commandOptions.UseSudo

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

		if _, err := commandrunner.RunWithOptions(ctx, commandOptions, "dd", args...); err != nil {
			if useDirect {
				useDirect = false
				if _, fallbackErr := commandrunner.RunWithOptions(ctx, commandOptions, "dd",
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
			UpdatedAt:               time.Now().UTC(),
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
