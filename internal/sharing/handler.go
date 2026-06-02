package sharing

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"

	"nas-server/pkg/httpx"
)

type Handler struct{}

var (
	writeJSON     = httpx.WriteJSON
	writeAPIError = httpx.WriteAPIError
)

func NewHandler() *Handler {
	return &Handler{}
}

func (h *Handler) HandleList(w http.ResponseWriter, r *http.Request) {
	items, err := ListShares()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "FILE_SHARES_LIST_FAILED", "Failed to list file shares: "+err.Error())
		return
	}
	if items == nil {
		items = []Share{}
	}
	writeJSON(w, map[string]any{"items": items})
}

func (h *Handler) HandleGet(w http.ResponseWriter, r *http.Request) {
	item, err := GetShare(r.PathValue("shareId"))
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "FILE_SHARE_NOT_FOUND", "File share not found")
		return
	}
	writeJSON(w, item)
}

func (h *Handler) HandleCreate(w http.ResponseWriter, r *http.Request) {
	var req UpsertShareRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	item, err := UpsertShare("", req)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "FILE_SHARE_CREATE_FAILED", err.Error())
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, item)
}

func (h *Handler) HandleUpdate(w http.ResponseWriter, r *http.Request) {
	var req UpsertShareRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	item, err := UpsertShare(r.PathValue("shareId"), req)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "FILE_SHARE_UPDATE_FAILED", err.Error())
		return
	}
	writeJSON(w, item)
}

func (h *Handler) HandleDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("shareId")
	if err := DeleteShare(id); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "FILE_SHARE_DELETE_FAILED", "Failed to delete file share: "+err.Error())
		return
	}
	writeJSON(w, map[string]any{"deleted": true, "id": id})
}

func (h *Handler) HandleSummary(w http.ResponseWriter, r *http.Request) {
	host := requestHost(r)
	items, err := ListProtocolServices(r.Context(), host)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "FILE_SHARE_SUMMARY_FAILED", "Failed to summarize file shares: "+err.Error())
		return
	}
	writeJSON(w, map[string]any{"items": items})
}

func (h *Handler) HandleProtocolServices(w http.ResponseWriter, r *http.Request) {
	host := requestHost(r)
	items, err := ListProtocolServices(r.Context(), host)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "PROTOCOL_SERVICES_FAILED", "Failed to list protocol services: "+err.Error())
		return
	}
	writeJSON(w, map[string]any{"items": items})
}

func (h *Handler) HandleProtocolServiceAction(w http.ResponseWriter, r *http.Request) {
	var req ProtocolActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	host := requestHost(r)
	item, err := ControlProtocolService(r.Context(), Protocol(r.PathValue("protocol")), req.Action, host)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "PROTOCOL_SERVICE_ACTION_FAILED", err.Error())
		return
	}
	writeJSON(w, item)
}

func requestHost(r *http.Request) string {
	host := r.Host
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-Host")); forwarded != "" {
		host = forwarded
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if host == "" {
		return "yesnas.local"
	}
	return host
}
