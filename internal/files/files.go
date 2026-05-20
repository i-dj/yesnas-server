package files

import (
	"fmt"

	"nas-server/pkg/httpx"
)

type Handler struct{}

func NewHandler() *Handler {
	return &Handler{}
}

var (
	writeJSON     = httpx.WriteJSON
	writeAPIError = httpx.WriteAPIError
)

func parsePagination(limitRaw, offsetRaw string) (limit int, offset int) {
	limit = 100
	offset = 0
	if limitRaw != "" {
		if _, err := fmt.Sscanf(limitRaw, "%d", &limit); err != nil || limit <= 0 {
			limit = 100
		}
	}
	if offsetRaw != "" {
		if _, err := fmt.Sscanf(offsetRaw, "%d", &offset); err != nil || offset < 0 {
			offset = 0
		}
	}
	return limit, offset
}

func paginate[T any](items []T, limit int, offset int) []T {
	if offset >= len(items) {
		return []T{}
	}
	end := offset + limit
	if end > len(items) {
		end = len(items)
	}
	return items[offset:end]
}
