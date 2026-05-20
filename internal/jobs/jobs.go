package jobs

import (
	"context"
	"encoding/json"
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
	StatusSuccess   Status = "success"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

const workerInterval = 2 * time.Second

type Job struct {
	ID           string  `db:"id" json:"id"`
	Type         string  `db:"type" json:"type"`
	Status       string  `db:"status" json:"status"`
	Title        string  `db:"title" json:"title"`
	StorageID    string  `db:"storage_id" json:"storageId"`
	ResourceType string  `db:"resource_type" json:"resourceType"`
	ResourceID   string  `db:"resource_id" json:"resourceId"`
	Progress     int     `db:"progress" json:"progress"`
	Message      string  `db:"message" json:"message"`
	ErrorMessage string  `db:"error_message" json:"errorMessage"`
	PayloadJSON  string  `db:"payload_json" json:"-"`
	ResultJSON   string  `db:"result_json" json:"-"`
	CreatedAt    string  `db:"created_at" json:"createdAt"`
	UpdatedAt    string  `db:"updated_at" json:"updatedAt"`
	StartedAt    *string `db:"started_at" json:"startedAt,omitempty"`
	FinishedAt   *string `db:"finished_at" json:"finishedAt,omitempty"`
}

type CloudSyncPayload struct {
	LocalPath   string `json:"localPath"`
	TargetPath  string `json:"targetPath"`
	ContentType string `json:"contentType"`
	FileName    string `json:"fileName"`
}

type Handler struct{}

var (
	writeJSON       = httpx.WriteJSON
	writeAPIError   = httpx.WriteAPIError
	startWorkerOnce sync.Once
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
				processOnePendingJob()
				<-ticker.C
			}
		}()
	})
}

func EnqueueCloudSync(storageID string, payload CloudSyncPayload) (*Job, error) {
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("编码同步任务失败: %w", err)
	}

	now := time.Now().Format(time.RFC3339)
	item := &Job{
		ID:           idgen.New(),
		Type:         string(TypeCloudSync),
		Status:       string(StatusPending),
		Title:        "同步到 Google Drive",
		StorageID:    storageID,
		ResourceType: "file",
		ResourceID:   payload.TargetPath,
		Progress:     0,
		Message:      "等待同步",
		PayloadJSON:  string(payloadJSON),
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	_, err = database.DB.Exec(
		`INSERT INTO jobs (id, type, status, title, storage_id, resource_type, resource_id, progress, message, error_message, payload_json, result_json, created_at, updated_at, started_at, finished_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.ID, item.Type, item.Status, item.Title, item.StorageID, item.ResourceType, item.ResourceID, item.Progress, item.Message, item.ErrorMessage, item.PayloadJSON, item.ResultJSON, item.CreatedAt, item.UpdatedAt, item.StartedAt, item.FinishedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("创建同步任务失败: %w", err)
	}
	log.Printf("[JOBS] enqueued id=%s type=%s storage=%s local=%s target=%s", item.ID, item.Type, storageID, payload.LocalPath, payload.TargetPath)
	return item, nil
}

func Get(id string) (*Job, error) {
	var item Job
	err := database.DB.Get(&item, `SELECT id, type, status, title, storage_id, resource_type, resource_id, progress, message, error_message, payload_json, result_json, created_at, updated_at, started_at, finished_at FROM jobs WHERE id = ?`, strings.TrimSpace(id))
	if err != nil {
		return nil, err
	}
	return &item, nil
}

func List(limit int) ([]Job, error) {
	if limit <= 0 {
		limit = 50
	}
	var items []Job
	err := database.DB.Select(&items, `SELECT id, type, status, title, storage_id, resource_type, resource_id, progress, message, error_message, payload_json, result_json, created_at, updated_at, started_at, finished_at FROM jobs ORDER BY created_at DESC LIMIT ?`, limit)
	return items, err
}

func (h *Handler) HandleList(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		fmt.Sscanf(raw, "%d", &limit)
	}
	items, err := List(limit)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "JOBS_LIST_FAILED", "获取任务列表失败: "+err.Error())
		return
	}
	writeJSON(w, map[string]any{"items": items})
}

func (h *Handler) HandleGet(w http.ResponseWriter, r *http.Request) {
	item, err := Get(r.PathValue("jobId"))
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "JOB_NOT_FOUND", "任务不存在")
		return
	}
	writeJSON(w, item)
}

func processOnePendingJob() {
	item, ok := claimNextPendingJob()
	if !ok || item == nil {
		return
	}

	log.Printf("[JOBS] claimed id=%s type=%s storage=%s resource=%s", item.ID, item.Type, item.StorageID, item.ResourceID)

	switch Type(item.Type) {
	case TypeCloudSync:
		runCloudSyncJob(item)
	default:
		_ = updateFailure(item.ID, "不支持的任务类型")
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
		string(StatusRunning), 5, "正在同步到云盘", now, now, item.ID, string(StatusPending),
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
	item.Message = "正在同步到云盘"
	item.StartedAt = stringPtr(now)
	item.UpdatedAt = now
	return &item, true
}

func runCloudSyncJob(item *Job) {
	startedAt := time.Now()
	var payload CloudSyncPayload
	if err := json.Unmarshal([]byte(item.PayloadJSON), &payload); err != nil {
		log.Printf("[JOBS] decode payload failed id=%s err=%v", item.ID, err)
		_ = updateFailure(item.ID, "解析同步任务失败: "+err.Error())
		return
	}
	log.Printf("[JOBS] cloud sync start id=%s storage=%s local=%s target=%s", item.ID, item.StorageID, payload.LocalPath, payload.TargetPath)

	storageRecord, err := storage.Get(item.StorageID)
	if err != nil || storageRecord == nil {
		log.Printf("[JOBS] cloud sync storage missing id=%s storage=%s err=%v", item.ID, item.StorageID, err)
		_ = updateFailure(item.ID, "云存储不存在")
		return
	}

	file, err := os.Open(payload.LocalPath)
	if err != nil {
		log.Printf("[JOBS] cloud sync open local failed id=%s path=%s err=%v", item.ID, payload.LocalPath, err)
		_ = updateFailure(item.ID, "打开本地缓存文件失败: "+err.Error())
		return
	}
	defer file.Close()

	_ = updateProgress(item.ID, 25, "正在上传到 Google Drive")
	if err := storage.UploadGoogleDriveReader(context.Background(), storageRecord, payload.TargetPath, file, payload.ContentType); err != nil {
		log.Printf("[JOBS] cloud sync upload failed id=%s storage=%s target=%s err=%v", item.ID, item.StorageID, payload.TargetPath, err)
		_ = updateFailure(item.ID, "上传到 Google Drive 失败: "+err.Error())
		return
	}

	_ = os.Remove(payload.LocalPath)
	_ = os.Remove(filepath.Dir(payload.LocalPath))
	log.Printf("[JOBS] cloud sync success id=%s target=%s duration=%s", item.ID, payload.TargetPath, time.Since(startedAt))
	_ = updateSuccess(item.ID, map[string]any{
		"targetPath": payload.TargetPath,
		"fileName":   payload.FileName,
	})
}

func updateProgress(jobID string, progress int, message string) error {
	_, err := database.DB.Exec(`UPDATE jobs SET progress = ?, message = ?, updated_at = ? WHERE id = ?`, progress, message, time.Now().Format(time.RFC3339), jobID)
	return err
}

func updateFailure(jobID string, message string) error {
	now := time.Now().Format(time.RFC3339)
	_, err := database.DB.Exec(
		`UPDATE jobs SET status = ?, progress = ?, message = ?, error_message = ?, finished_at = ?, updated_at = ? WHERE id = ?`,
		string(StatusFailed), 100, "同步失败", message, now, now, jobID,
	)
	return err
}

func updateSuccess(jobID string, result any) error {
	encoded, _ := json.Marshal(result)
	now := time.Now().Format(time.RFC3339)
	_, err := database.DB.Exec(
		`UPDATE jobs SET status = ?, progress = ?, message = ?, result_json = ?, error_message = '', finished_at = ?, updated_at = ? WHERE id = ?`,
		string(StatusSuccess), 100, "同步完成", string(encoded), now, now, jobID,
	)
	return err
}

func stringPtr(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return &value
}
