package iostats

import (
	"context"
	"runtime"
	"time"

	storagepkg "nas-server/internal/storage"
	stats "nas-server/pkg/iostatstypes"
	darwinplatform "nas-server/platform/darwin"
	linuxplatform "nas-server/platform/linux"
)

var (
	ErrNotSupported = stats.ErrNotSupported
	ErrTraceBusy    = stats.ErrTraceBusy
)

type Stats = stats.Stats

type Provider interface {
	Sample(ctx context.Context, storage storagepkg.Storage, interval time.Duration) (Stats, error)
}

func NewProvider() Provider {
	switch runtime.GOOS {
	case "darwin":
		return darwinplatform.Provider{}
	case "linux":
		return linuxplatform.Provider{}
	default:
		return linuxplatform.Provider{}
	}
}
