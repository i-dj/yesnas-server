package cloudmonitor

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"nas-server/internal/storage"
)

const (
	initialDelay    = 2 * time.Second
	monitorInterval = 5 * time.Minute
	mountTimeout    = 45 * time.Second
	usageTimeout    = 30 * time.Second
	accountTimeout  = 15 * time.Second
)

var (
	startOnce sync.Once
	runMu     sync.Mutex
)

func StartWorker() {
	startOnce.Do(func() {
		log.Printf("[CLOUD] monitor starting interval=%s", monitorInterval)
		go runWorker()
	})
}

func runWorker() {
	timer := time.NewTimer(initialDelay)
	defer timer.Stop()

	<-timer.C
	checkAll(context.Background())

	ticker := time.NewTicker(monitorInterval)
	defer ticker.Stop()

	for range ticker.C {
		checkAll(context.Background())
	}
}

func checkAll(ctx context.Context) {
	if !runMu.TryLock() {
		log.Printf("[CLOUD] monitor skipped; previous run still active")
		return
	}
	defer runMu.Unlock()

	items, err := storage.List()
	if err != nil {
		log.Printf("[CLOUD] list cloud storages failed err=%v", err)
		return
	}

	for i := range items {
		item := items[i]
		if storage.IsNetworkProvider(item.Provider) {
			checkNetworkStorage(ctx, item)
			continue
		}
		if isSupportedCloud(item) {
			checkGoogleDrive(ctx, item)
		}
	}
}

func isSupportedCloud(item storage.Storage) bool {
	return strings.TrimSpace(item.Provider) == string(storage.ProviderGoogleDrive)
}

func checkGoogleDrive(ctx context.Context, item storage.Storage) {
	token, err := storage.GetTokenByStorageID(item.ID)
	if err != nil {
		log.Printf("[CLOUD] load google drive token failed id=%s err=%v", item.ID, err)
		return
	}
	if token == nil {
		log.Printf("[CLOUD] skip google drive id=%s reason=missing_token", item.ID)
		return
	}

	accountCtx, accountCancel := context.WithTimeout(ctx, accountTimeout)
	storage.BackfillGoogleDriveAccountEmail(accountCtx, &item)
	accountCancel()

	mountCtx, mountCancel := context.WithTimeout(ctx, mountTimeout)
	if err := storage.EnsureGoogleDriveMounted(mountCtx, &item, token); err != nil {
		log.Printf("[CLOUD] mount google drive failed id=%s path=%s err=%v", item.ID, item.MountPath, err)
	} else {
		log.Printf("[CLOUD] google drive mount ok id=%s path=%s", item.ID, item.MountPath)
	}
	mountCancel()

	usageCtx, usageCancel := context.WithTimeout(ctx, usageTimeout)
	if _, err := storage.RefreshGoogleDriveUsage(usageCtx, &item); err != nil {
		log.Printf("[CLOUD] refresh google drive usage failed id=%s err=%v", item.ID, err)
	} else {
		log.Printf("[CLOUD] google drive usage refreshed id=%s", item.ID)
	}
	usageCancel()
}

func checkNetworkStorage(ctx context.Context, item storage.Storage) {
	mountCtx, mountCancel := context.WithTimeout(ctx, mountTimeout)
	if err := storage.RefreshNetworkStorageUsage(mountCtx, &item); err != nil {
		log.Printf("[CLOUD] refresh network storage failed id=%s provider=%s path=%s err=%v", item.ID, item.Provider, item.MountPath, err)
	} else {
		log.Printf("[CLOUD] network storage mount ok id=%s provider=%s path=%s", item.ID, item.Provider, item.MountPath)
	}
	mountCancel()
}
