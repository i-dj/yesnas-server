package system

import (
	"net/http"
	"runtime"

	"nas-server/pkg/disks"
	darwinplatform "nas-server/platform/darwin"
	linuxplatform "nas-server/platform/linux"
)

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
	var (
		result disks.DiskList
		err    error
	)
	switch runtime.GOOS {
	case "linux":
		result, err = linuxplatform.ListDisks(r.Context())
	case "darwin":
		result, err = darwinplatform.ListDisks(r.Context())
	default:
		err = errUnsupportedPlatform("system disks")
	}
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "SYSTEM_DISKS_FAILED", "Failed to load system disks: "+err.Error())
		return
	}
	writeJSON(w, result)
}
