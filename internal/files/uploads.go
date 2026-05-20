package files

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"nas-server/internal/jobs"
	"nas-server/internal/storage"
	"nas-server/internal/storagepool"
	"nas-server/pkg/idgen"
	commandrunner "nas-server/pkg/shell"
)

const (
	tusVersion             = "1.0.0"
	tusEndpointBase        = "/api/v1/uploads/tus"
	defaultCloudUploadRoot = "/srv/yesnas/cache/uploads"
)

type tusUploadMeta struct {
	ID           string            `json:"id"`
	StorageID    string            `json:"storageId"`
	ParentID     string            `json:"parentId"`
	RelativePath string            `json:"relativePath"`
	FileName     string            `json:"fileName"`
	FileType     string            `json:"fileType"`
	TempDir      string            `json:"tempDir"`
	UploadLength int64             `json:"uploadLength"`
	Offset       int64             `json:"offset"`
	CreatedAt    time.Time         `json:"createdAt"`
	UpdatedAt    time.Time         `json:"updatedAt"`
	RawMetadata  map[string]string `json:"rawMetadata"`
}

type uploadPatchLock struct {
	ch chan struct{}
}

var uploadPatchLocks sync.Map

func getUploadPatchLock(uploadID string) *uploadPatchLock {
	value, _ := uploadPatchLocks.LoadOrStore(uploadID, &uploadPatchLock{ch: make(chan struct{}, 1)})
	return value.(*uploadPatchLock)
}

func (l *uploadPatchLock) tryLock() bool {
	select {
	case l.ch <- struct{}{}:
		return true
	default:
		return false
	}
}

func (l *uploadPatchLock) unlock() {
	select {
	case <-l.ch:
	default:
	}
}

func (h *Handler) HandleTusCreate(w http.ResponseWriter, r *http.Request) {
	if !validateTusRequest(w, r) {
		return
	}

	log.Printf("[UPLOAD][CREATE] start method=%s path=%s remote=%s length=%q metadata=%q", r.Method, r.URL.Path, r.RemoteAddr, r.Header.Get("Upload-Length"), r.Header.Get("Upload-Metadata"))

	length, err := parseUploadLength(r.Header.Get("Upload-Length"))
	if err != nil {
		log.Printf("[UPLOAD][CREATE] invalid length remote=%s err=%v", r.RemoteAddr, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	rawMeta, err := parseTusMetadata(r.Header.Get("Upload-Metadata"))
	if err != nil {
		log.Printf("[UPLOAD][CREATE] invalid metadata remote=%s err=%v", r.RemoteAddr, err)
		http.Error(w, "invalid Upload-Metadata", http.StatusBadRequest)
		return
	}

	meta, err := buildTusUploadMeta(length, rawMeta)
	if err != nil {
		log.Printf("[UPLOAD][CREATE] build meta failed remote=%s err=%v", r.RemoteAddr, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	storageRecord, err := resolveUploadStorage(meta.StorageID)
	if err != nil {
		log.Printf("[UPLOAD][CREATE] storage resolve failed upload=%s storage=%s err=%v", meta.ID, meta.StorageID, err)
		http.Error(w, "storage not found", http.StatusNotFound)
		return
	}
	meta.StorageID = storageRecord.ID
	meta.TempDir = storageUploadRootPath(storageRecord)
	if err := ensureUploadMetaRoot(); err != nil {
		log.Printf("[UPLOAD] init meta dir failed root=%s err=%v", uploadMetaRootPath(), err)
		http.Error(w, "failed to initialize upload metadata directory", http.StatusInternalServerError)
		return
	}
	if err := ensureStorageUploadRoot(r.Context(), meta.TempDir); err != nil {
		log.Printf("[UPLOAD] init temp dir failed root=%s err=%v", meta.TempDir, err)
		http.Error(w, "failed to initialize upload temp directory", http.StatusInternalServerError)
		return
	}

	filePath := tusTempFilePath(meta)
	file, err := openTusTempFile(r.Context(), filePath)
	if err != nil {
		log.Printf("[UPLOAD] create temp file failed path=%s err=%v", filePath, err)
		http.Error(w, "failed to create upload", http.StatusInternalServerError)
		return
	}
	defer file.Close()
	if err := saveTusUploadMeta(meta); err != nil {
		http.Error(w, "failed to persist upload metadata", http.StatusInternalServerError)
		return
	}
	log.Printf(
		"[UPLOAD] created id=%s storage=%s parentId=%s relativePath=%q fileName=%q size=%d temp=%s",
		meta.ID,
		meta.StorageID,
		meta.ParentID,
		meta.RelativePath,
		meta.FileName,
		meta.UploadLength,
		filePath,
	)

	setTusCommonHeaders(w)
	w.Header().Set("Location", tusUploadURL(r, meta.ID))
	w.Header().Set("Upload-Offset", "0")
	w.WriteHeader(http.StatusCreated)
}

func (h *Handler) HandleTusHead(w http.ResponseWriter, r *http.Request) {
	if !validateTusRequest(w, r) {
		return
	}

	meta, err := loadTusUploadMeta(strings.TrimSpace(r.PathValue("uploadId")))
	if err != nil {
		log.Printf("[UPLOAD][HEAD] load meta failed upload=%s remote=%s err=%v", strings.TrimSpace(r.PathValue("uploadId")), r.RemoteAddr, err)
		http.Error(w, "upload not found", http.StatusNotFound)
		return
	}

	log.Printf("[UPLOAD][HEAD] upload=%s offset=%d length=%d remote=%s", meta.ID, meta.Offset, meta.UploadLength, r.RemoteAddr)

	setTusCommonHeaders(w)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Upload-Length", strconv.FormatInt(meta.UploadLength, 10))
	w.Header().Set("Upload-Offset", strconv.FormatInt(meta.Offset, 10))
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) HandleTusPatch(w http.ResponseWriter, r *http.Request) {
	if !validateTusRequest(w, r) {
		return
	}
	if contentType := strings.TrimSpace(r.Header.Get("Content-Type")); contentType != "application/offset+octet-stream" {
		log.Printf("[UPLOAD][PATCH] invalid content-type upload=%s remote=%s contentType=%q", strings.TrimSpace(r.PathValue("uploadId")), r.RemoteAddr, contentType)
		http.Error(w, "invalid Content-Type", http.StatusUnsupportedMediaType)
		return
	}

	meta, err := loadTusUploadMeta(strings.TrimSpace(r.PathValue("uploadId")))
	if err != nil {
		log.Printf("[UPLOAD][PATCH] load meta failed upload=%s remote=%s err=%v", strings.TrimSpace(r.PathValue("uploadId")), r.RemoteAddr, err)
		http.Error(w, "upload not found", http.StatusNotFound)
		return
	}
	patchLock := getUploadPatchLock(meta.ID)
	if !patchLock.tryLock() {
		log.Printf("[UPLOAD][PATCH] busy upload=%s remote=%s offset=%d", meta.ID, r.RemoteAddr, meta.Offset)
		setTusCommonHeaders(w)
		w.Header().Set("Upload-Offset", strconv.FormatInt(meta.Offset, 10))
		http.Error(w, "upload patch already in progress", http.StatusConflict)
		return
	}
	defer patchLock.unlock()

	offset, err := strconv.ParseInt(strings.TrimSpace(r.Header.Get("Upload-Offset")), 10, 64)
	if err != nil || offset < 0 {
		log.Printf("[UPLOAD][PATCH] invalid offset upload=%s remote=%s raw=%q err=%v", meta.ID, r.RemoteAddr, r.Header.Get("Upload-Offset"), err)
		http.Error(w, "invalid Upload-Offset", http.StatusBadRequest)
		return
	}
	if offset != meta.Offset {
		log.Printf("[UPLOAD][PATCH] mismatched offset upload=%s remote=%s want=%d got=%d", meta.ID, r.RemoteAddr, meta.Offset, offset)
		setTusCommonHeaders(w)
		w.Header().Set("Upload-Offset", strconv.FormatInt(meta.Offset, 10))
		http.Error(w, "mismatched Upload-Offset", http.StatusConflict)
		return
	}

	log.Printf("[UPLOAD][PATCH] start upload=%s remote=%s offset=%d length=%d temp=%s", meta.ID, r.RemoteAddr, meta.Offset, meta.UploadLength, tusTempFilePath(meta))

	tempFile, err := openTusTempFileForPatch(r.Context(), meta)
	if err != nil {
		log.Printf("[UPLOAD] open temp file failed id=%s path=%s err=%v", meta.ID, tusTempFilePath(meta), err)
		http.Error(w, "failed to open upload temp file", http.StatusInternalServerError)
		return
	}
	defer tempFile.Close()

	if _, err := tempFile.Seek(offset, io.SeekStart); err != nil {
		http.Error(w, "failed to seek upload temp file", http.StatusInternalServerError)
		return
	}

	copyStartedAt := time.Now()
	log.Printf("[UPLOAD][PATCH] copy-start upload=%s remote=%s offset=%d contentLength=%d", meta.ID, r.RemoteAddr, offset, r.ContentLength)
	written, err := io.Copy(tempFile, r.Body)
	copyDuration := time.Since(copyStartedAt)
	if err != nil {
		log.Printf("[UPLOAD][PATCH] write failed upload=%s remote=%s offset=%d wrote=%d duration=%s err=%v", meta.ID, r.RemoteAddr, offset, written, copyDuration, err)
		http.Error(w, "failed to write upload chunk", http.StatusInternalServerError)
		return
	}
	meta.Offset += written
	meta.UpdatedAt = time.Now()
	log.Printf("[UPLOAD][PATCH] wrote upload=%s remote=%s chunk=%d duration=%s newOffset=%d/%d", meta.ID, r.RemoteAddr, written, copyDuration, meta.Offset, meta.UploadLength)

	if meta.Offset > meta.UploadLength {
		log.Printf("[UPLOAD][PATCH] overflow upload=%s remote=%s offset=%d length=%d", meta.ID, r.RemoteAddr, meta.Offset, meta.UploadLength)
		http.Error(w, "upload exceeds declared length", http.StatusBadRequest)
		return
	}

	log.Printf("[UPLOAD][PATCH] saving-meta upload=%s remote=%s offset=%d", meta.ID, r.RemoteAddr, meta.Offset)
	if err := saveTusUploadMeta(meta); err != nil {
		log.Printf("[UPLOAD][PATCH] save meta failed upload=%s remote=%s offset=%d err=%v", meta.ID, r.RemoteAddr, meta.Offset, err)
		http.Error(w, "failed to persist upload offset", http.StatusInternalServerError)
		return
	}
	log.Printf("[UPLOAD][PATCH] saved-meta upload=%s remote=%s offset=%d", meta.ID, r.RemoteAddr, meta.Offset)

	if meta.Offset == meta.UploadLength {
		log.Printf("[UPLOAD] chunks complete id=%s size=%d temp=%s", meta.ID, meta.UploadLength, tusTempFilePath(meta))
		if err := finalizeTusUpload(r.Context(), meta); err != nil {
			if cleanupErr := cleanupTusUpload(meta); cleanupErr != nil {
				log.Printf("[UPLOAD] cleanup after finalize failure failed id=%s err=%v", meta.ID, cleanupErr)
			}
			log.Printf("[UPLOAD] finalize failed id=%s err=%v", meta.ID, err)
			http.Error(w, "finalize upload failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if err := cleanupTusUploadMetaOnly(meta); err != nil {
			log.Printf("[UPLOAD] cleanup failed id=%s err=%v", meta.ID, err)
			http.Error(w, "cleanup upload failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		log.Printf("[UPLOAD] completed id=%s", meta.ID)
		uploadPatchLocks.Delete(meta.ID)
	}

	setTusCommonHeaders(w)
	w.Header().Set("Upload-Offset", strconv.FormatInt(meta.Offset, 10))
	log.Printf("[UPLOAD][PATCH] responding upload=%s remote=%s offset=%d status=%d", meta.ID, r.RemoteAddr, meta.Offset, http.StatusNoContent)
	w.WriteHeader(http.StatusNoContent)
	log.Printf("[UPLOAD][PATCH] responded upload=%s remote=%s offset=%d status=%d", meta.ID, r.RemoteAddr, meta.Offset, http.StatusNoContent)
}

func (h *Handler) HandleTusDelete(w http.ResponseWriter, r *http.Request) {
	if !validateTusRequest(w, r) {
		return
	}
	meta, err := loadTusUploadMeta(strings.TrimSpace(r.PathValue("uploadId")))
	if err != nil {
		log.Printf("[UPLOAD][DELETE] load meta failed upload=%s remote=%s err=%v", strings.TrimSpace(r.PathValue("uploadId")), r.RemoteAddr, err)
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "upload not found", http.StatusNotFound)
			return
		}
		http.Error(w, "failed to load upload", http.StatusInternalServerError)
		return
	}
	if err := cleanupTusUpload(meta); err != nil {
		log.Printf("[UPLOAD][DELETE] cleanup failed upload=%s remote=%s err=%v", meta.ID, r.RemoteAddr, err)
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "upload not found", http.StatusNotFound)
			return
		}
		http.Error(w, "failed to delete upload", http.StatusInternalServerError)
		return
	}
	log.Printf("[UPLOAD][DELETE] completed upload=%s remote=%s", meta.ID, r.RemoteAddr)
	uploadPatchLocks.Delete(meta.ID)
	setTusCommonHeaders(w)
	w.WriteHeader(http.StatusNoContent)
}

func validateTusRequest(w http.ResponseWriter, r *http.Request) bool {
	if strings.TrimSpace(r.Header.Get("Tus-Resumable")) != tusVersion {
		setTusCommonHeaders(w)
		http.Error(w, "missing or invalid Tus-Resumable", http.StatusPreconditionFailed)
		return false
	}
	return true
}

func setTusCommonHeaders(w http.ResponseWriter) {
	w.Header().Set("Tus-Resumable", tusVersion)
	w.Header().Set("Tus-Version", tusVersion)
	w.Header().Set("Tus-Extension", "creation,termination")
	w.Header().Set("Tus-Max-Size", strconv.FormatInt(100*1024*1024*1024, 10))
	w.Header().Set("Cache-Control", "no-store")
}

func tusUploadURL(r *http.Request, uploadID string) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if forwardedProto := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); forwardedProto != "" {
		scheme = forwardedProto
	}
	host := r.Host
	if forwardedHost := strings.TrimSpace(r.Header.Get("X-Forwarded-Host")); forwardedHost != "" {
		host = forwardedHost
	}
	return scheme + "://" + host + tusEndpointBase + "/" + uploadID
}

func ensureUploadMetaRoot() error {
	return os.MkdirAll(uploadMetaRootPath(), 0o755)
}

func parseUploadLength(raw string) (int64, error) {
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("invalid Upload-Length")
	}
	return value, nil
}

func parseTusMetadata(header string) (map[string]string, error) {
	result := make(map[string]string)
	header = strings.TrimSpace(header)
	if header == "" {
		return result, nil
	}
	for _, pair := range strings.Split(header, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		parts := strings.SplitN(pair, " ", 2)
		key := strings.TrimSpace(parts[0])
		if key == "" {
			return nil, fmt.Errorf("invalid metadata key")
		}
		if len(parts) == 1 {
			result[key] = ""
			continue
		}
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(parts[1]))
		if err != nil {
			return nil, err
		}
		result[key] = string(decoded)
	}
	return result, nil
}

func buildTusUploadMeta(length int64, raw map[string]string) (*tusUploadMeta, error) {
	fileName := strings.TrimSpace(firstNonEmpty(raw["filename"], raw["name"]))
	if fileName == "" {
		return nil, fmt.Errorf("file name is required")
	}
	if fileName != filepath.Base(fileName) {
		return nil, fmt.Errorf("invalid file name")
	}
	storageID := strings.TrimSpace(raw["storageId"])
	if storageID == "" {
		return nil, fmt.Errorf("storageId is required")
	}

	now := time.Now()
	return &tusUploadMeta{
		ID:           idgen.New(),
		StorageID:    storageID,
		ParentID:     strings.TrimSpace(raw["parentId"]),
		RelativePath: strings.TrimSpace(raw["relativePath"]),
		FileName:     fileName,
		FileType:     strings.TrimSpace(firstNonEmpty(raw["filetype"], raw["type"])),
		UploadLength: length,
		Offset:       0,
		CreatedAt:    now,
		UpdatedAt:    now,
		RawMetadata:  raw,
	}, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func saveTusUploadMeta(meta *tusUploadMeta) error {
	data, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	return os.WriteFile(tusMetaFilePath(meta.ID), data, 0o644)
}

func loadTusUploadMeta(uploadID string) (*tusUploadMeta, error) {
	if uploadID == "" {
		return nil, os.ErrNotExist
	}
	data, err := os.ReadFile(tusMetaFilePath(uploadID))
	if err != nil {
		return nil, err
	}
	var meta tusUploadMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, err
	}
	return &meta, nil
}

func cleanupTusUpload(meta *tusUploadMeta) error {
	var firstErr error
	for _, path := range []string{tusTempFilePath(meta), tusMetaFilePath(meta.ID)} {
		if err := removeFileWithFallback(context.Background(), path); err != nil && !errors.Is(err, os.ErrNotExist) && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func cleanupTusUploadMetaOnly(meta *tusUploadMeta) error {
	return removeFileWithFallback(context.Background(), tusMetaFilePath(meta.ID))
}

func tusTempFilePath(meta *tusUploadMeta) string {
	return filepath.Join(meta.TempDir, meta.ID+".bin")
}

func tusMetaFilePath(uploadID string) string {
	return filepath.Join(uploadMetaRootPath(), uploadID+".json")
}

func uploadMetaRootPath() string {
	if custom := strings.TrimSpace(os.Getenv("YESNAS_UPLOAD_METADIR")); custom != "" {
		return custom
	}
	return filepath.Join(os.TempDir(), "yesnas-upload-meta")
}

func storageUploadRootPath(record *storage.Storage) string {
	if record == nil {
		return filepath.Join(os.TempDir(), "yesnas-uploads")
	}
	if record.Type == storage.Cloud {
		return filepath.Join(cloudUploadRootPath(), record.ID)
	}
	return filepath.Join(record.MountPath, ".uploads")
}

func cloudUploadRootPath() string {
	if custom := strings.TrimSpace(os.Getenv("YESNAS_CLOUD_UPLOAD_TMPDIR")); custom != "" {
		return custom
	}
	return defaultCloudUploadRoot
}

func resolveUploadStorage(id string) (*storage.Storage, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("storageId is required")
	}

	record, err := storage.Get(id)
	if err == nil && record != nil {
		return record, nil
	}

	pool, poolErr := storagepool.Get(id)
	if poolErr == nil && pool != nil && strings.TrimSpace(pool.StorageID) != "" {
		return storage.Get(strings.TrimSpace(pool.StorageID))
	}

	if err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("storage not found")
}

func finalizeTusUpload(ctx context.Context, meta *tusUploadMeta) error {
	storageRecord, err := resolveUploadStorage(meta.StorageID)
	if err != nil {
		return fmt.Errorf("storage not found: %w", err)
	}
	meta.StorageID = storageRecord.ID

	parentPath := filepath.Clean(storageRecord.MountPath)
	if meta.ParentID != "" {
		decodedParent, err := decodeFileID(meta.ParentID)
		if err != nil {
			return fmt.Errorf("invalid parentId")
		}
		parentPath = decodedParent
	}
	if !isPathWithinRoot(storageRecord.MountPath, parentPath) {
		return fmt.Errorf("parent path out of storage root")
	}

	targetPath, err := buildTusTargetPath(storageRecord.MountPath, parentPath, meta)
	if err != nil {
		return err
	}
	targetDir := filepath.Dir(targetPath)
	log.Printf(
		"[UPLOAD] finalize id=%s storage=%s root=%s parent=%s relativePath=%q target=%s",
		meta.ID,
		meta.StorageID,
		storageRecord.MountPath,
		parentPath,
		meta.RelativePath,
		targetPath,
	)

	tempPath := tusTempFilePath(meta)
	if storageRecord.Type == storage.Cloud {
		jobItem, err := jobs.EnqueueCloudSync(storageRecord.ID, jobs.CloudSyncPayload{
			LocalPath:   tempPath,
			TargetPath:  targetPath,
			ContentType: meta.FileType,
			FileName:    meta.FileName,
		})
		if err != nil {
			log.Printf("[UPLOAD][FINALIZE] cloud enqueue failed id=%s storage=%s source=%s target=%s err=%v", meta.ID, storageRecord.ID, tempPath, targetPath, err)
			return err
		}
		log.Printf("[UPLOAD] queued cloud sync id=%s job=%s source=%s target=%s", meta.ID, jobItem.ID, tempPath, targetPath)
		return nil
	}

	if err := createFolderAll(ctx, targetDir); err != nil {
		return fmt.Errorf("create target directory: %w", err)
	}
	if _, err := os.Stat(targetPath); err == nil {
		return fmt.Errorf("target file already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	if err := persistUploadedFile(ctx, tempPath, targetPath); err != nil {
		return err
	}
	log.Printf("[UPLOAD] persisted id=%s source=%s target=%s", meta.ID, tempPath, targetPath)
	return nil
}

func buildTusTargetPath(root, parentPath string, meta *tusUploadMeta) (string, error) {
	if rel := strings.TrimSpace(meta.RelativePath); rel != "" {
		cleanRel := filepath.Clean(rel)
		if cleanRel == "." || cleanRel == "" || filepath.IsAbs(cleanRel) {
			return "", fmt.Errorf("invalid relativePath")
		}
		targetPath := filepath.Join(parentPath, cleanRel)
		if !isPathWithinRoot(root, targetPath) {
			return "", fmt.Errorf("relativePath escapes storage root")
		}
		return targetPath, nil
	}

	targetPath := filepath.Join(parentPath, meta.FileName)
	if !isPathWithinRoot(root, targetPath) {
		return "", fmt.Errorf("target path invalid")
	}
	return targetPath, nil
}

func createFolderAll(ctx context.Context, targetPath string) error {
	if err := os.MkdirAll(targetPath, 0o755); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrPermission) {
		return err
	}

	_, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "mkdir", "-p", targetPath)
	return err
}

func ensureStorageUploadRoot(ctx context.Context, targetPath string) error {
	if err := os.MkdirAll(targetPath, 0o777); err == nil {
		_ = os.Chmod(targetPath, 0o777)
		return nil
	} else if !errors.Is(err, os.ErrPermission) {
		return err
	}

	if _, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "mkdir", "-p", targetPath); err != nil {
		return err
	}
	if _, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "chmod", "0777", targetPath); err != nil {
		return err
	}
	return nil
}

func openTusTempFile(ctx context.Context, filePath string) (*os.File, error) {
	file, err := os.OpenFile(filePath, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o666)
	if err == nil {
		return file, nil
	}
	if !errors.Is(err, os.ErrPermission) {
		return nil, err
	}

	if _, touchErr := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "touch", filePath); touchErr != nil {
		return nil, fmt.Errorf("sudo touch temp file: %w", touchErr)
	}
	if _, chmodErr := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "chmod", "0666", filePath); chmodErr != nil {
		return nil, fmt.Errorf("sudo chmod temp file: %w", chmodErr)
	}
	return os.OpenFile(filePath, os.O_RDWR|os.O_TRUNC, 0o666)
}

func openTusTempFileForPatch(ctx context.Context, meta *tusUploadMeta) (*os.File, error) {
	filePath := tusTempFilePath(meta)
	if err := ensureStorageUploadRoot(ctx, meta.TempDir); err != nil {
		log.Printf("[UPLOAD][PATCH] ensure temp dir failed upload=%s dir=%s err=%v", meta.ID, meta.TempDir, err)
		return nil, fmt.Errorf("ensure upload temp dir: %w", err)
	}

	file, err := os.OpenFile(filePath, os.O_CREATE|os.O_RDWR, 0o666)
	if err == nil {
		return file, nil
	}
	if !errors.Is(err, os.ErrPermission) && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	if _, touchErr := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "touch", filePath); touchErr != nil {
		log.Printf("[UPLOAD][PATCH] sudo touch failed upload=%s path=%s err=%v", meta.ID, filePath, touchErr)
		return nil, fmt.Errorf("sudo touch temp file for patch: %w", touchErr)
	}
	if _, chmodErr := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "chmod", "0666", filePath); chmodErr != nil {
		log.Printf("[UPLOAD][PATCH] sudo chmod failed upload=%s path=%s err=%v", meta.ID, filePath, chmodErr)
		return nil, fmt.Errorf("sudo chmod temp file for patch: %w", chmodErr)
	}
	file, err = os.OpenFile(filePath, os.O_CREATE|os.O_RDWR, 0o666)
	if err != nil {
		return nil, err
	}
	return file, nil
}

func persistUploadedFile(ctx context.Context, sourcePath, targetPath string) error {
	if err := os.Rename(sourcePath, targetPath); err == nil {
		log.Printf("[UPLOAD] rename success source=%s target=%s", sourcePath, targetPath)
		return nil
	} else if isCrossDeviceRename(err) {
		log.Printf("[UPLOAD] rename fallback-cross-device source=%s target=%s err=%v", sourcePath, targetPath, err)
	} else if errors.Is(err, os.ErrPermission) {
		log.Printf("[UPLOAD] rename fallback-permission source=%s target=%s err=%v", sourcePath, targetPath, err)
		if _, moveErr := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "mv", sourcePath, targetPath); moveErr == nil {
			log.Printf("[UPLOAD] sudo move success source=%s target=%s", sourcePath, targetPath)
			return nil
		} else {
			log.Printf("[UPLOAD] sudo move failed source=%s target=%s err=%v", sourcePath, targetPath, moveErr)
		}
	} else {
		log.Printf("[UPLOAD] rename failed source=%s target=%s err=%v", sourcePath, targetPath, err)
		return err
	}

	if _, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "cp", sourcePath, targetPath); err != nil {
		return fmt.Errorf("copy uploaded file with sudo: %w", err)
	}
	log.Printf("[UPLOAD] sudo copy success source=%s target=%s", sourcePath, targetPath)
	if err := removeFileWithFallback(ctx, sourcePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func removeFileWithFallback(ctx context.Context, path string) error {
	if err := os.Remove(path); err == nil || errors.Is(err, os.ErrNotExist) {
		return err
	} else if !errors.Is(err, os.ErrPermission) {
		return err
	}

	if _, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "rm", "-f", path); err != nil {
		return err
	}
	return nil
}

func isCrossDeviceRename(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "cross-device link") || strings.Contains(message, "invalid cross-device link")
}
