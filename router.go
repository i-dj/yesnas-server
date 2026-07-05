package main

import (
	"log"
	"net/http"
	"os"
	"strings"

	"nas-server/database"
	auditmodule "nas-server/internal/audit"
	authmodule "nas-server/internal/auth"
	cloudmonitor "nas-server/internal/cloudmonitor"
	dockermodule "nas-server/internal/docker"
	filesmodule "nas-server/internal/files"
	jobsmodule "nas-server/internal/jobs"
	sharingmodule "nas-server/internal/sharing"
	storagepool "nas-server/internal/storagepool"
	systemmodule "nas-server/internal/system"
	usersmodule "nas-server/internal/users"
)

type Router struct{}

func newRouter() http.Handler {
	if err := database.InitDB("data/nas1.db"); err != nil {
		log.Fatalf("Init DB error: %v", err)
	}
	database.DB.Exec("PRAGMA foreign_keys = ON;")
	if err := database.CreateTables(); err != nil {
		log.Fatalf("Create Tables error: %v", err)
	}
	if err := database.EnsureStorageSchema(); err != nil {
		log.Fatalf("Ensure Storage Schema error: %v", err)
	}
	if err := database.EnsureStorageTokenSchema(); err != nil {
		log.Fatalf("Ensure Storage Token Schema error: %v", err)
	}
	if err := database.EnsureOAuthBrokerSchema(); err != nil {
		log.Fatalf("Ensure OAuth Broker Schema error: %v", err)
	}
	if err := database.EnsureStoragePoolSchema(); err != nil {
		log.Fatalf("Ensure Storage Pool Schema error: %v", err)
	}
	if err := database.EnsureStoragePoolSnapshotSchema(); err != nil {
		log.Fatalf("Ensure Storage Pool Snapshot Schema error: %v", err)
	}
	if err := database.EnsureJobsSchema(); err != nil {
		log.Fatalf("Ensure Jobs Schema error: %v", err)
	}
	if err := database.EnsureUsersSchema(); err != nil {
		log.Fatalf("Ensure Users Schema error: %v", err)
	}
	if err := database.EnsureUserSessionSchema(); err != nil {
		log.Fatalf("Ensure User Session Schema error: %v", err)
	}
	if err := database.EnsureAuditLogSchema(); err != nil {
		log.Fatalf("Ensure Audit Log Schema error: %v", err)
	}
	if err := database.EnsureFileShareSchema(); err != nil {
		log.Fatalf("Ensure File Share Schema error: %v", err)
	}
	geoIPPath := strings.TrimSpace(os.Getenv("GEOLITE2_CITY_DB"))
	if geoIPPath == "" {
		geoIPPath = "data/GeoLite2-City.mmdb"
	}
	if err := auditmodule.InitGeoIP(geoIPPath); err != nil {
		log.Printf("notice: GeoIP disabled: %v", err)
	} else {
		log.Printf("GeoIP database loaded: %s", geoIPPath)
	}
	if err := auditmodule.BackfillLegacyLogs(); err != nil {
		log.Printf("notice: audit log backfill failed: %v", err)
	}
	if err := database.RunSeed("database/seed.sql"); err != nil {
		log.Printf("notice: %v", err)
	}
	jobsmodule.StartWorker()
	cloudmonitor.StartWorker()

	mux := http.NewServeMux()
	filesHandler := filesmodule.NewHandler()
	auditHandler := auditmodule.NewHandler()
	authHandler := authmodule.NewHandler()
	dockerHandler := dockermodule.NewHandler()
	jobsHandler := jobsmodule.NewHandler()
	sharingHandler := sharingmodule.NewHandler()
	storagePoolHandler := storagepool.NewHandler()
	systemHandler := systemmodule.NewHandler()
	usersHandler := usersmodule.NewHandler()

	mux.HandleFunc("GET /api/v1/storages/{storage}/files", filesHandler.HandleList)
	mux.HandleFunc("POST /api/v1/auth/login", authHandler.HandleLogin)
	mux.HandleFunc("POST /api/v1/auth/logout", authHandler.HandleLogout)
	mux.HandleFunc("GET /api/v1/logs", auditHandler.HandleList)
	mux.HandleFunc("GET /api/v1/logs/heatmap", auditHandler.HandleHeatmap)
	mux.HandleFunc("GET /api/v1/storages", systemHandler.HandleStorageList)
	mux.HandleFunc("GET /api/v1/storages/{provider}/connect", systemHandler.HandleStartCloudConnectRedirect)
	mux.HandleFunc("POST /api/v1/storages/{provider}/connect", systemHandler.HandleStartCloudConnect)
	mux.HandleFunc("GET /api/v1/storages/{provider}/oauth-status/{sessionId}", systemHandler.HandleCloudOAuthStatus)
	mux.HandleFunc("POST /api/v1/storages/{provider}/complete", systemHandler.HandleCompleteCloudConnect)
	mux.HandleFunc("GET /api/v1/system/disks", systemHandler.HandleSystemDisks)
	mux.HandleFunc("GET /api/v1/system/status", systemHandler.HandleSystemStatus)
	mux.HandleFunc("GET /api/v1/system/status/stream", systemHandler.HandleSystemStatusStream)
	mux.HandleFunc("GET /api/v1/system/hardware", systemHandler.HandleHardware)
	mux.HandleFunc("GET /api/v1/system/hardware/stream", systemHandler.HandleHardwareStream)
	mux.HandleFunc("GET /api/v1/system/network", systemHandler.HandleNetworkInterfaces)
	mux.HandleFunc("GET /api/v1/system/network/stream", systemHandler.HandleNetworkInterfacesStream)
	mux.HandleFunc("GET /api/v1/system/raid/candidates", systemHandler.HandleRaidCandidates)
	mux.HandleFunc("GET /api/v1/system/storage-pools", storagePoolHandler.HandleListPools)
	mux.HandleFunc("POST /api/v1/system/storage-pools", storagePoolHandler.HandleCreatePool)
	mux.HandleFunc("PUT /api/v1/system/storage-pools/{poolId}", storagePoolHandler.HandleUpdatePool)
	mux.HandleFunc("DELETE /api/v1/system/storage-pools/{poolId}", storagePoolHandler.HandleDeletePool)
	mux.HandleFunc("POST /api/v1/system/storage-pools/{poolId}/format", storagePoolHandler.HandleFormatPool)
	mux.HandleFunc("POST /api/v1/system/storage-pools/{poolId}/devices/replace", storagePoolHandler.HandleReplaceDevice)
	mux.HandleFunc("GET /api/v1/system/storage-pools/{poolId}/benchmark/stream", storagePoolHandler.HandleBenchmarkPoolStream)
	mux.HandleFunc("GET /api/v1/system/storage-pools/{poolId}/snapshots", storagePoolHandler.HandleListSnapshots)
	mux.HandleFunc("POST /api/v1/system/storage-pools/{poolId}/snapshots", storagePoolHandler.HandleCreateSnapshot)
	mux.HandleFunc("DELETE /api/v1/system/storage-pools/{poolId}/snapshots/{snapshotId}", storagePoolHandler.HandleDeleteSnapshot)
	mux.HandleFunc("POST /api/v1/system/storage-pools/{poolId}/snapshots/{snapshotId}/restore", storagePoolHandler.HandleRestoreSnapshot)
	mux.HandleFunc("GET /api/v1/storages/io-stats", systemHandler.HandleAllStorageIOStats)
	mux.HandleFunc("GET /api/v1/storages/io-stats/stream", systemHandler.HandleAllStorageIOStatsStream)
	mux.HandleFunc("GET /api/v1/storages/{storage}/io-stats", systemHandler.HandleStorageIOStats)
	mux.HandleFunc("GET /api/v1/storages/{storage}/io-stats/stream", systemHandler.HandleStorageIOStatsStream)
	mux.HandleFunc("GET /api/v1/files/tags", filesHandler.HandleGlobalTagList)
	mux.HandleFunc("GET /api/v1/files/trash", filesHandler.HandleGlobalTrashList)
	mux.HandleFunc("GET /api/v1/storages/{storage}/files/{fileId}/thumbnail", filesHandler.HandleThumbnail)
	mux.HandleFunc("GET /api/v1/storages/{storage}/files/{fileId}/content", filesHandler.HandleContent)
	mux.HandleFunc("PATCH /api/v1/storages/{storage}/files/{fileId}", filesHandler.HandleRename)
	mux.HandleFunc("POST /api/v1/storages/{storage}/files/{fileId}/conflicts", filesHandler.HandleConflictCheck)
	mux.HandleFunc("POST /api/v1/storages/{storage}/files/{fileId}/move", filesHandler.HandleMove)
	mux.HandleFunc("POST /api/v1/storages/{storage}/files/{fileId}/copy", filesHandler.HandleCopy)
	mux.HandleFunc("DELETE /api/v1/storages/{storage}/files/{fileId}", filesHandler.HandleDelete)
	mux.HandleFunc("POST /api/v1/storages/{storage}/folders", filesHandler.HandleCreateFolder)
	mux.HandleFunc("POST /api/v1/uploads/tus", filesHandler.HandleTusCreate)
	mux.HandleFunc("HEAD /api/v1/uploads/tus/{uploadId}", filesHandler.HandleTusHead)
	mux.HandleFunc("PATCH /api/v1/uploads/tus/{uploadId}", filesHandler.HandleTusPatch)
	mux.HandleFunc("DELETE /api/v1/uploads/tus/{uploadId}", filesHandler.HandleTusDelete)
	mux.HandleFunc("GET /api/v1/jobs", jobsHandler.HandleList)
	mux.HandleFunc("GET /api/v1/jobs/scheduled", jobsHandler.HandleScheduledList)
	mux.HandleFunc("GET /api/v1/jobs/{jobId}", jobsHandler.HandleGet)
	mux.HandleFunc("DELETE /api/v1/jobs/{jobId}", jobsHandler.HandleDelete)
	mux.HandleFunc("POST /api/v1/jobs/{jobId}/pause", jobsHandler.HandlePause)
	mux.HandleFunc("POST /api/v1/jobs/{jobId}/resume", jobsHandler.HandleResume)
	mux.HandleFunc("POST /api/v1/jobs/{jobId}/cancel", jobsHandler.HandleCancel)
	mux.HandleFunc("GET /api/v1/docker/containers", dockerHandler.HandleListContainers)
	mux.HandleFunc("GET /api/v1/docker/containers/stream", dockerHandler.HandleListContainersStream)
	mux.HandleFunc("GET /api/v1/users", usersHandler.HandleList)
	mux.HandleFunc("POST /api/v1/users", usersHandler.HandleCreate)
	mux.HandleFunc("PUT /api/v1/users/{userId}", usersHandler.HandleUpdate)
	mux.HandleFunc("DELETE /api/v1/users/{userId}", usersHandler.HandleDelete)
	mux.HandleFunc("GET /api/v1/file-shares", sharingHandler.HandleList)
	mux.HandleFunc("POST /api/v1/file-shares", sharingHandler.HandleCreate)
	mux.HandleFunc("GET /api/v1/file-shares/summary", sharingHandler.HandleSummary)
	mux.HandleFunc("GET /api/v1/file-shares/protocols", sharingHandler.HandleProtocolServices)
	mux.HandleFunc("POST /api/v1/file-shares/protocols/{protocol}/action", sharingHandler.HandleProtocolServiceAction)
	mux.HandleFunc("GET /api/v1/file-shares/{shareId}", sharingHandler.HandleGet)
	mux.HandleFunc("PUT /api/v1/file-shares/{shareId}", sharingHandler.HandleUpdate)
	mux.HandleFunc("DELETE /api/v1/file-shares/{shareId}", sharingHandler.HandleDelete)

	return cors(auditmodule.Middleware(authmodule.Middleware(mux)))
}

func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, HEAD, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, X-Auth-Token, Content-Type, Tus-Resumable, Upload-Length, Upload-Offset, Upload-Metadata")
		w.Header().Set("Access-Control-Expose-Headers", "Location, Upload-Offset, Upload-Length, Tus-Resumable, Tus-Version, Tus-Extension, Tus-Max-Size")
		if strings.HasPrefix(r.URL.Path, "/api/v1/uploads/tus") || strings.HasPrefix(r.URL.Path, "/api/v1/upload2") {
			w.Header().Set("Tus-Resumable", "1.0.0")
			w.Header().Set("Tus-Version", "1.0.0")
			w.Header().Set("Tus-Extension", "creation,termination")
			w.Header().Set("Tus-Max-Size", "107374182400")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
