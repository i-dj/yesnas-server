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
	"nas-server/internal/audit"
	"nas-server/internal/storage"
	"nas-server/internal/storagepool"
	"nas-server/pkg/httpx"
	"nas-server/pkg/idgen"
)

type Type string

const (
	TypeCloudSync    Type = "cloud_sync"
	TypeAutoSnapshot Type = "auto_snapshot"
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
	Title         string  `db:"title" json:"title"`
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

type JobListQuery struct {
	Page     int
	PageSize int
	Status   string
	Search   string
}

type JobListItem struct {
	Job
	StoragePoolName string `json:"storagePoolName,omitempty"`
	Schedule        string `json:"schedule,omitempty"`
}

type JobListCounts struct {
	All       int `json:"all"`
	Pending   int `json:"pending"`
	Running   int `json:"running"`
	Paused    int `json:"paused"`
	Success   int `json:"success"`
	Failed    int `json:"failed"`
	Cancelled int `json:"cancelled"`
}

type JobListPagination struct {
	Page       int `json:"page"`
	PageSize   int `json:"pageSize"`
	Total      int `json:"total"`
	TotalPages int `json:"totalPages"`
}

type JobListResponse struct {
	Items      []JobListItem     `json:"items"`
	Pagination JobListPagination `json:"pagination"`
	Counts     JobListCounts     `json:"counts"`
}

type ScheduledTask struct {
	ID              string  `json:"id"`
	Type            string  `json:"type"`
	Action          string  `json:"action"`
	Status          string  `json:"status"`
	Enabled         bool    `json:"enabled"`
	Schedule        string  `json:"schedule"`
	NextRunAt       *string `json:"nextRunAt,omitempty"`
	LastRunAt       *string `json:"lastRunAt,omitempty"`
	StoragePoolID   string  `json:"storagePoolId"`
	StoragePoolName string  `json:"storagePoolName"`
	ResourceType    string  `json:"resourceType"`
	ResourceID      string  `json:"resourceId"`
}

type CloudSyncPayload struct {
	LocalPath   string `json:"localPath"`
	TargetPath  string `json:"targetPath"`
	ContentType string `json:"contentType"`
	FileName    string `json:"fileName"`
}

type AutoSnapshotPayload struct {
	PoolID   string `json:"poolId"`
	Schedule string `json:"schedule"`
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
				enqueueDueAutoSnapshotJobs()
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

	now := time.Now().UTC().Format(time.RFC3339)
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

func ListJobs(query JobListQuery) (*JobListResponse, error) {
	markStaleRunningJobs()
	page := query.Page
	if page <= 0 {
		page = 1
	}
	pageSize := query.PageSize
	if pageSize <= 0 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}

	status := normalizeListStatus(query.Status)
	search := strings.TrimSpace(query.Search)
	whereParts := []string{}
	args := []any{}
	if status != "" {
		whereParts = append(whereParts, "status = ?")
		args = append(args, status)
	}
	if search != "" {
		like := "%" + search + "%"
		whereParts = append(whereParts, "(title LIKE ? OR message LIKE ? OR error_message LIKE ? OR status LIKE ? OR type LIKE ?)")
		args = append(args, like, like, like, like, like)
	}

	whereSQL := ""
	if len(whereParts) > 0 {
		whereSQL = " WHERE " + strings.Join(whereParts, " AND ")
	}

	var total int
	countQuery := `SELECT COUNT(1) FROM jobs` + whereSQL
	if err := database.DB.Get(&total, countQuery, args...); err != nil {
		return nil, err
	}

	offset := (page - 1) * pageSize
	listArgs := append(append([]any{}, args...), pageSize, offset)
	rows := []Job{}
	listQuery := `SELECT id, type, status, title, storage_id, resource_type, resource_id, progress, message, error_message, payload_json, result_json, created_at, updated_at, started_at, finished_at
		FROM jobs` + whereSQL + ` ORDER BY created_at DESC LIMIT ? OFFSET ?`
	if err := database.DB.Select(&rows, listQuery, listArgs...); err != nil {
		return nil, err
	}

	items, err := buildJobListItems(rows)
	if err != nil {
		return nil, err
	}
	counts, err := getJobCounts()
	if err != nil {
		return nil, err
	}

	totalPages := 0
	if total > 0 {
		totalPages = (total + pageSize - 1) / pageSize
	}
	return &JobListResponse{
		Items: items,
		Pagination: JobListPagination{
			Page:       page,
			PageSize:   pageSize,
			Total:      total,
			TotalPages: totalPages,
		},
		Counts: counts,
	}, nil
}

func ListScheduledTasks() ([]ScheduledTask, error) {
	pools, err := storagepool.List()
	if err != nil {
		return nil, err
	}
	items := make([]ScheduledTask, 0, len(pools))
	for _, pool := range pools {
		if !pool.AutoSnapshotEnabled || strings.TrimSpace(pool.AutoSnapshotSchedule) == "" {
			continue
		}
		items = append(items, ScheduledTask{
			ID:              "auto_snapshot:" + pool.ID,
			Type:            string(TypeAutoSnapshot),
			Action:          "snapshot",
			Status:          "scheduled",
			Enabled:         true,
			Schedule:        pool.AutoSnapshotSchedule,
			NextRunAt:       formatOptionalTime(pool.NextAutoSnapshotAt),
			LastRunAt:       formatOptionalTime(pool.LastAutoSnapshotAt),
			StoragePoolID:   pool.ID,
			StoragePoolName: pool.Name,
			ResourceType:    "storage_pool",
			ResourceID:      pool.ID,
		})
	}
	return items, nil
}

func (h *Handler) HandleList(w http.ResponseWriter, r *http.Request) {
	page := parsePositiveIntQuery(r, "page", 1)
	pageSize := parsePositiveIntQuery(r, "pageSize", 20)
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		pageSize = parsePositiveInt(raw, pageSize)
	}
	result, err := ListJobs(JobListQuery{
		Page:     page,
		PageSize: pageSize,
		Status:   r.URL.Query().Get("status"),
		Search:   r.URL.Query().Get("q"),
	})
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "JOBS_LIST_FAILED", "Failed to list jobs: "+err.Error())
		return
	}
	writeJSON(w, result)
}

func (h *Handler) HandleScheduledList(w http.ResponseWriter, r *http.Request) {
	items, err := ListScheduledTasks()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "JOBS_SCHEDULED_LIST_FAILED", "Failed to list scheduled jobs: "+err.Error())
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
		audit.UserAction(r.Context(), "job_delete_failed", "delete", false, "job", item.ID, item.Title, "Failed to delete job: "+err.Error(), nil)
		writeAPIError(w, http.StatusInternalServerError, "JOB_DELETE_FAILED", "Failed to delete job: "+err.Error())
		return
	}
	audit.UserAction(r.Context(), "job_deleted", "delete", true, "job", item.ID, item.Title, "Job deleted", nil)
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
		if err := updatePaused(item.ID, pausedStatusMessage(Type(item.Type))); err != nil {
			audit.UserAction(r.Context(), "job_pause_failed", "pause", false, "job", item.ID, item.Title, "Failed to pause job: "+err.Error(), nil)
			writeAPIError(w, http.StatusInternalServerError, "JOB_PAUSE_FAILED", "Failed to pause job: "+err.Error())
			return
		}
	case StatusRunning:
		requestRunningJobStop(item.ID, jobStopReasonPause)
		if err := updatePaused(item.ID, pausedStatusMessage(Type(item.Type))); err != nil {
			audit.UserAction(r.Context(), "job_pause_failed", "pause", false, "job", item.ID, item.Title, "Failed to pause job: "+err.Error(), nil)
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
	audit.UserAction(r.Context(), "job_paused", "pause", true, "job", item.ID, item.Title, "Job paused", nil)
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
	if err := updateResume(item.ID, Type(item.Type)); err != nil {
		audit.UserAction(r.Context(), "job_resume_failed", "resume", false, "job", item.ID, item.Title, "Failed to resume job: "+err.Error(), nil)
		writeAPIError(w, http.StatusInternalServerError, "JOB_RESUME_FAILED", "Failed to resume job: "+err.Error())
		return
	}
	item, err = Get(item.ID)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "JOB_NOT_FOUND", "Job not found")
		return
	}
	audit.UserAction(r.Context(), "job_resumed", "resume", true, "job", item.ID, item.Title, "Job resumed", nil)
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
			audit.UserAction(r.Context(), "job_cancel_failed", "cancel", false, "job", item.ID, item.Title, "Failed to cancel job: "+err.Error(), nil)
			writeAPIError(w, http.StatusInternalServerError, "JOB_CANCEL_FAILED", "Failed to cancel job: "+err.Error())
			return
		}
	case StatusRunning:
		requestRunningJobStop(item.ID, jobStopReasonCancel)
		cleanupJobPayload(item)
		if err := updateCancelled(item.ID, "Job cancelled"); err != nil {
			audit.UserAction(r.Context(), "job_cancel_failed", "cancel", false, "job", item.ID, item.Title, "Failed to cancel job: "+err.Error(), nil)
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
	audit.UserAction(r.Context(), "job_cancelled", "cancel", true, "job", item.ID, item.Title, "Job cancelled", nil)
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
	case TypeAutoSnapshot:
		runAutoSnapshotJob(item)
	default:
		_ = updateFailure(item.ID, "Unsupported job type", "Job failed")
	}
}

func claimNextPendingJob() (*Job, bool) {
	var item Job
	err := database.DB.Get(&item, `SELECT id, type, status, title, storage_id, resource_type, resource_id, progress, message, error_message, payload_json, result_json, created_at, updated_at, started_at, finished_at FROM jobs WHERE status = ? ORDER BY created_at ASC LIMIT 1`, string(StatusPending))
	if err != nil {
		return nil, false
	}

	now := time.Now().UTC().Format(time.RFC3339)
	runningMessage := runningMessageForType(Type(item.Type))
	result, err := database.DB.Exec(
		`UPDATE jobs SET status = ?, progress = ?, message = ?, started_at = ?, updated_at = ? WHERE id = ? AND status = ?`,
		string(StatusRunning), 5, runningMessage, now, now, item.ID, string(StatusPending),
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
	item.Message = runningMessage
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

	cutoff := time.Now().UTC().Add(-runningJobStaleAfter)
	for _, item := range items {
		updatedAt, ok := parseJobTime(item.UpdatedAt)
		if !ok || updatedAt.After(cutoff) {
			continue
		}
		message := fmt.Sprintf("Job heartbeat timed out; the backend has not updated this job for more than %s. The job may have stopped or the service may have restarted.", runningJobStaleAfter)
		statusMessage := failureStatusMessage(Type(item.Type))
		now := time.Now().UTC().Format(time.RFC3339)
		result, err := database.DB.Exec(
			`UPDATE jobs SET status = ?, message = ?, error_message = ?, finished_at = ?, updated_at = ? WHERE id = ? AND status = ?`,
			string(StatusFailed), statusMessage, message, now, now, item.ID, string(StatusRunning),
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
		_ = updateFailure(item.ID, "Failed to decode sync job payload: "+err.Error(), failureStatusMessage(TypeCloudSync))
		return
	}
	log.Printf("[JOBS] cloud sync start id=%s storagePool=%s local=%s target=%s", item.ID, item.StoragePoolID, payload.LocalPath, payload.TargetPath)

	storageRecord, err := resolveCloudSyncStorage(item.StoragePoolID)
	if err != nil || storageRecord == nil {
		log.Printf("[JOBS] cloud sync storage missing id=%s storagePool=%s err=%v", item.ID, item.StoragePoolID, err)
		_ = updateFailure(item.ID, "Cloud storage not found", failureStatusMessage(TypeCloudSync))
		cleanupCloudSyncLocalPayload(item.ID, payload)
		return
	}

	file, err := os.Open(payload.LocalPath)
	if err != nil {
		log.Printf("[JOBS] cloud sync open local failed id=%s path=%s err=%v", item.ID, payload.LocalPath, err)
		_ = updateFailure(item.ID, "Failed to open local cache file: "+err.Error(), failureStatusMessage(TypeCloudSync))
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
				_ = updatePaused(item.ID, pausedStatusMessage(TypeCloudSync))
			case jobStopReasonCancel:
				cleanupCloudSyncLocalPayload(item.ID, payload)
				_ = updateCancelled(item.ID, "Job cancelled")
			case jobStopReasonDelete:
				cleanupCloudSyncLocalPayload(item.ID, payload)
				_ = deleteJob(item.ID)
			default:
				_ = updateFailure(item.ID, "Sync job was interrupted", failureStatusMessage(TypeCloudSync))
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
		_ = updateFailure(item.ID, "Failed to upload to Google Drive: "+err.Error(), failureStatusMessage(TypeCloudSync))
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
	}, successStatusMessage(TypeCloudSync))
}

func enqueueDueAutoSnapshotJobs() {
	pools, err := storagepool.List()
	if err != nil {
		log.Printf("[JOBS] list auto snapshot pools failed err=%v", err)
		return
	}
	now := time.Now().UTC()
	for _, pool := range pools {
		if !pool.AutoSnapshotEnabled || strings.TrimSpace(pool.AutoSnapshotSchedule) == "" || pool.NextAutoSnapshotAt == nil {
			continue
		}
		if pool.NextAutoSnapshotAt.After(now) {
			continue
		}
		exists, err := hasPendingOrRunningAutoSnapshotJob(pool.ID)
		if err != nil {
			log.Printf("[JOBS] check auto snapshot pending job failed pool=%s err=%v", pool.ID, err)
			continue
		}
		if exists {
			continue
		}
		next := nextAutoSnapshotAfter(pool.NextAutoSnapshotAt, pool.AutoSnapshotSchedule, now)
		if err := storagepool.UpdateAutoSnapshotQueued(pool.ID, next); err != nil {
			log.Printf("[JOBS] update auto snapshot queued failed pool=%s err=%v", pool.ID, err)
			continue
		}
		if _, err := enqueueAutoSnapshotJob(pool, now); err != nil {
			log.Printf("[JOBS] enqueue auto snapshot job failed pool=%s err=%v", pool.ID, err)
			continue
		}
	}
}

func enqueueAutoSnapshotJob(pool storagepool.StoragePool, now time.Time) (*Job, error) {
	payload := AutoSnapshotPayload{
		PoolID:   pool.ID,
		Schedule: pool.AutoSnapshotSchedule,
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode auto snapshot payload failed: %w", err)
	}
	item := &Job{
		ID:            idgen.New(),
		Type:          string(TypeAutoSnapshot),
		Status:        string(StatusPending),
		Title:         "Automatic snapshot",
		StoragePoolID: pool.ID,
		ResourceType:  "storage_pool",
		ResourceID:    pool.ID,
		Progress:      0,
		Message:       "Waiting to create automatic snapshot",
		PayloadJSON:   string(payloadJSON),
		CreatedAt:     now.UTC().Format(time.RFC3339),
		UpdatedAt:     now.UTC().Format(time.RFC3339),
	}
	_, err = database.DB.Exec(
		`INSERT INTO jobs (id, type, status, title, storage_id, resource_type, resource_id, progress, message, error_message, payload_json, result_json, created_at, updated_at, started_at, finished_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.ID, item.Type, item.Status, item.Title, item.StoragePoolID, item.ResourceType, item.ResourceID, item.Progress, item.Message, item.ErrorMessage, item.PayloadJSON, item.ResultJSON, item.CreatedAt, item.UpdatedAt, item.StartedAt, item.FinishedAt,
	)
	if err != nil {
		return nil, err
	}
	log.Printf("[JOBS] enqueued id=%s type=%s pool=%s schedule=%s", item.ID, item.Type, pool.ID, pool.AutoSnapshotSchedule)
	audit.AutoSnapshotScheduled(context.Background(), pool.ID, pool.Name, map[string]any{
		"jobId":     item.ID,
		"schedule":  pool.AutoSnapshotSchedule,
		"nextRunAt": pool.NextAutoSnapshotAt,
		"storageId": pool.StorageID,
	})
	return item, nil
}

func hasPendingOrRunningAutoSnapshotJob(poolID string) (bool, error) {
	var count int
	if err := database.DB.Get(&count, `SELECT COUNT(1) FROM jobs WHERE type = ? AND resource_id = ? AND status IN (?, ?)`, string(TypeAutoSnapshot), poolID, string(StatusPending), string(StatusRunning)); err != nil {
		return false, err
	}
	return count > 0, nil
}

func nextAutoSnapshotAfter(lastScheduledAt *time.Time, schedule string, now time.Time) time.Time {
	base := now
	if lastScheduledAt != nil && !lastScheduledAt.IsZero() {
		base = *lastScheduledAt
	}
	next := storagepool.NextAutoSnapshotTime(base, schedule)
	for !next.After(now) {
		next = storagepool.NextAutoSnapshotTime(next, schedule)
	}
	return next
}

func runAutoSnapshotJob(item *Job) {
	startedAt := time.Now()
	var payload AutoSnapshotPayload
	if err := json.Unmarshal([]byte(item.PayloadJSON), &payload); err != nil {
		log.Printf("[JOBS] decode auto snapshot payload failed id=%s err=%v", item.ID, err)
		audit.AutoSnapshotFailed(context.Background(), "job", item.ID, item.Title, "Failed to decode automatic snapshot payload: "+err.Error(), nil)
		_ = updateFailure(item.ID, "Failed to decode auto snapshot payload: "+err.Error(), failureStatusMessage(TypeAutoSnapshot))
		return
	}

	pool, err := storagepool.Get(strings.TrimSpace(payload.PoolID))
	if err != nil {
		log.Printf("[JOBS] auto snapshot pool missing id=%s pool=%s err=%v", item.ID, payload.PoolID, err)
		audit.AutoSnapshotFailed(context.Background(), "storage_pool", payload.PoolID, "", "Storage pool not found for automatic snapshot", map[string]any{"jobId": item.ID})
		_ = updateFailure(item.ID, "Storage pool not found", failureStatusMessage(TypeAutoSnapshot))
		return
	}
	if !pool.AutoSnapshotEnabled || strings.TrimSpace(pool.AutoSnapshotSchedule) == "" {
		log.Printf("[JOBS] auto snapshot disabled id=%s pool=%s", item.ID, pool.ID)
		audit.AutoSnapshotFailed(context.Background(), "storage_pool", pool.ID, pool.Name, "Automatic snapshot is disabled for this storage pool", map[string]any{"jobId": item.ID})
		_ = updateFailure(item.ID, "Automatic snapshot is disabled for this storage pool", failureStatusMessage(TypeAutoSnapshot))
		return
	}

	_ = updateProgress(item.ID, 25, "Creating automatic snapshot")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	unregisterRunningJob := registerRunningJob(item.ID, cancel)
	stopHeartbeat := startJobHeartbeat(item.ID)
	snapshot, err := storagepool.CreateSnapshot(ctx, pool, storagepool.CreateSnapshotRequest{
		Name:        autoSnapshotName(payload.Schedule, startedAt),
		Description: fmt.Sprintf("automatic %s snapshot", strings.TrimSpace(payload.Schedule)),
		CreatedBy:   "auto-snapshot",
	})
	stopHeartbeat()
	stopReason := unregisterRunningJob()
	if err != nil {
		if errors.Is(err, context.Canceled) {
			switch stopReason {
			case jobStopReasonPause:
				_ = updatePaused(item.ID, pausedStatusMessage(TypeAutoSnapshot))
			case jobStopReasonCancel:
				_ = updateCancelled(item.ID, "Job cancelled")
			case jobStopReasonDelete:
				_ = deleteJob(item.ID)
			default:
				_ = updateFailure(item.ID, "Automatic snapshot was interrupted", failureStatusMessage(TypeAutoSnapshot))
			}
			return
		}
		log.Printf("[JOBS] auto snapshot failed id=%s pool=%s err=%v", item.ID, pool.ID, err)
		audit.AutoSnapshotFailed(context.Background(), "storage_pool", pool.ID, pool.Name, "Failed to create automatic snapshot: "+err.Error(), map[string]any{"jobId": item.ID, "schedule": pool.AutoSnapshotSchedule})
		_ = updateFailure(item.ID, "Failed to create automatic snapshot: "+err.Error(), failureStatusMessage(TypeAutoSnapshot))
		return
	}

	if status := currentJobStatus(item.ID); status != StatusRunning {
		return
	}

	completedAt := time.Now().UTC()
	next := storagepool.NextAutoSnapshotTime(completedAt, pool.AutoSnapshotSchedule)
	if err := storagepool.UpdateAutoSnapshotSuccess(pool.ID, completedAt, next); err != nil {
		log.Printf("[JOBS] auto snapshot metadata update failed id=%s pool=%s err=%v", item.ID, pool.ID, err)
		audit.AutoSnapshotFailed(context.Background(), "storage_pool", pool.ID, pool.Name, "Snapshot created but schedule update failed: "+err.Error(), map[string]any{"jobId": item.ID, "snapshotName": snapshot.Name})
		_ = updateFailure(item.ID, "Snapshot created but failed to update auto snapshot schedule: "+err.Error(), failureStatusMessage(TypeAutoSnapshot))
		return
	}

	log.Printf("[JOBS] auto snapshot success id=%s pool=%s snapshot=%s duration=%s", item.ID, pool.ID, snapshot.Name, time.Since(startedAt))
	audit.AutoSnapshotCompleted(context.Background(), pool.ID, pool.Name, map[string]any{
		"jobId":        item.ID,
		"schedule":     pool.AutoSnapshotSchedule,
		"snapshotId":   snapshot.MetadataID,
		"snapshotName": snapshot.Name,
		"snapshotPath": snapshot.Path,
		"nextRunAt":    next.UTC().Format(time.RFC3339),
	})
	_ = updateSuccess(item.ID, map[string]any{
		"poolId":     pool.ID,
		"schedule":   pool.AutoSnapshotSchedule,
		"snapshotId": snapshot.MetadataID,
		"name":       snapshot.Name,
		"path":       snapshot.Path,
	}, successStatusMessage(TypeAutoSnapshot))
}

func autoSnapshotName(schedule string, now time.Time) string {
	return fmt.Sprintf("auto-%s-%s", strings.TrimSpace(schedule), now.In(time.Local).Format("20060102-150405"))
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
	_, err := database.DB.Exec(`UPDATE jobs SET progress = ?, message = ?, updated_at = ? WHERE id = ?`, progress, message, time.Now().UTC().Format(time.RFC3339), jobID)
	return err
}

func updateHeartbeat(jobID string) error {
	_, err := database.DB.Exec(`UPDATE jobs SET updated_at = ? WHERE id = ? AND status = ?`, time.Now().UTC().Format(time.RFC3339), jobID, string(StatusRunning))
	return err
}

func updatePaused(jobID string, message string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := database.DB.Exec(
		`UPDATE jobs SET status = ?, message = ?, error_message = '', updated_at = ? WHERE id = ? AND status IN (?, ?)`,
		string(StatusPaused), message, now, jobID, string(StatusPending), string(StatusRunning),
	)
	return err
}

func updateResume(jobID string, jobType Type) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := database.DB.Exec(
		`UPDATE jobs SET status = ?, progress = ?, message = ?, error_message = '', finished_at = NULL, updated_at = ? WHERE id = ? AND status = ?`,
		string(StatusPending), 0, pendingStatusMessage(jobType), now, jobID, string(StatusPaused),
	)
	return err
}

func updateCancelled(jobID string, message string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := database.DB.Exec(
		`UPDATE jobs SET status = ?, progress = ?, message = ?, error_message = '', finished_at = ?, updated_at = ? WHERE id = ?`,
		string(StatusCancelled), 100, message, now, now, jobID,
	)
	return err
}

func updateFailure(jobID string, message string, statusMessage string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := database.DB.Exec(
		`UPDATE jobs SET status = ?, progress = ?, message = ?, error_message = ?, finished_at = ?, updated_at = ? WHERE id = ? AND status = ?`,
		string(StatusFailed), 100, statusMessage, message, now, now, jobID, string(StatusRunning),
	)
	return err
}

func deleteJob(jobID string) error {
	_, err := database.DB.Exec(`DELETE FROM jobs WHERE id = ?`, strings.TrimSpace(jobID))
	return err
}

func updateSuccess(jobID string, result any, statusMessage string) error {
	encoded, _ := json.Marshal(result)
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := database.DB.Exec(
		`UPDATE jobs SET status = ?, progress = ?, message = ?, result_json = ?, error_message = '', finished_at = ?, updated_at = ? WHERE id = ? AND status = ?`,
		string(StatusSuccess), 100, statusMessage, string(encoded), now, now, jobID, string(StatusRunning),
	)
	return err
}

func pendingStatusMessage(jobType Type) string {
	switch jobType {
	case TypeAutoSnapshot:
		return "Waiting to create automatic snapshot"
	default:
		return "Waiting to sync"
	}
}

func runningMessageForType(jobType Type) string {
	switch jobType {
	case TypeAutoSnapshot:
		return "Creating automatic snapshot"
	default:
		return "Syncing to cloud storage"
	}
}

func pausedStatusMessage(jobType Type) string {
	switch jobType {
	case TypeAutoSnapshot:
		return "Automatic snapshot paused"
	default:
		return "Sync job paused"
	}
}

func successStatusMessage(jobType Type) string {
	switch jobType {
	case TypeAutoSnapshot:
		return "Automatic snapshot completed"
	default:
		return "Sync completed"
	}
}

func failureStatusMessage(jobType Type) string {
	switch jobType {
	case TypeAutoSnapshot:
		return "Automatic snapshot failed"
	default:
		return "Sync failed"
	}
}

func buildJobListItems(rows []Job) ([]JobListItem, error) {
	poolMap, err := loadStoragePoolNameMap()
	if err != nil {
		return nil, err
	}
	items := make([]JobListItem, 0, len(rows))
	for i := range rows {
		normalizeJobForResponse(&rows[i])
		item := JobListItem{Job: rows[i]}
		if item.Type == string(TypeAutoSnapshot) {
			var payload AutoSnapshotPayload
			if err := json.Unmarshal([]byte(rows[i].PayloadJSON), &payload); err == nil {
				item.Schedule = payload.Schedule
				if item.StoragePoolID == "" {
					item.StoragePoolID = payload.PoolID
				}
			}
		}
		if poolName := resolveJobStoragePoolName(poolMap, item.Job); poolName != "" {
			item.StoragePoolName = poolName
		}
		items = append(items, item)
	}
	return items, nil
}

func loadStoragePoolNameMap() (map[string]string, error) {
	pools, err := storagepool.List()
	if err != nil {
		return nil, err
	}
	result := make(map[string]string, len(pools))
	for _, pool := range pools {
		result[pool.ID] = pool.Name
		if strings.TrimSpace(pool.StorageID) != "" {
			result[pool.StorageID] = pool.Name
		}
	}
	return result, nil
}

func resolveJobStoragePoolName(poolMap map[string]string, job Job) string {
	if name := strings.TrimSpace(poolMap[strings.TrimSpace(job.StoragePoolID)]); name != "" {
		return name
	}
	if name := strings.TrimSpace(poolMap[strings.TrimSpace(job.ResourceID)]); name != "" {
		return name
	}
	return ""
}

func getJobCounts() (JobListCounts, error) {
	type countsRow struct {
		All       int `db:"all_count"`
		Pending   int `db:"pending_count"`
		Running   int `db:"running_count"`
		Paused    int `db:"paused_count"`
		Success   int `db:"success_count"`
		Failed    int `db:"failed_count"`
		Cancelled int `db:"cancelled_count"`
	}
	var row countsRow
	err := database.DB.Get(&row, `SELECT
		COUNT(1) AS all_count,
		COALESCE(SUM(CASE WHEN status = 'pending' THEN 1 ELSE 0 END), 0) AS pending_count,
		COALESCE(SUM(CASE WHEN status = 'running' THEN 1 ELSE 0 END), 0) AS running_count,
		COALESCE(SUM(CASE WHEN status = 'paused' THEN 1 ELSE 0 END), 0) AS paused_count,
		COALESCE(SUM(CASE WHEN status = 'success' THEN 1 ELSE 0 END), 0) AS success_count,
		COALESCE(SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END), 0) AS failed_count,
		COALESCE(SUM(CASE WHEN status = 'cancelled' THEN 1 ELSE 0 END), 0) AS cancelled_count
		FROM jobs`)
	if err != nil {
		return JobListCounts{}, err
	}
	return JobListCounts{
		All:       row.All,
		Pending:   row.Pending,
		Running:   row.Running,
		Paused:    row.Paused,
		Success:   row.Success,
		Failed:    row.Failed,
		Cancelled: row.Cancelled,
	}, nil
}

func normalizeListStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "all":
		return ""
	case string(StatusPending), string(StatusRunning), string(StatusPaused), string(StatusSuccess), string(StatusFailed), string(StatusCancelled):
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func parsePositiveIntQuery(r *http.Request, key string, fallback int) int {
	return parsePositiveInt(strings.TrimSpace(r.URL.Query().Get(key)), fallback)
}

func parsePositiveInt(raw string, fallback int) int {
	if raw == "" {
		return fallback
	}
	var value int
	if _, err := fmt.Sscanf(raw, "%d", &value); err != nil || value <= 0 {
		return fallback
	}
	return value
}

func formatOptionalTime(value *time.Time) *string {
	if value == nil || value.IsZero() {
		return nil
	}
	formatted := value.UTC().Format(time.RFC3339)
	return &formatted
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
