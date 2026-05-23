package files

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"nas-server/internal/storage"
	commandrunner "nas-server/pkg/shell"
)

func (h *Handler) HandleCreateFolder(w http.ResponseWriter, r *http.Request) {
	var req CreateFolderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}

	storageID := r.PathValue("storage")
	if storageID == "" {
		storageID = req.StorageID
	}
	if storageID == "" {
		writeAPIError(w, http.StatusBadRequest, "STORAGE_ID_REQUIRED", "storageId is required")
		return
	}

	storageRecord, err := storage.Get(storageID)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "STORAGE_NOT_FOUND", "Storage not found: "+storageID)
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeAPIError(w, http.StatusBadRequest, "NAME_REQUIRED", "name is required")
		return
	}
	if name == "." || name == ".." || name != filepath.Base(name) {
		writeAPIError(w, http.StatusBadRequest, "INVALID_FOLDER_NAME", "Invalid folder name")
		return
	}

	parentPath := filepath.Clean(storageRecord.MountPath)
	if req.ParentID != "" {
		decodedParent, err := decodeFileID(req.ParentID)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "INVALID_PARENT_ID", "Invalid parentId")
			return
		}
		parentPath = decodedParent
	}

	if !isPathWithinRoot(storageRecord.MountPath, parentPath) {
		writeAPIError(w, http.StatusBadRequest, "PARENT_OUT_OF_RANGE", "parentId is outside the storage root")
		return
	}

	targetPath := filepath.Join(parentPath, name)
	if !isPathWithinRoot(storageRecord.MountPath, targetPath) {
		writeAPIError(w, http.StatusBadRequest, "TARGET_PATH_INVALID", "Invalid target path")
		return
	}

	if err := createFolder(targetPath); err != nil {
		if os.IsExist(err) {
			writeAPIError(w, http.StatusConflict, "FOLDER_ALREADY_EXISTS", "Folder already exists")
			return
		}
		writeAPIError(w, http.StatusInternalServerError, "CREATE_FOLDER_FAILED", "Failed to create folder: "+err.Error())
		return
	}

	w.WriteHeader(http.StatusCreated)
	writeJSON(w, CreateFolderResponse{
		ID:       encodeFilePath(targetPath),
		ParentID: encodeFilePath(parentPath),
		Name:     name,
	})
}

func createFolder(targetPath string) error {
	if err := os.Mkdir(targetPath, 0o755); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrPermission) {
		return err
	}

	if _, err := commandrunner.RunWithOptions(
		context.Background(),
		commandrunner.Options{UseSudo: true},
		"mkdir",
		targetPath,
	); err != nil {
		if _, statErr := os.Stat(targetPath); statErr == nil {
			return os.ErrExist
		}
		return err
	}
	return nil
}
