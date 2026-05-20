package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	upload2TusVersion = "1.0.0"
	upload2Endpoint   = "/api/v1/upload2"
)

type upload2Meta struct {
	ID           string            `json:"id"`
	FileName     string            `json:"fileName"`
	UploadLength int64             `json:"uploadLength"`
	Offset       int64             `json:"offset"`
	Path         string            `json:"path"`
	RawMetadata  map[string]string `json:"rawMetadata"`
	CreatedAt    time.Time         `json:"createdAt"`
	UpdatedAt    time.Time         `json:"updatedAt"`
}

var upload2Locks sync.Map

func handleUpload2Create(w http.ResponseWriter, r *http.Request) {
	if !validateUpload2Tus(w, r) {
		return
	}

	log.Printf("[UPLOAD2][CREATE] start remote=%s length=%q metadata=%q", r.RemoteAddr, r.Header.Get("Upload-Length"), r.Header.Get("Upload-Metadata"))

	length, err := parseUpload2Length(r.Header.Get("Upload-Length"))
	if err != nil {
		log.Printf("[UPLOAD2][CREATE] invalid length remote=%s err=%v", r.RemoteAddr, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	rawMetadata, err := parseUpload2Metadata(r.Header.Get("Upload-Metadata"))
	if err != nil {
		log.Printf("[UPLOAD2][CREATE] invalid metadata remote=%s err=%v", r.RemoteAddr, err)
		http.Error(w, "invalid Upload-Metadata", http.StatusBadRequest)
		return
	}

	id, err := newUpload2ID()
	if err != nil {
		log.Printf("[UPLOAD2][CREATE] id failed remote=%s err=%v", r.RemoteAddr, err)
		http.Error(w, "failed to create upload id", http.StatusInternalServerError)
		return
	}

	if err := os.MkdirAll(upload2Root(), 0o755); err != nil {
		log.Printf("[UPLOAD2][CREATE] mkdir failed root=%s err=%v", upload2Root(), err)
		http.Error(w, "failed to create upload directory", http.StatusInternalServerError)
		return
	}

	fileName := safeUpload2FileName(firstNonEmpty(rawMetadata["filename"], rawMetadata["name"], "upload.bin"))
	path := filepath.Join(upload2Root(), id+"-"+fileName)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		log.Printf("[UPLOAD2][CREATE] file create failed id=%s path=%s err=%v", id, path, err)
		http.Error(w, "failed to create upload file", http.StatusInternalServerError)
		return
	}
	file.Close()

	meta := &upload2Meta{
		ID:           id,
		FileName:     fileName,
		UploadLength: length,
		Offset:       0,
		Path:         path,
		RawMetadata:  rawMetadata,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	if err := saveUpload2Meta(meta); err != nil {
		_ = os.Remove(path)
		log.Printf("[UPLOAD2][CREATE] save meta failed id=%s path=%s err=%v", id, path, err)
		http.Error(w, "failed to save upload metadata", http.StatusInternalServerError)
		return
	}

	log.Printf("[UPLOAD2][CREATE] created id=%s fileName=%q size=%d path=%s", id, fileName, length, path)

	setUpload2TusHeaders(w)
	w.Header().Set("Location", upload2Location(r, id))
	w.Header().Set("Upload-Offset", "0")
	w.WriteHeader(http.StatusCreated)
}

func handleUpload2Head(w http.ResponseWriter, r *http.Request) {
	if !validateUpload2Tus(w, r) {
		return
	}

	meta, err := loadUpload2Meta(strings.TrimSpace(r.PathValue("uploadId")))
	if err != nil {
		log.Printf("[UPLOAD2][HEAD] not found id=%s remote=%s err=%v", strings.TrimSpace(r.PathValue("uploadId")), r.RemoteAddr, err)
		http.Error(w, "upload not found", http.StatusNotFound)
		return
	}

	log.Printf("[UPLOAD2][HEAD] id=%s offset=%d length=%d remote=%s", meta.ID, meta.Offset, meta.UploadLength, r.RemoteAddr)

	setUpload2TusHeaders(w)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Upload-Length", strconv.FormatInt(meta.UploadLength, 10))
	w.Header().Set("Upload-Offset", strconv.FormatInt(meta.Offset, 10))
	w.WriteHeader(http.StatusNoContent)
}

func handleUpload2Patch(w http.ResponseWriter, r *http.Request) {
	if !validateUpload2Tus(w, r) {
		return
	}
	if strings.TrimSpace(r.Header.Get("Content-Type")) != "application/offset+octet-stream" {
		log.Printf("[UPLOAD2][PATCH] invalid content-type id=%s remote=%s contentType=%q", strings.TrimSpace(r.PathValue("uploadId")), r.RemoteAddr, r.Header.Get("Content-Type"))
		http.Error(w, "invalid Content-Type", http.StatusUnsupportedMediaType)
		return
	}

	uploadID := strings.TrimSpace(r.PathValue("uploadId"))
	lock := getUpload2Lock(uploadID)
	lock.Lock()
	defer lock.Unlock()

	meta, err := loadUpload2Meta(uploadID)
	if err != nil {
		log.Printf("[UPLOAD2][PATCH] not found id=%s remote=%s err=%v", uploadID, r.RemoteAddr, err)
		http.Error(w, "upload not found", http.StatusNotFound)
		return
	}

	offset, err := strconv.ParseInt(strings.TrimSpace(r.Header.Get("Upload-Offset")), 10, 64)
	if err != nil || offset < 0 {
		log.Printf("[UPLOAD2][PATCH] invalid offset id=%s remote=%s raw=%q err=%v", uploadID, r.RemoteAddr, r.Header.Get("Upload-Offset"), err)
		http.Error(w, "invalid Upload-Offset", http.StatusBadRequest)
		return
	}
	if offset != meta.Offset {
		log.Printf("[UPLOAD2][PATCH] offset conflict id=%s remote=%s want=%d got=%d", uploadID, r.RemoteAddr, meta.Offset, offset)
		setUpload2TusHeaders(w)
		w.Header().Set("Upload-Offset", strconv.FormatInt(meta.Offset, 10))
		http.Error(w, "mismatched Upload-Offset", http.StatusConflict)
		return
	}

	file, err := os.OpenFile(meta.Path, os.O_WRONLY, 0o644)
	if err != nil {
		log.Printf("[UPLOAD2][PATCH] open failed id=%s path=%s err=%v", uploadID, meta.Path, err)
		http.Error(w, "failed to open upload file", http.StatusInternalServerError)
		return
	}
	defer file.Close()

	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		log.Printf("[UPLOAD2][PATCH] seek failed id=%s offset=%d err=%v", uploadID, offset, err)
		http.Error(w, "failed to seek upload file", http.StatusInternalServerError)
		return
	}

	startedAt := time.Now()
	log.Printf("[UPLOAD2][PATCH] write start id=%s remote=%s offset=%d contentLength=%d path=%s", uploadID, r.RemoteAddr, offset, r.ContentLength, meta.Path)
	written, err := io.Copy(file, r.Body)
	if err != nil {
		log.Printf("[UPLOAD2][PATCH] write failed id=%s offset=%d wrote=%d duration=%s err=%v", uploadID, offset, written, time.Since(startedAt), err)
		http.Error(w, "failed to write upload chunk", http.StatusInternalServerError)
		return
	}
	if meta.Offset+written > meta.UploadLength {
		log.Printf("[UPLOAD2][PATCH] overflow id=%s offset=%d wrote=%d length=%d", uploadID, meta.Offset, written, meta.UploadLength)
		http.Error(w, "upload exceeds declared length", http.StatusBadRequest)
		return
	}

	meta.Offset += written
	meta.UpdatedAt = time.Now()
	if err := saveUpload2Meta(meta); err != nil {
		log.Printf("[UPLOAD2][PATCH] save meta failed id=%s offset=%d err=%v", uploadID, meta.Offset, err)
		http.Error(w, "failed to save upload metadata", http.StatusInternalServerError)
		return
	}

	log.Printf("[UPLOAD2][PATCH] wrote id=%s chunk=%d newOffset=%d/%d duration=%s", uploadID, written, meta.Offset, meta.UploadLength, time.Since(startedAt))
	if meta.Offset == meta.UploadLength {
		log.Printf("[UPLOAD2][DONE] id=%s fileName=%q size=%d path=%s", meta.ID, meta.FileName, meta.UploadLength, meta.Path)
	}

	setUpload2TusHeaders(w)
	w.Header().Set("Upload-Offset", strconv.FormatInt(meta.Offset, 10))
	w.WriteHeader(http.StatusNoContent)
}

func handleUpload2Delete(w http.ResponseWriter, r *http.Request) {
	if !validateUpload2Tus(w, r) {
		return
	}

	meta, err := loadUpload2Meta(strings.TrimSpace(r.PathValue("uploadId")))
	if err == nil {
		_ = os.Remove(meta.Path)
		_ = os.Remove(upload2MetaPath(meta.ID))
		log.Printf("[UPLOAD2][DELETE] deleted id=%s path=%s remote=%s", meta.ID, meta.Path, r.RemoteAddr)
	} else {
		log.Printf("[UPLOAD2][DELETE] not found id=%s remote=%s err=%v", strings.TrimSpace(r.PathValue("uploadId")), r.RemoteAddr, err)
	}

	setUpload2TusHeaders(w)
	w.WriteHeader(http.StatusNoContent)
}

func validateUpload2Tus(w http.ResponseWriter, r *http.Request) bool {
	if strings.TrimSpace(r.Header.Get("Tus-Resumable")) != upload2TusVersion {
		log.Printf("[UPLOAD2] invalid Tus-Resumable method=%s path=%s remote=%s header=%q", r.Method, r.URL.Path, r.RemoteAddr, r.Header.Get("Tus-Resumable"))
		setUpload2TusHeaders(w)
		http.Error(w, "missing or invalid Tus-Resumable", http.StatusPreconditionFailed)
		return false
	}
	return true
}

func setUpload2TusHeaders(w http.ResponseWriter) {
	w.Header().Set("Tus-Resumable", upload2TusVersion)
	w.Header().Set("Tus-Version", upload2TusVersion)
	w.Header().Set("Tus-Extension", "creation,termination")
	w.Header().Set("Tus-Max-Size", "107374182400")
}

func parseUpload2Length(raw string) (int64, error) {
	length, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || length < 0 {
		return 0, errors.New("invalid Upload-Length")
	}
	return length, nil
}

func parseUpload2Metadata(raw string) (map[string]string, error) {
	result := make(map[string]string)
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		parts := strings.SplitN(item, " ", 2)
		key := strings.TrimSpace(parts[0])
		if key == "" {
			continue
		}
		if len(parts) == 1 {
			result[key] = ""
			continue
		}
		value, err := base64.StdEncoding.DecodeString(strings.TrimSpace(parts[1]))
		if err != nil {
			return nil, err
		}
		result[key] = string(value)
	}
	return result, nil
}

func newUpload2ID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func upload2Root() string {
	return filepath.Join(os.TempDir(), "yesnas-upload2")
}

func upload2MetaPath(id string) string {
	return filepath.Join(upload2Root(), id+".json")
}

func saveUpload2Meta(meta *upload2Meta) error {
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(upload2MetaPath(meta.ID), data, 0o644)
}

func loadUpload2Meta(id string) (*upload2Meta, error) {
	if id == "" || strings.Contains(id, "/") || strings.Contains(id, string(filepath.Separator)) {
		return nil, os.ErrNotExist
	}
	data, err := os.ReadFile(upload2MetaPath(id))
	if err != nil {
		return nil, err
	}
	var meta upload2Meta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, err
	}
	return &meta, nil
}

func getUpload2Lock(id string) *sync.Mutex {
	value, _ := upload2Locks.LoadOrStore(id, &sync.Mutex{})
	return value.(*sync.Mutex)
}

func upload2Location(r *http.Request, id string) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host + upload2Endpoint + "/" + id
}

func safeUpload2FileName(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	if name == "." || name == string(filepath.Separator) || name == "" {
		return "upload.bin"
	}
	return name
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
