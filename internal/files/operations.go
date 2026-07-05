package files

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"nas-server/internal/storage"
	commandrunner "nas-server/pkg/shell"
)

func (h *Handler) HandleRename(w http.ResponseWriter, r *http.Request) {
	storageRecord, sourcePath, ok := resolveFileOperationSource(w, r)
	if !ok {
		return
	}
	var req RenameFileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	name := strings.TrimSpace(req.Name)
	if !validFileName(name) {
		writeAPIError(w, http.StatusBadRequest, "INVALID_FILE_NAME", "Invalid file name")
		return
	}
	targetPath := filepath.Join(filepath.Dir(sourcePath), name)
	if err := validateOperationTarget(storageRecord.MountPath, sourcePath, targetPath); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_TARGET", err.Error())
		return
	}
	if err := movePath(r.Context(), sourcePath, targetPath); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "RENAME_FILE_FAILED", "Failed to rename file: "+err.Error())
		return
	}
	writeJSON(w, buildFileOperationResponse(storageRecord.MountPath, targetPath))
}

func (h *Handler) HandleConflictCheck(w http.ResponseWriter, r *http.Request) {
	storageRecord, sourcePath, ok := resolveFileOperationSource(w, r)
	if !ok {
		return
	}
	var req FileConflictCheckRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	parentPath, ok := resolveOperationParent(w, storageRecord, req.ParentID)
	if !ok {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = filepath.Base(sourcePath)
	}
	if !validFileName(name) {
		writeAPIError(w, http.StatusBadRequest, "INVALID_FILE_NAME", "Invalid file name")
		return
	}
	targetPath := filepath.Join(parentPath, name)
	if err := validateOperationTargetBase(storageRecord.MountPath, sourcePath, targetPath); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_TARGET", err.Error())
		return
	}
	resp := FileConflictCheckResponse{
		HasConflict: false,
		Name:        name,
		ParentID:    encodeFilePath(parentPath),
	}
	if info, err := os.Stat(targetPath); err == nil {
		targetType := FileType
		if info.IsDir() {
			targetType = FolderType
		}
		resp.HasConflict = true
		resp.TargetID = encodeFilePath(targetPath)
		resp.TargetType = &targetType
	} else if !os.IsNotExist(err) {
		writeAPIError(w, http.StatusInternalServerError, "STAT_TARGET_FAILED", "Failed to check target: "+err.Error())
		return
	}
	writeJSON(w, resp)
}

func (h *Handler) HandleMove(w http.ResponseWriter, r *http.Request) {
	storageRecord, sourcePath, ok := resolveFileOperationSource(w, r)
	if !ok {
		return
	}
	var req MoveFileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	parentPath, ok := resolveOperationParent(w, storageRecord, req.ParentID)
	if !ok {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = filepath.Base(sourcePath)
	}
	if !validFileName(name) {
		writeAPIError(w, http.StatusBadRequest, "INVALID_FILE_NAME", "Invalid file name")
		return
	}
	policy, ok := normalizeConflictPolicy(w, req.ConflictPolicy)
	if !ok {
		return
	}
	targetPath, shouldOverwrite, err := resolveConflictTarget(storageRecord.MountPath, sourcePath, filepath.Join(parentPath, name), policy)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_TARGET", err.Error())
		return
	}
	if isPathWithinRoot(sourcePath, targetPath) {
		writeAPIError(w, http.StatusBadRequest, "MOVE_INTO_SELF", "Cannot move a folder into itself")
		return
	}
	if shouldOverwrite {
		if err := removePath(r.Context(), targetPath); err != nil {
			writeAPIError(w, http.StatusInternalServerError, "OVERWRITE_TARGET_FAILED", "Failed to overwrite target: "+err.Error())
			return
		}
	}
	if err := movePath(r.Context(), sourcePath, targetPath); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "MOVE_FILE_FAILED", "Failed to move file: "+err.Error())
		return
	}
	writeJSON(w, buildFileOperationResponse(storageRecord.MountPath, targetPath))
}

func (h *Handler) HandleCopy(w http.ResponseWriter, r *http.Request) {
	storageRecord, sourcePath, ok := resolveFileOperationSource(w, r)
	if !ok {
		return
	}
	var req CopyFileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	parentPath, ok := resolveOperationParent(w, storageRecord, req.ParentID)
	if !ok {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = filepath.Base(sourcePath)
	}
	if !validFileName(name) {
		writeAPIError(w, http.StatusBadRequest, "INVALID_FILE_NAME", "Invalid file name")
		return
	}
	policy, ok := normalizeConflictPolicy(w, req.ConflictPolicy)
	if !ok {
		return
	}
	targetPath, shouldOverwrite, err := resolveConflictTarget(storageRecord.MountPath, sourcePath, filepath.Join(parentPath, name), policy)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_TARGET", err.Error())
		return
	}
	if isPathWithinRoot(sourcePath, targetPath) {
		writeAPIError(w, http.StatusBadRequest, "COPY_INTO_SELF", "Cannot copy a folder into itself")
		return
	}
	if shouldOverwrite {
		if err := removePath(r.Context(), targetPath); err != nil {
			writeAPIError(w, http.StatusInternalServerError, "OVERWRITE_TARGET_FAILED", "Failed to overwrite target: "+err.Error())
			return
		}
	}
	if err := copyPath(r.Context(), sourcePath, targetPath); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "COPY_FILE_FAILED", "Failed to copy file: "+err.Error())
		return
	}
	writeJSON(w, buildFileOperationResponse(storageRecord.MountPath, targetPath))
}

func resolveFileOperationSource(w http.ResponseWriter, r *http.Request) (*storage.Storage, string, bool) {
	storageID := strings.TrimSpace(r.PathValue("storage"))
	fileID := strings.TrimSpace(r.PathValue("fileId"))
	if fileID == "" {
		writeAPIError(w, http.StatusBadRequest, "FILE_ID_REQUIRED", "fileId is required")
		return nil, "", false
	}
	storageRecord, err := storage.Get(storageID)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "STORAGE_NOT_FOUND", "Storage not found: "+storageID)
		return nil, "", false
	}
	sourcePath, err := decodeFileID(fileID)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_FILE_ID", "Invalid fileId")
		return nil, "", false
	}
	sourcePath = filepath.Clean(sourcePath)
	if !isPathWithinRoot(storageRecord.MountPath, sourcePath) {
		writeAPIError(w, http.StatusBadRequest, "PATH_OUT_OF_RANGE", "Target path is outside the storage root")
		return nil, "", false
	}
	if filepath.Clean(sourcePath) == filepath.Clean(storageRecord.MountPath) {
		writeAPIError(w, http.StatusBadRequest, "ROOT_OPERATION_FORBIDDEN", "Cannot operate on storage root")
		return nil, "", false
	}
	if isPathWithinRoot(filepath.Join(storageRecord.MountPath, ".trash"), sourcePath) {
		writeAPIError(w, http.StatusBadRequest, "TRASH_OPERATION_FORBIDDEN", "Use trash endpoints for files in trash")
		return nil, "", false
	}
	if _, err := os.Stat(sourcePath); err != nil {
		if os.IsNotExist(err) {
			writeAPIError(w, http.StatusNotFound, "FILE_NOT_FOUND", "File not found")
			return nil, "", false
		}
		writeAPIError(w, http.StatusInternalServerError, "STAT_FILE_FAILED", "Failed to read file info: "+err.Error())
		return nil, "", false
	}
	return storageRecord, sourcePath, true
}

func resolveOperationParent(w http.ResponseWriter, storageRecord *storage.Storage, parentID string) (string, bool) {
	parentID = strings.TrimSpace(parentID)
	if parentID == "" {
		writeAPIError(w, http.StatusBadRequest, "PARENT_ID_REQUIRED", "parentId is required")
		return "", false
	}
	parentPath, err := decodeFileID(parentID)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_PARENT_ID", "Invalid parentId")
		return "", false
	}
	parentPath = filepath.Clean(parentPath)
	if !isPathWithinRoot(storageRecord.MountPath, parentPath) {
		writeAPIError(w, http.StatusBadRequest, "PARENT_OUT_OF_RANGE", "parentId is outside the storage root")
		return "", false
	}
	if isPathWithinRoot(filepath.Join(storageRecord.MountPath, ".trash"), parentPath) {
		writeAPIError(w, http.StatusBadRequest, "TRASH_TARGET_FORBIDDEN", "Cannot target trash with this operation")
		return "", false
	}
	info, err := os.Stat(parentPath)
	if err != nil {
		if os.IsNotExist(err) {
			writeAPIError(w, http.StatusNotFound, "PARENT_NOT_FOUND", "Parent folder not found")
			return "", false
		}
		writeAPIError(w, http.StatusInternalServerError, "STAT_PARENT_FAILED", "Failed to read parent folder: "+err.Error())
		return "", false
	}
	if !info.IsDir() {
		writeAPIError(w, http.StatusBadRequest, "PARENT_NOT_FOLDER", "parentId must point to a folder")
		return "", false
	}
	return parentPath, true
}

func validFileName(name string) bool {
	return name != "" && name != "." && name != ".." && name == filepath.Base(name)
}

func validateOperationTargetBase(root string, sourcePath string, targetPath string) error {
	targetPath = filepath.Clean(targetPath)
	if !isPathWithinRoot(root, targetPath) {
		return fmt.Errorf("target path is outside the storage root")
	}
	if filepath.Clean(sourcePath) == targetPath {
		return fmt.Errorf("source and target are the same")
	}
	if isPathWithinRoot(filepath.Join(root, ".trash"), targetPath) {
		return fmt.Errorf("target path cannot be inside trash")
	}
	return nil
}

func validateOperationTarget(root string, sourcePath string, targetPath string) error {
	if err := validateOperationTargetBase(root, sourcePath, targetPath); err != nil {
		return err
	}
	targetPath = filepath.Clean(targetPath)
	if _, err := os.Stat(targetPath); err == nil {
		return fmt.Errorf("target already exists")
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("failed to check target: %w", err)
	}
	return nil
}

func normalizeConflictPolicy(w http.ResponseWriter, policy string) (string, bool) {
	policy = strings.TrimSpace(strings.ToLower(policy))
	if policy == "" {
		policy = "error"
	}
	switch policy {
	case "error", "overwrite", "rename":
		return policy, true
	default:
		writeAPIError(w, http.StatusBadRequest, "INVALID_CONFLICT_POLICY", "conflictPolicy must be one of: error, overwrite, rename")
		return "", false
	}
}

func resolveConflictTarget(root string, sourcePath string, targetPath string, policy string) (string, bool, error) {
	if err := validateOperationTargetBase(root, sourcePath, targetPath); err != nil {
		return "", false, err
	}
	targetPath = filepath.Clean(targetPath)
	if _, err := os.Stat(targetPath); err == nil {
		switch policy {
		case "overwrite":
			return targetPath, true, nil
		case "rename":
			nextPath, err := nextAvailablePath(sourcePath, targetPath)
			if err != nil {
				return "", false, err
			}
			return nextPath, false, nil
		default:
			return "", false, fmt.Errorf("target already exists")
		}
	} else if !os.IsNotExist(err) {
		return "", false, fmt.Errorf("failed to check target: %w", err)
	}
	return targetPath, false, nil
}

func nextAvailablePath(sourcePath string, targetPath string) (string, error) {
	info, err := os.Stat(sourcePath)
	if err != nil {
		return "", fmt.Errorf("failed to read source: %w", err)
	}
	dir := filepath.Dir(targetPath)
	name := filepath.Base(targetPath)
	base := name
	ext := ""
	if !info.IsDir() {
		ext = filepath.Ext(name)
		base = strings.TrimSuffix(name, ext)
	}
	for i := 1; i <= 10000; i++ {
		candidate := filepath.Join(dir, fmt.Sprintf("%s (%d)%s", base, i, ext))
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate, nil
		} else if err != nil {
			return "", fmt.Errorf("failed to check target: %w", err)
		}
	}
	return "", fmt.Errorf("failed to generate available target name")
}

func movePath(ctx context.Context, sourcePath string, targetPath string) error {
	if err := os.Rename(sourcePath, targetPath); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrPermission) && !isCrossDeviceRename(err) {
		return err
	}
	_, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "mv", sourcePath, targetPath)
	return err
}

func copyPath(ctx context.Context, sourcePath string, targetPath string) error {
	_, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "cp", "-a", sourcePath, targetPath)
	return err
}

func removePath(ctx context.Context, targetPath string) error {
	if err := os.RemoveAll(targetPath); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrPermission) {
		return err
	}
	_, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "rm", "-rf", "--", targetPath)
	return err
}

func buildFileOperationResponse(root string, path string) FileOperationResponse {
	info, _ := os.Stat(path)
	fileType := FileType
	if info != nil && info.IsDir() {
		fileType = FolderType
	}
	rel, _ := filepath.Rel(root, path)
	return FileOperationResponse{
		ID:       encodeFilePath(path),
		ParentID: encodeFilePath(filepath.Dir(path)),
		Name:     filepath.Base(path),
		Type:     fileType,
		Path:     filepath.ToSlash(rel),
	}
}
