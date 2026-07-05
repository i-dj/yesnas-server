package system

import (
	"encoding/json"
	"net/http"
	"strings"

	"nas-server/internal/storage"
)

func (h *Handler) HandleStartCloudConnect(w http.ResponseWriter, r *http.Request) {
	h.handleStartCloudConnect(w, r, strings.TrimSpace(r.PathValue("provider")))
}

func (h *Handler) handleStartCloudConnect(w http.ResponseWriter, r *http.Request, provider string) {
	var req storage.CloudConnectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}

	result, err := storage.StartCloudOAuthViaBroker(r.Context(), provider, req)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "CLOUD_CONNECT_FAILED", err.Error())
		return
	}

	w.WriteHeader(http.StatusCreated)
	writeJSON(w, result)
}

func (h *Handler) HandleCloudOAuthStatus(w http.ResponseWriter, r *http.Request) {
	sessionID := strings.TrimSpace(r.PathValue("sessionId"))
	if sessionID == "" {
		writeAPIError(w, http.StatusBadRequest, "INVALID_SESSION_ID", "sessionId is required")
		return
	}
	result, err := storage.GetCloudOAuthBrokerStatus(r.Context(), sessionID)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "CLOUD_OAUTH_STATUS_FAILED", err.Error())
		return
	}
	writeJSON(w, result)
}

func (h *Handler) HandleCompleteCloudConnect(w http.ResponseWriter, r *http.Request) {
	h.handleCompleteCloudConnect(w, r, strings.TrimSpace(r.PathValue("provider")))
}

func (h *Handler) handleCompleteCloudConnect(w http.ResponseWriter, r *http.Request, provider string) {
	var req storage.CloudConnectCompleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	result, err := storage.CompleteCloudOAuthViaBroker(r.Context(), provider, strings.TrimSpace(req.SessionID))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "CLOUD_COMPLETE_FAILED", err.Error())
		return
	}
	writeJSON(w, result)
}

func (h *Handler) HandleStartCloudConnectRedirect(w http.ResponseWriter, r *http.Request) {
	h.handleStartCloudConnectRedirect(w, r, strings.TrimSpace(r.PathValue("provider")))
}

func (h *Handler) handleStartCloudConnectRedirect(w http.ResponseWriter, r *http.Request, provider string) {
	req := storage.CloudConnectRequest{
		StorageID: strings.TrimSpace(r.URL.Query().Get("storageId")),
		Name:      strings.TrimSpace(r.URL.Query().Get("name")),
		RootPath:  strings.TrimSpace(r.URL.Query().Get("rootPath")),
	}

	result, err := storage.StartCloudOAuthViaBroker(r.Context(), provider, req)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "CLOUD_CONNECT_FAILED", err.Error())
		return
	}

	http.Redirect(w, r, result.AuthURL, http.StatusFound)
}
