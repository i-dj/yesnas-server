package audit

import (
	"net/http"
	"strconv"
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
	query := ListQuery{
		Page:        parsePositiveInt(r.URL.Query().Get("page"), 1),
		PageSize:    parsePositiveInt(r.URL.Query().Get("pageSize"), 20),
		Category:    r.URL.Query().Get("category"),
		Severity:    r.URL.Query().Get("severity"),
		Source:      r.URL.Query().Get("source"),
		Event:       r.URL.Query().Get("event"),
		ActorUserID: r.URL.Query().Get("actorUserId"),
		IPAddress:   r.URL.Query().Get("ipAddress"),
		Search:      r.URL.Query().Get("q"),
		From:        r.URL.Query().Get("from"),
		To:          r.URL.Query().Get("to"),
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("success")); raw != "" {
		value := raw == "1" || strings.EqualFold(raw, "true")
		query.Success = &value
	}
	result, err := List(query)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "AUDIT_LOG_LIST_FAILED", "Failed to list audit logs: "+err.Error())
		return
	}
	writeJSON(w, result)
}

func (h *Handler) HandleHeatmap(w http.ResponseWriter, r *http.Request) {
	result, err := Heatmap(r.URL.Query().Get("range"))
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "AUDIT_LOG_HEATMAP_FAILED", "Failed to build audit log heatmap: "+err.Error())
		return
	}
	writeJSON(w, result)
}

func parsePositiveInt(raw string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}
