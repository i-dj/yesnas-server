package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"nas-server/database"
	"nas-server/internal/identity"
	"nas-server/pkg/idgen"
)

type contextKey string

const metadataContextKey contextKey = "audit.metadata"

type Metadata struct {
	IPAddress string
	UserAgent string
	Method    string
	Path      string
	Source    string
}

func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		meta := Metadata{
			IPAddress: requestIP(r),
			UserAgent: strings.TrimSpace(r.UserAgent()),
			Method:    r.Method,
			Path:      r.URL.Path,
			Source:    "api",
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), metadataContextKey, meta)))
	})
}

func metadataFromContext(ctx context.Context) Metadata {
	if ctx == nil {
		return Metadata{Source: "api"}
	}
	meta, ok := ctx.Value(metadataContextKey).(Metadata)
	if !ok {
		return Metadata{Source: "api"}
	}
	if strings.TrimSpace(meta.Source) == "" {
		meta.Source = "api"
	}
	return meta
}

func WithSource(ctx context.Context, source string) context.Context {
	meta := metadataFromContext(ctx)
	meta.Source = strings.TrimSpace(source)
	if meta.Source == "" {
		meta.Source = "api"
	}
	return context.WithValue(ctx, metadataContextKey, meta)
}

func actorFromContext(ctx context.Context) Actor {
	current := identity.ActorFromContext(ctx)
	if current == nil {
		return Actor{}
	}
	return Actor{
		UserID:      current.UserID,
		Username:    current.Username,
		DisplayName: current.DisplayName,
	}
}

func Record(entry Entry) error {
	if strings.TrimSpace(entry.Event) == "" {
		return fmt.Errorf("audit event is required")
	}
	if strings.TrimSpace(entry.Action) == "" {
		return fmt.Errorf("audit action is required")
	}
	category := strings.TrimSpace(string(entry.Category))
	if category == "" {
		category = string(CategoryUser)
	}
	severity := strings.TrimSpace(string(entry.Severity))
	if severity == "" {
		if entry.Success {
			severity = string(SeverityInfo)
		} else {
			severity = string(SeverityError)
		}
	}
	source := strings.TrimSpace(entry.Source)
	if source == "" {
		source = "api"
	}
	detailsJSON := ""
	if entry.Details != nil {
		payload, err := json.Marshal(entry.Details)
		if err != nil {
			return fmt.Errorf("marshal audit details: %w", err)
		}
		detailsJSON = string(payload)
	}
	keyData := entry.KeyData
	if keyData == nil {
		keyData = defaultKeyData(entry)
	}
	keyDataJSON := ""
	if keyData != nil {
		payload, err := json.Marshal(keyData)
		if err != nil {
			return fmt.Errorf("marshal audit key data: %w", err)
		}
		keyDataJSON = string(payload)
	}
	keyword := strings.TrimSpace(entry.Keyword)
	if keyword == "" {
		keyword = defaultKeyword(entry)
	}
	content := strings.TrimSpace(entry.Content)
	if content == "" {
		content = defaultContent(entry, keyword)
	}
	occurredAt := strings.TrimSpace(entry.OccurredAt)
	if occurredAt == "" {
		occurredAt = time.Now().UTC().Format(time.RFC3339)
	}
	_, err := database.DB.Exec(
		`INSERT INTO audit_logs (id, category, severity, source, event, action, success, actor_user_id, actor_username, actor_display_name, ip_address, ip_type, country_code, country, city, user_agent, method, path, resource_type, resource_id, resource_name, keyword, content, key_data_json, message, details_json, occurred_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		idgen.New(),
		category,
		severity,
		source,
		strings.TrimSpace(entry.Event),
		strings.TrimSpace(entry.Action),
		boolToInt(entry.Success),
		strings.TrimSpace(entry.Actor.UserID),
		strings.TrimSpace(entry.Actor.Username),
		strings.TrimSpace(entry.Actor.DisplayName),
		strings.TrimSpace(entry.IPAddress),
		strings.TrimSpace(entry.IPType),
		strings.TrimSpace(entry.CountryCode),
		strings.TrimSpace(entry.Country),
		strings.TrimSpace(entry.City),
		strings.TrimSpace(entry.UserAgent),
		strings.TrimSpace(entry.Method),
		strings.TrimSpace(entry.Path),
		strings.TrimSpace(entry.ResourceType),
		strings.TrimSpace(entry.ResourceID),
		strings.TrimSpace(entry.ResourceName),
		keyword,
		content,
		keyDataJSON,
		strings.TrimSpace(entry.Message),
		detailsJSON,
		occurredAt,
	)
	return err
}

func MustRecord(entry Entry) {
	if err := Record(entry); err != nil {
		log.Printf("[AUDIT] record failed event=%s action=%s err=%v", entry.Event, entry.Action, err)
	}
}

func Emit(ctx context.Context, entry Entry) {
	meta := metadataFromContext(ctx)
	if entry.Actor == (Actor{}) {
		entry.Actor = actorFromContext(ctx)
	}
	if strings.TrimSpace(entry.IPAddress) == "" {
		entry.IPAddress = meta.IPAddress
	}
	if strings.TrimSpace(entry.IPType) == "" || (strings.TrimSpace(entry.CountryCode) == "" && strings.TrimSpace(entry.Country) == "" && strings.TrimSpace(entry.City) == "") {
		location := lookupGeoIP(entry.IPAddress)
		if strings.TrimSpace(entry.IPType) == "" {
			entry.IPType = location.IPType
		}
		entry.CountryCode = location.CountryCode
		entry.Country = location.Country
		entry.City = location.City
	}
	if strings.TrimSpace(entry.UserAgent) == "" {
		entry.UserAgent = meta.UserAgent
	}
	if strings.TrimSpace(entry.Method) == "" {
		entry.Method = meta.Method
	}
	if strings.TrimSpace(entry.Path) == "" {
		entry.Path = meta.Path
	}
	if strings.TrimSpace(entry.Source) == "" {
		entry.Source = meta.Source
	}
	MustRecord(entry)
}

func UserAction(ctx context.Context, event string, action string, success bool, resourceType string, resourceID string, resourceName string, message string, details any) {
	Emit(ctx, Entry{
		Category:     CategoryUser,
		Event:        event,
		Action:       action,
		Success:      success,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		ResourceName: resourceName,
		Message:      message,
		Details:      details,
	})
}

func SystemAction(ctx context.Context, event string, action string, success bool, resourceType string, resourceID string, resourceName string, message string, details any) {
	Emit(WithSource(ctx, "system"), Entry{
		Category:     CategorySystem,
		Event:        event,
		Action:       action,
		Success:      success,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		ResourceName: resourceName,
		Message:      message,
		Details:      details,
	})
}

func LoginFailed(ctx context.Context, username string, message string) {
	Emit(ctx, Entry{
		Category: CategoryUser,
		Severity: SeverityWarn,
		Event:    "login_failed",
		Action:   "login",
		Success:  false,
		Message:  message,
		Details:  map[string]any{"username": strings.TrimSpace(username)},
	})
}

func LoginSucceeded(ctx context.Context, actor Actor, userID string, username string) {
	Emit(ctx, Entry{
		Category:     CategoryUser,
		Event:        "login_success",
		Action:       "login",
		Success:      true,
		Actor:        actor,
		ResourceType: "user",
		ResourceID:   strings.TrimSpace(userID),
		ResourceName: strings.TrimSpace(username),
		Message:      "User login succeeded",
	})
}

func LogoutSucceeded(ctx context.Context, actor Actor, sessionID string, username string) {
	Emit(ctx, Entry{
		Category:     CategoryUser,
		Event:        "logout",
		Action:       "logout",
		Success:      true,
		Actor:        actor,
		ResourceType: "session",
		ResourceID:   strings.TrimSpace(sessionID),
		ResourceName: strings.TrimSpace(username),
		Message:      "User logged out",
	})
}

func UserDeleteBlocked(ctx context.Context, userID string, username string, message string) {
	Emit(ctx, Entry{
		Category:     CategoryUser,
		Severity:     SeverityWarn,
		Event:        "user_delete_blocked",
		Action:       "delete",
		Success:      false,
		ResourceType: "user",
		ResourceID:   strings.TrimSpace(userID),
		ResourceName: strings.TrimSpace(username),
		Message:      strings.TrimSpace(message),
	})
}

func AutoSnapshotScheduled(ctx context.Context, poolID string, poolName string, details any) {
	SystemAction(ctx, "auto_snapshot_scheduled", "schedule", true, "storage_pool", poolID, poolName, "Automatic snapshot job scheduled", details)
}

func AutoSnapshotFailed(ctx context.Context, resourceType string, resourceID string, resourceName string, message string, details any) {
	Emit(WithSource(ctx, "system"), Entry{
		Category:     CategorySystem,
		Severity:     SeverityError,
		Event:        "auto_snapshot_failed",
		Action:       "run",
		Success:      false,
		ResourceType: strings.TrimSpace(resourceType),
		ResourceID:   strings.TrimSpace(resourceID),
		ResourceName: strings.TrimSpace(resourceName),
		Message:      strings.TrimSpace(message),
		Details:      details,
	})
}

func AutoSnapshotCompleted(ctx context.Context, poolID string, poolName string, details any) {
	SystemAction(ctx, "auto_snapshot_completed", "run", true, "storage_pool", poolID, poolName, "Automatic snapshot completed", details)
}

func BackfillLegacyLogs() error {
	var items []Log
	if err := database.DB.Select(&items, `SELECT id, event, action, actor_username, ip_address, ip_type, country_code, country, city, resource_type, resource_id, resource_name, keyword, content, key_data_json, message
		FROM audit_logs
		WHERE COALESCE(keyword, '') = ''
			OR COALESCE(content, '') = ''
			OR COALESCE(key_data_json, '') = ''
			OR COALESCE(ip_type, '') = ''`); err != nil {
		return err
	}

	for _, item := range items {
		entry := Entry{
			Event:        item.Event,
			Action:       item.Action,
			Actor:        Actor{Username: item.ActorUsername},
			ResourceType: item.ResourceType,
			ResourceID:   item.ResourceID,
			ResourceName: item.ResourceName,
			Message:      item.Message,
		}
		keyword := strings.TrimSpace(item.Keyword)
		if keyword == "" {
			keyword = defaultKeyword(entry)
		}
		content := strings.TrimSpace(item.Content)
		if content == "" {
			content = defaultContent(entry, keyword)
		}
		keyDataJSON := strings.TrimSpace(item.KeyDataJSON)
		if keyDataJSON == "" {
			if keyData := defaultKeyData(entry); keyData != nil {
				payload, err := json.Marshal(keyData)
				if err != nil {
					return fmt.Errorf("marshal legacy audit key data: %w", err)
				}
				keyDataJSON = string(payload)
			}
		}
		countryCode := strings.TrimSpace(item.CountryCode)
		country := strings.TrimSpace(item.Country)
		city := strings.TrimSpace(item.City)
		ipType := strings.TrimSpace(item.IPType)
		if ipType == "" || (countryCode == "" && country == "" && city == "") {
			location := lookupGeoIP(item.IPAddress)
			if ipType == "" {
				ipType = location.IPType
			}
			countryCode = location.CountryCode
			country = location.Country
			city = location.City
		}
		if _, err := database.DB.Exec(
			`UPDATE audit_logs SET keyword = ?, content = ?, key_data_json = ?, ip_type = ?, country_code = ?, country = ?, city = ? WHERE id = ?`,
			keyword,
			content,
			keyDataJSON,
			ipType,
			countryCode,
			country,
			city,
			item.ID,
		); err != nil {
			return err
		}
	}
	return nil
}

func List(query ListQuery) (*ListResponse, error) {
	page := query.Page
	if page <= 0 {
		page = 1
	}
	pageSize := query.PageSize
	if pageSize <= 0 {
		pageSize = 20
	}
	if pageSize > 200 {
		pageSize = 200
	}

	whereParts := []string{"1=1"}
	args := []any{}
	if value := strings.TrimSpace(query.Category); value != "" && value != "all" {
		whereParts = append(whereParts, "category = ?")
		args = append(args, value)
	}
	if value := strings.TrimSpace(query.Severity); value != "" && value != "all" {
		whereParts = append(whereParts, "severity = ?")
		args = append(args, value)
	}
	if value := strings.TrimSpace(query.Source); value != "" && value != "all" {
		whereParts = append(whereParts, "source = ?")
		args = append(args, value)
	}
	if value := strings.TrimSpace(query.Event); value != "" {
		whereParts = append(whereParts, "event = ?")
		args = append(args, value)
	}
	if value := strings.TrimSpace(query.ActorUserID); value != "" {
		whereParts = append(whereParts, "actor_user_id = ?")
		args = append(args, value)
	}
	if value := strings.TrimSpace(query.IPAddress); value != "" {
		whereParts = append(whereParts, "ip_address LIKE ?")
		args = append(args, "%"+value+"%")
	}
	if value := strings.TrimSpace(query.Country); value != "" {
		whereParts = append(whereParts, "(country_code = ? OR country LIKE ?)")
		args = append(args, strings.ToUpper(value), "%"+value+"%")
	}
	if value := strings.TrimSpace(query.City); value != "" {
		whereParts = append(whereParts, "city LIKE ?")
		args = append(args, "%"+value+"%")
	}
	if value := strings.TrimSpace(query.Keyword); value != "" {
		whereParts = append(whereParts, "keyword LIKE ?")
		args = append(args, "%"+value+"%")
	}
	if query.Success != nil {
		whereParts = append(whereParts, "success = ?")
		args = append(args, boolToInt(*query.Success))
	}
	if value := strings.TrimSpace(query.Search); value != "" {
		pattern := "%" + value + "%"
		whereParts = append(whereParts, "(event LIKE ? OR action LIKE ? OR actor_username LIKE ? OR actor_display_name LIKE ? OR resource_name LIKE ? OR keyword LIKE ? OR content LIKE ? OR message LIKE ? OR key_data_json LIKE ? OR ip_address LIKE ? OR country LIKE ? OR city LIKE ?)")
		args = append(args, pattern, pattern, pattern, pattern, pattern, pattern, pattern, pattern, pattern, pattern, pattern, pattern)
	}
	if value := strings.TrimSpace(query.From); value != "" {
		whereParts = append(whereParts, "occurred_at >= ?")
		args = append(args, value)
	}
	if value := strings.TrimSpace(query.To); value != "" {
		whereParts = append(whereParts, "occurred_at <= ?")
		args = append(args, value)
	}

	whereSQL := " WHERE " + strings.Join(whereParts, " AND ")
	var total int
	if err := database.DB.Get(&total, `SELECT COUNT(1) FROM audit_logs`+whereSQL, args...); err != nil {
		return nil, err
	}

	offset := (page - 1) * pageSize
	listArgs := append(append([]any{}, args...), pageSize, offset)
	var items []Log
	if err := database.DB.Select(&items, `SELECT id, category, severity, source, event, action, success, actor_user_id, actor_username, actor_display_name, ip_address, ip_type, country_code, country, city, user_agent, method, path, resource_type, resource_id, resource_name, keyword, content, key_data_json, message, details_json, occurred_at
		FROM audit_logs`+whereSQL+` ORDER BY occurred_at DESC LIMIT ? OFFSET ?`, listArgs...); err != nil {
		return nil, err
	}
	for i := range items {
		normalizeLog(&items[i])
	}
	totalPages := 0
	if total > 0 {
		totalPages = (total + pageSize - 1) / pageSize
	}
	return &ListResponse{
		Items: items,
		Pagination: Pagination{
			Page:       page,
			PageSize:   pageSize,
			Total:      total,
			TotalPages: totalPages,
		},
	}, nil
}

func Heatmap(rangeKey string) (*HeatmapResponse, error) {
	now := time.Now().UTC()
	start, bucketFormat, bucket := heatmapRange(now, rangeKey)
	rows := []struct {
		Time  string `db:"bucket_time"`
		Count int    `db:"count"`
	}{}
	query := `SELECT strftime(?, occurred_at) AS bucket_time, COUNT(1) AS count
		FROM audit_logs
		WHERE occurred_at >= ?
		GROUP BY bucket_time
		ORDER BY bucket_time ASC`
	if err := database.DB.Select(&rows, query, bucketFormat, start.UTC().Format(time.RFC3339)); err != nil {
		return nil, err
	}
	items := make([]HeatmapBucket, 0, len(rows))
	for _, row := range rows {
		items = append(items, HeatmapBucket{Time: row.Time, Count: row.Count})
	}
	return &HeatmapResponse{
		Range:   normalizeRangeKey(rangeKey),
		Bucket:  bucket,
		Buckets: items,
	}, nil
}

func heatmapRange(now time.Time, rangeKey string) (time.Time, string, string) {
	switch normalizeRangeKey(rangeKey) {
	case "24h":
		return now.Add(-24 * time.Hour), "%Y-%m-%dT%H:00:00", "hour"
	case "7d":
		return now.AddDate(0, 0, -7), "%Y-%m-%d", "day"
	case "30d":
		return now.AddDate(0, 0, -30), "%Y-%m-%d", "day"
	case "90d":
		return now.AddDate(0, 0, -90), "%Y-%m-%d", "day"
	case "1y":
		return now.AddDate(-1, 0, 0), "%Y-%m", "month"
	default:
		return now.AddDate(0, 0, -30), "%Y-%m-%d", "day"
	}
}

func normalizeRangeKey(value string) string {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case "24h", "7d", "30d", "90d", "1y":
		return strings.TrimSpace(strings.ToLower(value))
	default:
		return "30d"
	}
}

func normalizeLog(item *Log) {
	if strings.TrimSpace(item.KeyDataJSON) != "" {
		var keyData any
		if err := json.Unmarshal([]byte(item.KeyDataJSON), &keyData); err == nil {
			item.KeyData = keyData
		}
	}
	if strings.TrimSpace(item.DetailsJSON) == "" {
		return
	}
	var details any
	if err := json.Unmarshal([]byte(item.DetailsJSON), &details); err == nil {
		item.Details = details
	}
}

func defaultKeyword(entry Entry) string {
	event := strings.TrimSpace(entry.Event)
	if message := strings.TrimSpace(entry.Message); message != "" && !strings.HasSuffix(event, "_failed") {
		return message
	}
	words := strings.Fields(strings.ReplaceAll(event, "_", " "))
	for i := range words {
		if i == 0 && words[i] != "" {
			words[i] = strings.ToUpper(words[i][:1]) + words[i][1:]
		}
	}
	return strings.Join(words, " ")
}

func defaultContent(entry Entry, keyword string) string {
	parts := make([]string, 0, 3)
	message := strings.TrimSpace(entry.Message)
	if message != "" {
		parts = append(parts, message)
	} else if keyword != "" {
		parts = append(parts, keyword)
	}
	if name := strings.TrimSpace(entry.ResourceName); name != "" {
		parts = append(parts, name)
	}
	if actor := strings.TrimSpace(entry.Actor.Username); actor != "" {
		parts = append(parts, "by "+actor)
	}
	return strings.Join(parts, " · ")
}

func defaultKeyData(entry Entry) any {
	data := map[string]any{}
	if value := strings.TrimSpace(entry.ResourceType); value != "" {
		data["resourceType"] = value
	}
	if value := strings.TrimSpace(entry.ResourceID); value != "" {
		data["resourceId"] = value
	}
	if value := strings.TrimSpace(entry.ResourceName); value != "" {
		data["resourceName"] = value
	}
	if value := strings.TrimSpace(entry.Action); value != "" {
		data["action"] = value
	}
	if len(data) == 0 {
		return nil
	}
	return data
}

func requestIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); forwarded != "" {
		parts := strings.Split(forwarded, ",")
		if len(parts) > 0 {
			return strings.TrimSpace(parts[0])
		}
	}
	if forwarded := strings.TrimSpace(r.Header.Get("X-Real-IP")); forwarded != "" {
		return forwarded
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil {
		return host
	}
	return strings.TrimSpace(r.RemoteAddr)
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
