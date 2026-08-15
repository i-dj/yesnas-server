package files

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const transcodeCacheDir = "./data/transcodes"

var (
	transcodeLocks sync.Map
	hlsJobs        sync.Map
)

type hlsJob struct {
	done       chan struct{}
	cancel     context.CancelFunc
	err        error
	completed  atomic.Bool
	lastAccess atomic.Int64
}

func (j *hlsJob) touch() {
	j.lastAccess.Store(time.Now().UnixNano())
}

func transcodeCachePath(path string, info os.FileInfo) string {
	return filepath.Join(transcodeCacheDir, transcodeCacheKey(path, info)+".mp4")
}

func transcodeCacheKey(path string, info os.FileInfo) string {
	hash := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%d", filepath.Clean(path), info.Size(), info.ModTime().UnixNano())))
	return hex.EncodeToString(hash[:])
}

func hlsCacheDir(path string, info os.FileInfo) string {
	return filepath.Join(transcodeCacheDir, transcodeCacheKey(path, info)+"_hls")
}

func getTranscodeLock(cachePath string) *sync.Mutex {
	value, _ := transcodeLocks.LoadOrStore(cachePath, &sync.Mutex{})
	return value.(*sync.Mutex)
}

func transcodeToMP4(src string, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}

	tmp := dst + ".tmp.mp4"
	defer os.Remove(tmp)

	cmd := exec.Command(
		"ffmpeg",
		"-y",
		"-i",
		src,
		"-map",
		"0:v:0",
		"-map",
		"0:a?",
		"-c:v",
		"libx264",
		"-preset",
		"veryfast",
		"-crf",
		"23",
		"-pix_fmt",
		"yuv420p",
		"-c:a",
		"aac",
		"-b:a",
		"160k",
		"-movflags",
		"+faststart",
		"-f",
		"mp4",
		tmp,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg failed: %w: %s", err, string(output))
	}
	return os.Rename(tmp, dst)
}

func startHLSJob(src string, dir string) *hlsJob {
	ctx, cancel := context.WithCancel(context.Background())
	nextJob := &hlsJob{done: make(chan struct{}), cancel: cancel}
	nextJob.touch()

	value, loaded := hlsJobs.LoadOrStore(dir, nextJob)
	job := value.(*hlsJob)
	if loaded {
		cancel()
		job.touch()
		return job
	}

	go func() {
		defer close(job.done)
		defer hlsJobs.Delete(dir)
		defer func() {
			if !job.completed.Load() {
				os.RemoveAll(dir)
			}
		}()

		go func() {
			ticker := time.NewTicker(5 * time.Second)
			defer ticker.Stop()

			for {
				select {
				case <-job.done:
					return
				case <-ticker.C:
					lastAccess := time.Unix(0, job.lastAccess.Load())
					if time.Since(lastAccess) > 2*time.Minute {
						log.Printf("[HLS] cancel idle transcode: %s", filepath.Base(src))
						job.cancel()
						return
					}
				}
			}
		}()

		if err := os.MkdirAll(dir, 0755); err != nil {
			job.err = err
			return
		}

		manifest := filepath.Join(dir, "index.m3u8")
		segmentPattern := filepath.Join(dir, "segment_%05d.ts")
		cmd := exec.CommandContext(
			ctx,
			"ffmpeg",
			"-y",
			"-i",
			src,
			"-map",
			"0:v:0",
			"-map",
			"0:a?",
			"-c:v",
			"libx264",
			"-preset",
			"veryfast",
			"-crf",
			"23",
			"-pix_fmt",
			"yuv420p",
			"-c:a",
			"aac",
			"-b:a",
			"160k",
			"-hls_time",
			"4",
			"-hls_list_size",
			"0",
			"-hls_flags",
			"independent_segments+temp_file",
			"-hls_playlist_type",
			"event",
			"-hls_segment_filename",
			segmentPattern,
			"-f",
			"hls",
			manifest,
		)
		output, err := cmd.CombinedOutput()
		if err != nil {
			if ctx.Err() == context.Canceled {
				job.err = ctx.Err()
				return
			}
			job.err = fmt.Errorf("ffmpeg hls failed: %w: %s", err, string(output))
			return
		}
		job.completed.Store(true)
	}()

	return job
}

func waitForManifest(manifest string, job *hlsJob) error {
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()

	timeout := time.After(45 * time.Second)
	for {
		if _, err := os.Stat(manifest); err == nil {
			return nil
		}

		select {
		case <-ticker.C:
		case <-job.done:
			if job.err != nil {
				return job.err
			}
			if _, err := os.Stat(manifest); err == nil {
				return nil
			}
			return os.ErrNotExist
		case <-timeout:
			return fmt.Errorf("timeout waiting for hls manifest")
		}
	}
}

func resolveHLSCache(fileID string) (string, string, error) {
	path, err := decodeFileID(fileID)
	if err != nil {
		return "", "", err
	}

	info, err := os.Stat(path)
	if err != nil {
		return "", "", err
	}
	if info.IsDir() {
		return "", "", fmt.Errorf("target is not a file")
	}

	return path, hlsCacheDir(path, info), nil
}

func (h *Handler) HandleHLSManifest(w http.ResponseWriter, r *http.Request) {
	fileID := r.PathValue("fileId")
	path, dir, err := resolveHLSCache(fileID)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "HLS_SOURCE_INVALID", err.Error())
		return
	}

	manifest := filepath.Join(dir, "index.m3u8")
	if _, err := os.Stat(manifest); err != nil {
		log.Printf("[HLS] start: %s", filepath.Base(path))
		job := startHLSJob(path, dir)
		job.touch()
		if err := waitForManifest(manifest, job); err != nil {
			log.Printf("[HLS] failed: %s: %v", filepath.Base(path), err)
			writeAPIError(w, http.StatusInternalServerError, "HLS_TRANSCODE_FAILED", "failed to prepare video stream")
			return
		}
		log.Printf("[HLS] manifest ready: %s", filepath.Base(path))
	} else if value, ok := hlsJobs.Load(dir); ok {
		value.(*hlsJob).touch()
	}

	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFile(w, r, manifest)
}

func (h *Handler) HandleHLSSegment(w http.ResponseWriter, r *http.Request) {
	_, dir, err := resolveHLSCache(r.PathValue("fileId"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "HLS_SOURCE_INVALID", err.Error())
		return
	}

	segment := filepath.Base(r.PathValue("segment"))
	if !strings.HasSuffix(segment, ".ts") {
		writeAPIError(w, http.StatusBadRequest, "HLS_SEGMENT_INVALID", "invalid hls segment")
		return
	}

	path := filepath.Join(dir, segment)
	if _, err := os.Stat(path); err != nil {
		writeAPIError(w, http.StatusNotFound, "HLS_SEGMENT_NOT_FOUND", "hls segment not found")
		return
	}
	if value, ok := hlsJobs.Load(dir); ok {
		value.(*hlsJob).touch()
	}

	w.Header().Set("Content-Type", "video/mp2t")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeFile(w, r, path)
}

func (h *Handler) HandleHLSStop(w http.ResponseWriter, r *http.Request) {
	_, dir, err := resolveHLSCache(r.PathValue("fileId"))
	if err != nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if value, ok := hlsJobs.Load(dir); ok {
		job := value.(*hlsJob)
		log.Printf("[HLS] stop: %s", filepath.Base(dir))
		job.cancel()
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) HandlePlayableContent(w http.ResponseWriter, r *http.Request) {
	fileID := r.PathValue("fileId")
	path, err := decodeFileID(fileID)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_FILE_ID", "invalid file id")
		return
	}

	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			writeAPIError(w, http.StatusNotFound, "FILE_NOT_FOUND", "file not found")
			return
		}
		writeAPIError(w, http.StatusInternalServerError, "STAT_FILE_FAILED", "failed to read file: "+err.Error())
		return
	}
	if info.IsDir() {
		writeAPIError(w, http.StatusBadRequest, "NOT_A_FILE", "target is not a file")
		return
	}

	cachePath := transcodeCachePath(path, info)
	if _, err := os.Stat(cachePath); err != nil {
		lock := getTranscodeLock(cachePath)
		lock.Lock()
		defer lock.Unlock()

		if _, err := os.Stat(cachePath); err != nil {
			log.Printf("[TRANSCODE] start: %s", filepath.Base(path))
			if err := transcodeToMP4(path, cachePath); err != nil {
				log.Printf("[TRANSCODE] failed: %s: %v", filepath.Base(path), err)
				writeAPIError(w, http.StatusInternalServerError, "TRANSCODE_FAILED", "failed to prepare playable video")
				return
			}
			log.Printf("[TRANSCODE] done: %s", filepath.Base(path))
		}
	}

	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeFile(w, r, cachePath)
}
