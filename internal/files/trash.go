package files

import (
	"fmt"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"nas-server/internal/storage"
	"nas-server/pkg/idgen"
)

func (h *Handler) HandleGlobalTagList(w http.ResponseWriter, r *http.Request) {
	favs, err := ListFavorites()
	if err != nil {
		log.Printf("[GLOBAL-TAG-ERROR] 读取收藏失败: %v", err)
		writeAPIError(w, http.StatusInternalServerError, "GLOBAL_TAG_LIST_FAILED", "获取标签文件失败")
		return
	}

	nodes := make([]FileNode, 0)
	for _, fav := range favs {
		storageRecord, err := storage.Get(fav.StorageID)
		if err != nil {
			log.Printf("[GLOBAL-TAG-WARN] 存储节点 %s 已失效", fav.StorageID)
			continue
		}

		fullPath := filepath.Join(storageRecord.MountPath, fav.FilePath)
		info, err := os.Stat(fullPath)
		if err != nil {
			continue
		}

		node := FileNode{
			ID:        fav.ID,
			Name:      fav.FileName,
			Type:      FileType,
			ParentID:  encodeFilePath(filepath.Dir(fullPath)),
			UpdatedAt: info.ModTime().Format(time.RFC3339),
			Size:      info.Size(),
			Extension: filepath.Ext(fav.FileName),
			MimeType:  mime.TypeByExtension(filepath.Ext(fav.FileName)),
			TagColors: fav.Colors,
		}
		if info.IsDir() {
			node.Type = FolderType
		}
		node.MediaType = detectMediaType(fav.FileName, node.MimeType)
		nodes = append(nodes, node)
	}

	writeJSON(w, nodes)
}

func (h *Handler) HandleGlobalTrashList(w http.ResponseWriter, r *http.Request) {
	items, err := ListAllRecycleBinItems()
	if err != nil {
		log.Printf("[GLOBAL-TRASH-ERROR] 读取回收站失败: %v", err)
		writeAPIError(w, http.StatusInternalServerError, "GLOBAL_TRASH_LIST_FAILED", "获取回收站文件失败")
		return
	}

	nodes := make([]TrashNode, 0, len(items))
	for _, item := range items {
		storageRecord, err := storage.Get(item.StorageID)
		if err != nil {
			log.Printf("[GLOBAL-TRASH-WARN] 存储节点 %s 已失效", item.StorageID)
			continue
		}
		nodes = append(nodes, buildTrashNode(item, storageRecord.MountPath))
	}

	writeJSON(w, nodes)
}

func (h *Handler) HandleDelete(w http.ResponseWriter, r *http.Request) {
	storageID := r.PathValue("storage")
	fileID := r.PathValue("fileId")
	if fileID == "" {
		writeAPIError(w, http.StatusBadRequest, "FILE_ID_REQUIRED", "fileId 不能为空")
		return
	}

	storageRecord, err := storage.Get(storageID)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "STORAGE_NOT_FOUND", "存储节点不存在: "+storageID)
		return
	}

	sourcePath, err := decodeFileID(fileID)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_FILE_ID", "fileId 无效")
		return
	}
	if !isPathWithinRoot(storageRecord.MountPath, sourcePath) {
		writeAPIError(w, http.StatusBadRequest, "PATH_OUT_OF_RANGE", "目标路径超出存储范围")
		return
	}

	trashRoot := filepath.Join(storageRecord.MountPath, ".trash")
	if isPathWithinRoot(trashRoot, sourcePath) {
		writeAPIError(w, http.StatusBadRequest, "ALREADY_IN_TRASH", "回收站中的文件不能重复移入回收站")
		return
	}

	info, err := os.Stat(sourcePath)
	if err != nil {
		if os.IsNotExist(err) {
			writeAPIError(w, http.StatusNotFound, "FILE_NOT_FOUND", "文件不存在")
			return
		}
		writeAPIError(w, http.StatusInternalServerError, "STAT_FILE_FAILED", "读取文件信息失败: "+err.Error())
		return
	}

	originalRelPath, err := filepath.Rel(storageRecord.MountPath, sourcePath)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "REL_PATH_FAILED", "计算原始路径失败: "+err.Error())
		return
	}

	now := time.Now()
	trashDir := buildTrashDir(storageRecord.MountPath, now)
	if err := ensureDir(trashDir); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "CREATE_TRASH_DIR_FAILED", "创建回收站目录失败: "+err.Error())
		return
	}

	targetPath := filepath.Join(trashDir, uniqueTrashName(info.Name()))
	if err := os.Rename(sourcePath, targetPath); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "MOVE_TO_TRASH_FAILED", "移动到回收站失败: "+err.Error())
		return
	}

	recycleRelPath, err := filepath.Rel(storageRecord.MountPath, targetPath)
	if err != nil {
		_ = os.Rename(targetPath, sourcePath)
		writeAPIError(w, http.StatusInternalServerError, "TRASH_REL_PATH_FAILED", "计算回收站路径失败: "+err.Error())
		return
	}

	fileType := string(FileType)
	if info.IsDir() {
		fileType = string(FolderType)
	}

	recordID, err := AddRecycleBinItem(RecycleBinItem{
		StorageID:    storageID,
		FileName:     info.Name(),
		OriginalPath: filepath.ToSlash(originalRelPath),
		RecyclePath:  filepath.ToSlash(recycleRelPath),
		FileType:     fileType,
		Size:         info.Size(),
	})
	if err != nil {
		_ = os.Rename(targetPath, sourcePath)
		writeAPIError(w, http.StatusInternalServerError, "RECYCLE_RECORD_FAILED", "写入回收站记录失败: "+err.Error())
		return
	}

	if err := DeleteFavoriteByPath(storageID, filepath.ToSlash(originalRelPath)); err != nil {
		log.Printf("[RECYCLE-WARN] 删除收藏记录失败: storage=%s path=%s err=%v", storageID, originalRelPath, err)
	}

	writeJSON(w, DeleteFileResponse{
		ID:           recordID,
		StorageID:    storageID,
		OriginalPath: filepath.ToSlash(originalRelPath),
		RecyclePath:  filepath.ToSlash(recycleRelPath),
		Name:         info.Name(),
	})
}

func buildTrashDir(root string, now time.Time) string {
	return filepath.Join(root, ".trash", now.Format("2006"), now.Format("01"), now.Format("02"))
}

func ensureDir(path string) error {
	return os.MkdirAll(path, 0o755)
}

func uniqueTrashName(name string) string {
	return fmt.Sprintf("%s_%s", idgen.New(), filepath.Base(name))
}

func buildTrashNode(item RecycleBinItem, storageRoot string) TrashNode {
	recycleAbsPath := filepath.Join(storageRoot, item.RecyclePath)
	mimeType := mime.TypeByExtension(filepath.Ext(item.FileName))
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	node := TrashNode{
		ID:           item.ID,
		StorageID:    item.StorageID,
		Name:         item.FileName,
		ParentID:     encodeFilePath(filepath.Dir(recycleAbsPath)),
		OriginalPath: item.OriginalPath,
		RecyclePath:  item.RecyclePath,
		DeletedAt:    item.DeletedAt.Format(time.RFC3339),
		Size:         item.Size,
		Extension:    filepath.Ext(item.FileName),
		IsHidden:     strings.HasPrefix(item.FileName, "."),
		MimeType:     mimeType,
		MediaType:    detectMediaType(item.FileName, mimeType),
		TagColors:    []FavoriteColor{},
	}
	if item.ExpiresAt != nil {
		node.ExpiresAt = item.ExpiresAt.Format(time.RFC3339)
	}
	if item.FileType == string(FolderType) {
		node.Type = FolderType
	} else {
		node.Type = FileType
	}
	return node
}
