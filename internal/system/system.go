package system

import (
	"fmt"
	"runtime"

	"nas-server/pkg/httpx"
)

type Handler struct{}

func NewHandler() *Handler {
	return &Handler{}
}

var (
	writeJSON     = httpx.WriteJSON
	writeAPIError = httpx.WriteAPIError
	prepareSSE    = httpx.PrepareSSE
	writeSSEEvent = httpx.WriteSSEEvent
)

type APIError = httpx.APIError

func errUnsupportedPlatform(feature string) error {
	return fmt.Errorf("%s not supported on %s", feature, runtime.GOOS)
}
