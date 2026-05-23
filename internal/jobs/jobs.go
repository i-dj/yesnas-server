package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"nas-server/database"
	"nas-server/internal/storage"
	"nas-server/internal/storagepool"
	"nas-server/pkg/httpx"
	"nas-server/pkg/idgen"
)

type Type string

const (
	TypeCloudSync Type = "cloud_sync"
)

type Status string

const (
	StatusPending   Status = "pending"
	StatusRunning   Status = "running"
	StatusPaused    Status = "paused"
	StatusSuccess   Status = "success"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

const (
	workerInterval       = 2 * time.Second
	jobHeartbeatInterval = 15 * time.Second
	runningJobStaleAfter = 5 * time.Minute
)

type Job struct {
	ID            string  `db:"id" json:"id"`
	Type          string  `db:"type" json:"type"`
	Status        string  `db:"status" json:"status"`
	Title         string  `db:"title" json:"-"`
	StoragePoolID string  `db:"storage_id" json:"storagePoolId"`
	ResourceType  string  `db:"resource_type" json:"resourceType"`
	ResourceID    string  `db:"resource_id" json:"resourceId"`
	Progress      int     `db:"progress" json:"progress"`
	Message       string  `db:"message" json:"message"`
	ErrorMessage  string  `db:"error_message" json:"errorMessage"`
	PayloadJSON   string  `db:"payload_json" json:"-"`
	ResultJSON    string  `db:"result_json" json:"-"`
	CreatedAt     string  `db:"created_at" json:"createdAt"`
	UpdatedAt     string  `db:"updated_at" json:"updatedAt"`
	StartedAt     *string `db:"started_at" json:"startedAt,omitempty"`
	FinishedAt    *string `db:"finished_at" json:"finishedAt,omitempty"`
}

type CloudSyncPayload struct {
	LocalPath   string `json:"localPath"`
	TargetPath  string `json:"targetPath"`
	ContentType string `json:"contentType"`
	FileName    string `json:"fileName"`
}

type Handler struct{}

type jobStopReason string

const (
	jobStopReasonPause  jobStopReason = "pause"
	jobStopReasonCancel jobStopReason = "cancel"
	jobStopReasonDelete jobStopReason = "delete"
)

var (
	writeJSON             = httpx.WriteJSON
	writeAPIError         = httpx.WriteAPIError
	startWorkerOnce       sync.Once
	runningJobsMu         sync.Mutex
	runningJobCancels     = map[string]context.CancelFunc{}
	runningJobStopReasons = map[string]jobStopReason{}
)

func NewHandler() *Handler {
	return &Handler{}
}

func StartWorker() {
	startWorkerOnce.Do(func() {
		log.Printf("[JOBS] worker starting interval=%s", workerInterval)
		go func() {
			ticker := time.NewTicker(workerInterval)
			defer ticker.Stop()

			for {
				markStaleRunningJobs()
				processOnePendingJob()
				<-ticker.C
			}
		}()
	})
}

func EnqueueCloudSync(storagePoolID string, payload CloudSyncPayload) (*Job, error) {
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode sync job failed: %w", err)
	}

	now := time.Now().Format(time.RFC3339)
	item := &Job{
		ID:            idgen.New(),
		Type:          string(TypeCloudSync),
		Status:        string(StatusPending),
		Title:         "Sync to Google Drive",
		StoragePoolID: storagePoolID,
		ResourceType:  "file",
		ResourceID:    payload.TargetPath,
		Progress:      0,
		Message:       "Waiting to sync",
		PayloadJSON:   string(payloadJSON),
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	_, err = database.DB.Exec(
		`INSERT INTO jobs (id, type, status, title, storage_id, resource_type, resource_id, progress, message, error_message, payload_json, result_json, created_at, updated_at, started_at, finished_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.ID, item.Type, item.Status, item.Title, item.StoragePoolID, item.ResourceType, item.ResourceID, item.Progress, item.Message, item.ErrorMessage, item.PayloadJSON, item.ResultJSON, item.CreatedAt, item.UpdatedAt, item.StartedAt, item.FinishedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("create sync job failed: %w", err)
	}
	log.Printf("[JOBS] enqueued id=%s type=%s storagePool=%s local=%s target=%s", item.ID, item.Type, storagePoolID, payload.LocalPath, payload.TargetPath)
	return item, nil
}

func Get(id string) (*Job, error) {
	markStaleRunningJobs()
	return getForResponse(id)
}

func getForResponse(id string) (*Job, error) {
	item, err := getRaw(id)
	if err != nil {
		return nil, err
	}
	normalizeJobForResponse(item)
	return item, nil
}

func getRaw(id string) (*Job, error) {
	var item Job
	err := database.DB.Get(&item, `SELECT id, type, status, title, storage_id, resource_type, resource_id, progress, message, error_message, payload_json, result_json, created_at, updated_at, started_at, finished_at FROM jobs WHERE id = ?`, strings.TrimSpace(id))
	if err != nil {
		return nil, err
	}
	return &item, nil
}

func List(limit int) ([]Job, error) {
	markStaleRunningJobs()
	if limit <= 0 {
		limit = 50
	}
	var items []Job
	err := database.DB.Select(&items, `SELECT id, type, status, title, storage_id, resource_type, resource_id, progress, message, error_message, payload_json, result_json, created_at, updated_at, started_at, finished_at FROM jobs ORDER BY created_at DESC LIMIT ?`, limit)
	for i := range items {
		normalizeJobForResponse(&items[i])
	}
	return items, err
}

func (h *Handler) HandleList(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		fmt.Sscanf(raw, "%d", &limit)
	}
	items, err := List(limit)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "JOBS_LIST_FAILED", "Failed to list jobs: "+err.Error())
		return
	}
	writeJSON(w, map[string]any{"items": items})
}

func (h *Handler) HandleGet(w http.ResponseWriter, r *http.Request) {
	item, err := Get(r.PathValue("jobId"))
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "JOB_NOT_FOUND", "Job not found")
		return
	}
	writeJSON(w, item)
}

func (h *Handler) HandleDelete(w http.ResponseWriter, r *http.Request) {
	jobID := strings.TrimSpace(r.PathValue("jobId"))
	item, err := getRaw(jobID)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "JOB_NOT_FOUND", "Job not found")
		return
	}

	if item.Status == string(StatusRunning) {
		if !requestRunningJobStop(item.ID, jobStopReasonDelete) {
			cleanupJobPayload(item)
		}
	} else {
		cleanupJobPayload(item)
	}
	if err := deleteJob(item.ID); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "JOB_DELETE_FAILED", "Failed to delete job: "+err.Error())
		return
	}
	writeJSON(w, map[string]any{"deleted": true, "id": item.ID})
}

func (h *Handler) HandlePause(w http.ResponseWriter, r *http.Request) {
	jobID := strings.TrimSpace(r.PathValue("jobId"))
	item, err := getRaw(jobID)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "JOB_NOT_FOUND", "Job not found")
		return
	}

	switch Status(item.Status) {
	case StatusPending:
		if err := updatePaused(item.ID, "Sync job paused"); err != nil {
			writeAPIError(w, http.StatusInternalServerError, "JOB_PAUSE_FAILED", "Failed to pause job: "+err.Error())
			return
		}
	case StatusRunning:
		requestRunningJobStop(item.ID, jobStopReasonPause)
		if err := updatePaused(item.ID, "Sync job paused"); err != nil {
			writeAPIError(w, http.StatusInternalServerError, "JOB_PAUSE_FAILED", "Failed to pause job: "+err.Error())
			return
		}
	case StatusPaused:
	default:
		writeAPIError(w, http.StatusConflict, "JOB_NOT_PAUSABLE", "Only pending or running jobs can be paused")
		return
	}

	item, err = Get(item.ID)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "JOB_NOT_FOUND", "Job not found")
		return
	}
	writeJSON(w, item)
}

func (h *Handler) HandleResume(w http.ResponseWriter, r *http.Request) {
	jobID := strings.TrimSpace(r.PathValue("jobId"))
	item, err := getRaw(jobID)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "JOB_NOT_FOUND", "Job not found")
		return
	}
	if item.Status != string(StatusPaused) {
		writeAPIError(w, http.StatusConflict, "JOB_NOT_RESUMABLE", "Only paused jobs can be resumed")
		return
	}
	if err := ensureJobLocalCacheAvailable(item); err != nil {
		writeAPIError(w, http.StatusConflict, "JOB_CACHE_MISSING", err.Error())
		return
	}
	if err := updateResume(item.ID); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "JOB_RESUME_FAILED", "Failed to resume job: "+err.Error())
		return
	}
	item, err = Get(item.ID)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "JOB_NOT_FOUND", "Job not found")
		return
	}
	writeJSON(w, item)
}

func (h *Handler) HandleCancel(w http.ResponseWriter, r *http.Request) {
	jobID := strings.TrimSpace(r.PathValue("jobId"))
	item, err := getRaw(jobID)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "JOB_NOT_FOUND", "Job not found")
		return
	}

	switch Status(item.Status) {
	case StatusPending, StatusPaused:
		cleanupJobPayload(item)
		if err := updateCancelled(item.ID, "Job cancelled"); err != nil {
			writeAPIError(w, http.StatusInternalServerError, "JOB_CANCEL_FAILED", "Failed to cancel job: "+err.Error())
			return
		}
	case StatusRunning:
		requestRunningJobStop(item.ID, jobStopReasonCancel)
		cleanupJobPayload(item)
		if err := updateCancelled(item.ID, "Job cancelled"); err != nil {
			writeAPIError(w, http.StatusInternalServerError, "JOB_CANCEL_FAILED", "Failed to cancel job: "+err.Error())
			return
		}
	case StatusCancelled:
	default:
		writeAPIError(w, http.StatusConflict, "JOB_NOT_CANCELLABLE", "Only pending, paused, or running jobs can be cancelled")
		return
	}

	item, err = Get(item.ID)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "JOB_NOT_FOUND", "Job not found")
		return
	}
	writeJSON(w, item)
}

func processOnePendingJob() {
	item, ok := claimNextPendingJob()
	if !ok || item == nil {
		return
	}

	log.Printf("[JOBS] claimed id=%s type=%s storagePool=%s resource=%s", item.ID, item.Type, item.StoragePoolID, item.ResourceID)

	switch Type(item.Type) {
	case TypeCloudSync:
		runCloudSyncJob(item)
	default:
		_ = updateFailure(item.ID, "Unsupported job type")
	}
}

func claimNextPendingJob() (*Job, bool) {
	var item Job
	err := database.DB.Get(&item, `SELECT id, type, status, title, storage_id, resource_type, resource_id, progress, message, error_message, payload_json, result_json, created_at, updated_at, started_at, finished_at FROM jobs WHERE status = ? ORDER BY created_at ASC LIMIT 1`, string(StatusPending))
	if err != nil {
		return nil, false
	}

	now := time.Now().Format(time.RFC3339)
	result, err := database.DB.Exec(
		`UPDATE jobs SET status = ?, progress = ?, message = ?, started_at = ?, updated_at = ? WHERE id = ? AND status = ?`,
		string(StatusRunning), 5, "Syncing to cloud storage", now, now, item.ID, string(StatusPending),
	)
	if err != nil {
		return nil, false
	}
	rows, err := result.RowsAffected()
	if err != nil || rows == 0 {
		return nil, false
	}
	item.Status = string(StatusRunning)
	item.Progress = 5
	item.Message = "Syncing to cloud storage"
	item.StartedAt = stringPtr(now)
	item.UpdatedAt = now
	return &item, true
}

func markStaleRunningJobs() {
	var items []Job
	if err := database.DB.Select(&items, `SELECT id, type, status, title, storage_id, resource_type, resource_id, progress, message, error_message, payload_json, result_json, created_at, updated_at, started_at, finished_at FROM jobs WHERE status = ?`, string(StatusRunning)); err != nil {
		log.Printf("[JOBS] list running jobs failed err=%v", err)
		return
	}

	cutoff := time.Now().Add(-runningJobStaleAfter)
	for _, item := range items {
		updatedAt, ok := parseJobTime(item.UpdatedAt)
		if !ok || updatedAt.After(cutoff) {
			continue
		}
		message := fmt.Sprintf("Job heartbeat timed out; the backend has not updated this job for more than %s. The job may have stopped or the service may have restarted.", runningJobStaleAfter)
		now := time.Now().Format(time.RFC3339)
		result, err := database.DB.Exec(
			`UPDATE jobs SET status = ?, message = ?, error_message = ?, finished_at = ?, updated_at = ? WHERE id = ? AND status = ?`,
			string(StatusFailed), "Sync job stopped", message, now, now, item.ID, string(StatusRunning),
		)
		if err != nil {
			log.Printf("[JOBS] mark stale running job failed id=%s err=%v", item.ID, err)
			continue
		}
		rows, err := result.RowsAffected()
		if err != nil || rows == 0 {
			continue
		}
		var payload CloudSyncPayload
		if item.Type == string(TypeCloudSync) && json.Unmarshal([]byte(item.PayloadJSON), &payload) == nil {
			cleanupCloudSyncLocalPayload(item.ID, payload)
		}
		log.Printf("[JOBS] marked stale running job failed id=%s updatedAt=%s", item.ID, item.UpdatedAt)
	}
}

func runCloudSyncJob(item *Job) {
	startedAt := time.Now()
	var payload CloudSyncPayload
	if err := json.Unmarshal([]byte(item.PayloadJSON), &payload); err != nil {
		log.Printf("[JOBS] decode payload failed id=%s err=%v", item.ID, err)
		_ = updateFailure(item.ID, "Failed to decode sync job payload: "+err.Error())
		return
	}
	log.Printf("[JOBS] cloud sync start id=%s storagePool=%s local=%s target=%s", item.ID, item.StoragePoolID, payload.LocalPath, payload.TargetPath)

	storageRecord, err := resolveCloudSyncStorage(item.StoragePoolID)
	if err != nil || storageRecord == nil {
		log.Printf("[JOBS] cloud sync storage missing id=%s storagePool=%s err=%v", item.ID, item.StoragePoolID, err)
		_ = updateFailure(item.ID, "Cloud storage not found")
		cleanupCloudSyncLocalPayload(item.ID, payload)
		return
	}

	file, err := os.Open(payload.LocalPath)
	if err != nil {
		log.Printf("[JOBS] cloud sync open local failed id=%s path=%s err=%v", item.ID, payload.LocalPath, err)
		_ = updateFailure(item.ID, "Failed to open local cache file: "+err.Error())
		cleanupCloudSyncLocalPayload(item.ID, payload)
		return
	}
	defer file.Close()

	_ = updateProgress(item.ID, 25, "Uploading to Google Drive")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	unregisterRunningJob := registerRunningJob(item.ID, cancel)
	stopHeartbeat := startJobHeartbeat(item.ID)
	err = storage.UploadGoogleDriveReader(ctx, storageRecord, payload.TargetPath, file, payload.ContentType)
	stopHeartbeat()
	stopReason := unregisterRunningJob()
	if err != nil {
		status := currentJobStatus(item.ID)
		if errors.Is(err, context.Canceled) {
			_ = file.Close()
			switch stopReason {
			case jobStopReasonPause:
				_ = updatePaused(item.ID, "Sync job paused")
			case jobStopReasonCancel:
				cleanupCloudSyncLocalPayload(item.ID, payload)
				_ = updateCancelled(item.ID, "Job cancelled")
			case jobStopReasonDelete:
				cleanupCloudSyncLocalPayload(item.ID, payload)
				_ = deleteJob(item.ID)
			default:
				_ = updateFailure(item.ID, "Sync job was interrupted")
			}
			return
		}
		switch status {
		case StatusPaused:
			return
		case StatusCancelled:
			_ = file.Close()
			cleanupCloudSyncLocalPayload(item.ID, payload)
			return
		}
		log.Printf("[JOBS] cloud sync upload failed id=%s storagePool=%s target=%s err=%v", item.ID, item.StoragePoolID, payload.TargetPath, err)
		_ = updateFailure(item.ID, "Failed to upload to Google Drive: "+err.Error())
		_ = file.Close()
		cleanupCloudSyncLocalPayload(item.ID, payload)
		return
	}

	if status := currentJobStatus(item.ID); status != StatusRunning {
		switch status {
		case StatusCancelled:
			cleanupCloudSyncLocalPayload(item.ID, payload)
		}
		return
	}
	cleanupCloudSyncLocalPayload(item.ID, payload)
	log.Printf("[JOBS] cloud sync success id=%s target=%s duration=%s", item.ID, payload.TargetPath, time.Since(startedAt))
	_ = updateSuccess(item.ID, map[string]any{
		"targetPath": payload.TargetPath,
		"fileName":   payload.FileName,
	})
}

func startJobHeartbeat(jobID string) func() {
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(jobHeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := updateHeartbeat(jobID); err != nil {
					log.Printf("[JOBS] heartbeat failed id=%s err=%v", jobID, err)
				}
			case <-done:
				return
			}
		}
	}()
	return func() {
		close(done)
	}
}

func registerRunningJob(jobID string, cancel context.CancelFunc) func() jobStopReason {
	runningJobsMu.Lock()
	runningJobCancels[jobID] = cancel
	delete(runningJobStopReasons, jobID)
	runningJobsMu.Unlock()

	return func() jobStopReason {
		runningJobsMu.Lock()
		defer runningJobsMu.Unlock()
		reason := runningJobStopReasons[jobID]
		delete(runningJobCancels, jobID)
		delete(runningJobStopReasons, jobID)
		return reason
	}
}

func requestRunningJobStop(jobID string, reason jobStopReason) bool {
	runningJobsMu.Lock()
	cancel := runningJobCancels[jobID]
	if cancel != nil {
		runningJobStopReasons[jobID] = reason
	}
	runningJobsMu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}

func cleanupCloudSyncLocalPayload(jobID string, payload CloudSyncPayload) {
	localPath := strings.TrimSpace(payload.LocalPath)
	if localPath == "" {
		return
	}
	if err := os.Remove(localPath); err != nil && !os.IsNotExist(err) {
		log.Printf("[JOBS] cloud sync cleanup local failed id=%s path=%s err=%v", jobID, localPath, err)
	}
	if err := os.Remove(filepath.Dir(localPath)); err != nil && !os.IsNotExist(err) {
		log.Printf("[JOBS] cloud sync cleanup dir skipped id=%s dir=%s err=%v", jobID, filepath.Dir(localPath), err)
	}
}

func cleanupJobPayload(item *Job) {
	if item == nil || item.Type != string(TypeCloudSync) || strings.TrimSpace(item.PayloadJSON) == "" {
		return
	}
	var payload CloudSyncPayload
	if err := json.Unmarshal([]byte(item.PayloadJSON), &payload); err != nil {
		log.Printf("[JOBS] cleanup payload decode failed id=%s err=%v", item.ID, err)
		return
	}
	cleanupCloudSyncLocalPayload(item.ID, payload)
}

func ensureJobLocalCacheAvailable(item *Job) error {
	if item == nil || item.Type != string(TypeCloudSync) || strings.TrimSpace(item.PayloadJSON) == "" {
		return nil
	}
	var payload CloudSyncPayload
	if err := json.Unmarshal([]byte(item.PayloadJSON), &payload); err != nil {
		return fmt.Errorf("failed to decode job payload: %w", err)
	}
	localPath := strings.TrimSpace(payload.LocalPath)
	if localPath == "" {
		return fmt.Errorf("local cache file path is missing")
	}
	if _, err := os.Stat(localPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("local cache file is missing; please upload the file again")
		}
		return fmt.Errorf("failed to check local cache file: %w", err)
	}
	return nil
}

func currentJobStatus(jobID string) Status {
	item, err := getRaw(jobID)
	if err != nil || item == nil {
		return ""
	}
	return Status(item.Status)
}

func resolveCloudSyncStorage(storagePoolID string) (*storage.Storage, error) {
	id := strings.TrimSpace(storagePoolID)
	if id == "" {
		return nil, fmt.Errorf("storagePoolId is required")
	}
	if storageRecord, err := storage.Get(id); err == nil && storageRecord != nil {
		return storageRecord, nil
	}
	pool, err := storagepool.Get(id)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(pool.StorageID) == "" {
		return nil, fmt.Errorf("storage pool has no storage id")
	}
	return storage.Get(strings.TrimSpace(pool.StorageID))
}

func updateProgress(jobID string, progress int, message string) error {
	_, err := database.DB.Exec(`UPDATE jobs SET progress = ?, message = ?, updated_at = ? WHERE id = ?`, progress, message, time.Now().Format(time.RFC3339), jobID)
	return err
}

func updateHeartbeat(jobID string) error {
	_, err := database.DB.Exec(`UPDATE jobs SET updated_at = ? WHERE id = ? AND status = ?`, time.Now().Format(time.RFC3339), jobID, string(StatusRunning))
	return err
}

func updatePaused(jobID string, message string) error {
	now := time.Now().Format(time.RFC3339)
	_, err := database.DB.Exec(
		`UPDATE jobs SET status = ?, message = ?, error_message = '', updated_at = ? WHERE id = ? AND status IN (?, ?)`,
		string(StatusPaused), message, now, jobID, string(StatusPending), string(StatusRunning),
	)
	return err
}

func updateResume(jobID string) error {
	now := time.Now().Format(time.RFC3339)
	_, err := database.DB.Exec(
		`UPDATE jobs SET status = ?, progress = ?, message = ?, error_message = '', finished_at = NULL, updated_at = ? WHERE id = ? AND status = ?`,
		string(StatusPending), 0, "Waiting to sync", now, jobID, string(StatusPaused),
	)
	return err
}

func updateCancelled(jobID string, message string) error {
	now := time.Now().Format(time.RFC3339)
	_, err := database.DB.Exec(
		`UPDATE jobs SET status = ?, progress = ?, message = ?, error_message = '', finished_at = ?, updated_at = ? WHERE id = ?`,
		string(StatusCancelled), 100, message, now, now, jobID,
	)
	return err
}

func updateFailure(jobID string, message string) error {
	now := time.Now().Format(time.RFC3339)
	_, err := database.DB.Exec(
		`UPDATE jobs SET status = ?, progress = ?, message = ?, error_message = ?, finished_at = ?, updated_at = ? WHERE id = ? AND status = ?`,
		string(StatusFailed), 100, "Sync failed", message, now, now, jobID, string(StatusRunning),
	)
	return err
}

func deleteJob(jobID string) error {
	_, err := database.DB.Exec(`DELETE FROM jobs WHERE id = ?`, strings.TrimSpace(jobID))
	return err
}

func updateSuccess(jobID string, result any) error {
	encoded, _ := json.Marshal(result)
	now := time.Now().Format(time.RFC3339)
	_, err := database.DB.Exec(
		`UPDATE jobs SET status = ?, progress = ?, message = ?, result_json = ?, error_message = '', finished_at = ?, updated_at = ? WHERE id = ? AND status = ?`,
		string(StatusSuccess), 100, "Sync completed", string(encoded), now, now, jobID, string(StatusRunning),
	)
	return err
}

func stringPtr(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return &value
}

func parseJobTime(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05-07:00",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
	}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, true
		}
	}
	if parsed, err := time.ParseInLocation("2006-01-02 15:04:05", value, time.Local); err == nil {
		return parsed, true
	}
	return time.Time{}, false
}

func normalizeJobForResponse(item *Job) {
	if item == nil {
		return
	}
	item.Message = normalizeLegacyJobText(item.Message)
	item.ErrorMessage = normalizeLegacyJobText(item.ErrorMessage)
}

func normalizeLegacyJobText(value string) string {
	value = strings.TrimSpace(value)
	switch value {
	case "同步到 Google Drive":
		return "Sync to Google Drive"
	case "等待同步":
		return "Waiting to sync"
	case "正在同步到云盘":
		return "Syncing to cloud storage"
	case "正在上传到 Google Drive":
		return "Uploading to Google Drive"
	case "同步任务已停止":
		return "Sync job stopped"
	case "同步失败":
		return "Sync failed"
	case "同步完成":
		return "Sync completed"
	case "不支持的任务类型":
		return "Unsupported job type"
	case "云存储不存在":
		return "Cloud storage not found"
	}
	replacements := map[string]string{
		"解析同步任务失败":            "Failed to decode sync job payload",
		"打开本地缓存文件失败":          "Failed to open local cache file",
		"上传到 Google Drive 失败": "Failed to upload to Google Drive",
		"任务心跳超时":              "Job heartbeat timed out",
		"后台超过":                "the backend has not updated this job for more than",
		"没有更新；任务可能已停止或服务曾经重启": "The job may have stopped or the service may have restarted.",
	}
	for oldText, newText := range replacements {
		value = strings.ReplaceAll(value, oldText, newText)
	}
	return value
}
