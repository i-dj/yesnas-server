package system

import (
	"context"
	"net/http"
	"runtime"

	"nas-server/pkg/disks"
	darwinplatform "nas-server/platform/darwin"
	linuxplatform "nas-server/platform/linux"
)

func listSystemDisksDetails(ctx context.Context) (disks.DiskList, error) {
	switch runtime.GOOS {
	case "linux":
		return linuxplatform.ListDisks(ctx)
	case "darwin":
		return darwinplatform.ListDisks(ctx)
	default:
		return disks.DiskList{}, errUnsupportedPlatform("system disks")
	}
}

func (h *Handler) HandleRaidCandidates(w http.ResponseWriter, r *http.Request) {
	var (
		result disks.CandidateList
		err    error
	)
	switch runtime.GOOS {
	case "linux":
		result, err = linuxplatform.ListCandidates(r.Context())
	case "darwin":
		result, err = darwinplatform.ListCandidates(r.Context())
	default:
		err = errUnsupportedPlatform("raid candidates")
	}
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "RAID_CANDIDATES_FAILED", "Failed to load RAID candidates: "+err.Error())
		return
	}
	writeJSON(w, result)
}

func (h *Handler) HandleSystemDisks(w http.ResponseWriter, r *http.Request) {
	result, err := listSystemDisksDetails(r.Context())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "SYSTEM_DISKS_FAILED", "Failed to load system disks: "+err.Error())
		return
	}
	writeJSON(w, result)
}
