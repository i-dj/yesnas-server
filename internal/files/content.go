package files

import (
	"log"
	"net/http"
	"path/filepath"

	"nas-server/pkg/thumbnail"
)

func (h *Handler) HandleContent(w http.ResponseWriter, r *http.Request) {
	fileID := r.PathValue("fileId")
	path, err := decodeFileID(fileID)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_FILE_ID", "invalid file id")
		return
	}
	http.ServeFile(w, r, path)
}

func (h *Handler) HandleThumbnail(w http.ResponseWriter, r *http.Request) {
	fileID := r.PathValue("fileId")
	path, err := decodeFileID(fileID)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_FILE_ID", "Invalid ID")
		return
	}

	cachePath, err := thumbnail.GetOrCreateThumbnail(path, "")
	if err != nil {
		log.Printf("[THUMBNAIL-SKIP] %s: %v", filepath.Base(path), err)
		w.WriteHeader(http.StatusNotFound)
		return
	}

	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeFile(w, r, cachePath)
}
