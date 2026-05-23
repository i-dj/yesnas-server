package files

import (
	"encoding/base64"
	"log"
	"mime"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func detectMediaType(fileName string, mimeType string) string {
	ext := strings.ToLower(filepath.Ext(fileName))
	unsupported := map[string]bool{".psd": true, ".ai": true, ".tiff": true}
	if unsupported[ext] {
		return "file"
	}
	if strings.HasPrefix(mimeType, "image/") {
		return "image"
	}
	if strings.HasPrefix(mimeType, "video/") {
		return "video"
	}
	return "file"
}

func encodeFilePath(path string) string {
	return base64.URLEncoding.EncodeToString([]byte(filepath.Clean(path)))
}

func decodeFileID(id string) (string, error) {
	b, err := base64.URLEncoding.DecodeString(id)
	if err != nil {
		return "", err
	}
	return filepath.Clean(string(b)), nil
}

func buildBreadcrumbs(path, root string) []Breadcrumb {
	path = filepath.Clean(path)
	root = filepath.Clean(root)
	if !strings.HasPrefix(path, root) {
		return []Breadcrumb{{ID: encodeFilePath(root), Name: "Root"}}
	}
	crumbs := []Breadcrumb{{ID: encodeFilePath(root), Name: "Root"}}
	rel, _ := filepath.Rel(root, path)
	current := root
	for _, part := range strings.Split(rel, string(os.PathSeparator)) {
		if part == "." || part == "" {
			continue
		}
		current = filepath.Join(current, part)
		crumbs = append(crumbs, Breadcrumb{ID: encodeFilePath(current), Name: part})
	}
	return crumbs
}

func isPathWithinRoot(root, target string) bool {
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)))
}

func getFavoriteMap() map[string][]FavoriteColor {
	favMap := make(map[string][]FavoriteColor)
	favs, err := ListFavorites()
	if err != nil {
		log.Printf("Warning: Failed to load favorites: %v", err)
		return favMap
	}
	for _, f := range favs {
		favMap[f.FilePath] = f.Colors
	}
	return favMap
}

func buildFileNode(e os.DirEntry, dir string, colors []FavoriteColor) (FileNode, error) {
	info, err := e.Info()
	if err != nil {
		return FileNode{}, err
	}
	fullPath := filepath.Join(dir, e.Name())
	ext := filepath.Ext(e.Name())
	mimeType := mime.TypeByExtension(ext)
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	node := FileNode{
		ID:        encodeFilePath(fullPath),
		Name:      e.Name(),
		ParentID:  encodeFilePath(dir),
		UpdatedAt: info.ModTime().Format(time.RFC3339),
		Extension: ext,
		IsHidden:  strings.HasPrefix(e.Name(), "."),
		MimeType:  mimeType,
		MediaType: detectMediaType(e.Name(), mimeType),
		TagColors: colors,
	}
	if e.IsDir() {
		node.Type = FolderType
	} else {
		node.Type = FileType
		node.Size = info.Size()
	}
	return node, nil
}

func scanDirectory(targetDir, storageRoot string, favMap map[string][]FavoriteColor) ([]FileNode, error) {
	entries, err := os.ReadDir(targetDir)
	if err != nil {
		return nil, err
	}
	var nodes []FileNode
	for _, e := range entries {
		relPath, _ := filepath.Rel(storageRoot, filepath.Join(targetDir, e.Name()))
		colors := favMap[relPath]
		if colors == nil {
			colors = []FavoriteColor{}
		}
		if node, err := buildFileNode(e, targetDir, colors); err == nil {
			nodes = append(nodes, node)
		}
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Name < nodes[j].Name })
	return nodes, nil
}
