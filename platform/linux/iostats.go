package linuxplatform

import (
	"context"
	"runtime"
	"time"

	storagepkg "nas-server/internal/storage"
	stats "nas-server/pkg/iostatstypes"
)

type Provider struct{}

func (Provider) Sample(ctx context.Context, storage storagepkg.Storage, interval time.Duration) (stats.Stats, error) {
	_ = ctx
	_ = storage
	_ = interval
	return stats.Stats{
		Platform: runtime.GOOS,
	}, stats.ErrNotSupported
}
