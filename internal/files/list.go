package files

import (
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"nas-server/internal/storage"
)

func (h *Handler) HandleList(w http.ResponseWriter, r *http.Request) {
	storageID := r.PathValue("storage")
	storageRecord, err := storage.Get(storageID)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "STORAGE_NOT_FOUND", "存储节点不存在: "+storageID)
		return
	}

	root := storageRecord.MountPath
	q := r.URL.Query()
	limit, offset := parsePagination(q.Get("limit"), q.Get("offset"))
	favMap := getFavoriteMap()
	targetPath := root
	var nodes []FileNode

	switch q.Get("type") {
	case "trash":
		items, err := ListRecycleBinItemsByStorage(storageID)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "TRASH_LIST_FAILED", "获取回收站失败: "+err.Error())
			return
		}

		trashNodes := make([]TrashNode, 0, len(items))
		for _, item := range items {
			trashNodes = append(trashNodes, buildTrashNode(item, root))
		}

		writeJSON(w, TrashResponseData{
			Files:       paginate(trashNodes, limit, offset),
			Breadcrumbs: []Breadcrumb{{ID: encodeFilePath(filepath.Join(root, ".trash")), Name: "回收站"}},
		})
		return

	case "tag":
		favs, err := ListFavorites()
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "TAG_LIST_FAILED", "查询标签失败: "+err.Error())
			return
		}

		for _, fav := range favs {
			if fav.StorageID != storageID {
				continue
			}
			favStorage, err := storage.Get(fav.StorageID)
			if err != nil {
				continue
			}
			fullPath := filepath.Join(favStorage.MountPath, fav.FilePath)
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
				Extension: filepath.Ext(fav.FileName),
				Size:      info.Size(),
				MimeType:  mime.TypeByExtension(filepath.Ext(fav.FileName)),
				TagColors: fav.Colors,
			}
			if info.IsDir() {
				node.Type = FolderType
			}
			node.MediaType = detectMediaType(fav.FileName, node.MimeType)
			nodes = append(nodes, node)
		}

	default:
		parentID := q.Get("parentId")
		if parentID != "" {
			if decoded, err := decodeFileID(parentID); err == nil {
				targetPath = decoded
			}
		}

		nodes, err = scanDirectory(targetPath, root, favMap)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "SCAN_DIRECTORY_FAILED", err.Error())
			return
		}
	}

	writeJSON(w, ResponseData{
		Files:       paginate(nodes, limit, offset),
		Breadcrumbs: buildBreadcrumbs(targetPath, root),
	})
}
