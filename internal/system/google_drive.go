package system

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"nas-server/internal/storage"
)

func (h *Handler) HandleStartGoogleDriveConnect(w http.ResponseWriter, r *http.Request) {
	var req storage.GoogleDriveConnectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}

	result, err := storage.StartGoogleDriveOAuth(r, req)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "GOOGLE_DRIVE_CONNECT_FAILED", err.Error())
		return
	}

	w.WriteHeader(http.StatusCreated)
	writeJSON(w, result)
}

func (h *Handler) HandleGoogleDriveCallback(w http.ResponseWriter, r *http.Request) {
	result, redirectURL, err := storage.CompleteGoogleDriveOAuth(r.Context(), r.URL.Query().Get("code"), r.URL.Query().Get("state"))
	if err != nil {
		if redirectURL != "" {
			http.Redirect(w, r, appendRedirectQuery(redirectURL, map[string]string{
				"status":   "error",
				"provider": string(storage.ProviderGoogleDrive),
				"message":  err.Error(),
			}), http.StatusFound)
			return
		}
		writeAPIError(w, http.StatusBadRequest, "GOOGLE_DRIVE_CALLBACK_FAILED", err.Error())
		return
	}

	if redirectURL != "" && result != nil {
		http.Redirect(w, r, appendRedirectQuery(redirectURL, map[string]string{
			"status":    "success",
			"provider":  result.Provider,
			"storageId": result.StorageID,
		}), http.StatusFound)
		return
	}

	writeJSON(w, result)
}

func (h *Handler) HandleStartGoogleDriveConnectRedirect(w http.ResponseWriter, r *http.Request) {
	req := storage.GoogleDriveConnectRequest{
		Name:               strings.TrimSpace(r.URL.Query().Get("name")),
		RootPath:           strings.TrimSpace(r.URL.Query().Get("rootPath")),
		Scope:              strings.TrimSpace(r.URL.Query().Get("scope")),
		SuccessRedirectURL: strings.TrimSpace(r.URL.Query().Get("successRedirectURL")),
		FailureRedirectURL: strings.TrimSpace(r.URL.Query().Get("failureRedirectURL")),
	}

	result, err := storage.StartGoogleDriveOAuth(r, req)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "GOOGLE_DRIVE_CONNECT_FAILED", err.Error())
		return
	}

	http.Redirect(w, r, result.AuthURL, http.StatusFound)
}

func appendRedirectQuery(base string, values map[string]string) string {
	parsed, err := url.Parse(base)
	if err != nil {
		return base
	}
	query := parsed.Query()
	for key, value := range values {
		if value == "" {
			continue
		}
		query.Set(key, value)
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}
