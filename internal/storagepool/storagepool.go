package storagepool

import "nas-server/pkg/httpx"

type Handler struct{}

func NewHandler() *Handler {
	return &Handler{}
}

var (
	writeJSON     = httpx.WriteJSON
	writeAPIError = httpx.WriteAPIError
)
