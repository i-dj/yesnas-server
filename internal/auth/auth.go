package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"nas-server/database"
	"nas-server/internal/audit"
	"nas-server/internal/identity"
	"nas-server/internal/users"
	"nas-server/pkg/httpx"
	"nas-server/pkg/idgen"
)

const (
	sessionStatusActive  = "active"
	sessionStatusRevoked = "revoked"
	sessionTTL           = 30 * 24 * time.Hour
)

type Actor = identity.Actor

type Session struct {
	ID          string  `db:"id"`
	UserID      string  `db:"user_id"`
	TokenHash   string  `db:"token_hash"`
	Status      string  `db:"status"`
	IPAddress   string  `db:"ip_address"`
	UserAgent   string  `db:"user_agent"`
	CreatedAt   string  `db:"created_at"`
	UpdatedAt   string  `db:"updated_at"`
	ExpiresAt   *string `db:"expires_at"`
	LastSeenAt  *string `db:"last_seen_at"`
	LoggedOutAt *string `db:"logged_out_at"`
}

type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type LoginResponse struct {
	Token     string     `json:"token"`
	TokenType string     `json:"tokenType"`
	ExpiresAt string     `json:"expiresAt"`
	User      users.User `json:"user"`
}

type LogoutRequest struct {
	Token string `json:"token"`
}

type sessionWithUser struct {
	Session
	Username    string `db:"username"`
	DisplayName string `db:"display_name"`
	IsAdmin     bool   `db:"is_admin"`
	UserStatus  string `db:"user_status"`
}

type Handler struct{}

var (
	writeJSON     = httpx.WriteJSON
	writeAPIError = httpx.WriteAPIError
)

func NewHandler() *Handler {
	return &Handler{}
}

func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isPublicRequest(r) {
			next.ServeHTTP(w, r)
			return
		}
		token := ExtractToken(r)
		if token == "" {
			writeAPIError(w, http.StatusUnauthorized, "AUTH_REQUIRED", "Authentication is required")
			return
		}
		actor, _, err := LookupSession(token)
		if err != nil || actor == nil {
			writeAPIError(w, http.StatusUnauthorized, "AUTH_INVALID", "Invalid or expired session")
			return
		}
		r = r.WithContext(identity.WithActor(r.Context(), *actor))
		next.ServeHTTP(w, r)
	})
}

func isPublicRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	if r.Method == http.MethodOptions {
		return true
	}
	path := strings.TrimSpace(r.URL.Path)
	return r.Method == http.MethodPost && path == "/api/v1/auth/login"
}

func (h *Handler) HandleLogin(w http.ResponseWriter, r *http.Request) {
	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	user, err := users.Authenticate(req.Username, req.Password)
	if err != nil {
		audit.LoginFailed(r.Context(), req.Username, err.Error())
		writeAPIError(w, http.StatusUnauthorized, "LOGIN_FAILED", "Invalid username or password")
		return
	}
	token, expiresAt, err := CreateSession(user, r)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "LOGIN_FAILED", "Failed to create session: "+err.Error())
		return
	}
	audit.LoginSucceeded(r.Context(), audit.Actor{
		UserID:      user.ID,
		Username:    user.Username,
		DisplayName: user.DisplayName,
	}, user.ID, user.Username)
	writeJSON(w, LoginResponse{
		Token:     token,
		TokenType: "Bearer",
		ExpiresAt: expiresAt,
		User:      *user,
	})
}

func (h *Handler) HandleLogout(w http.ResponseWriter, r *http.Request) {
	token := ExtractToken(r)
	if token == "" && r.ContentLength > 0 {
		var req LogoutRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
			token = strings.TrimSpace(req.Token)
		}
	}
	if token == "" {
		writeAPIError(w, http.StatusBadRequest, "LOGOUT_TOKEN_REQUIRED", "Login token is required")
		return
	}
	actor, session, err := LookupSession(token)
	if err != nil {
		writeAPIError(w, http.StatusUnauthorized, "LOGOUT_FAILED", "Invalid or expired session")
		return
	}
	if err := RevokeSession(session.ID); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "LOGOUT_FAILED", "Failed to revoke session: "+err.Error())
		return
	}
	audit.LogoutSucceeded(r.Context(), audit.Actor{
		UserID:      actor.UserID,
		Username:    actor.Username,
		DisplayName: actor.DisplayName,
	}, session.ID, actor.Username)
	writeJSON(w, map[string]any{"loggedOut": true})
}

func CreateSession(user *users.User, r *http.Request) (string, string, error) {
	rawToken, hashedToken, err := newSessionToken()
	if err != nil {
		return "", "", err
	}
	now := time.Now().UTC()
	expiresAt := now.Add(sessionTTL).Format(time.RFC3339)
	ipAddress := ""
	userAgent := ""
	if r != nil {
		ipAddress = requestIP(r)
		userAgent = strings.TrimSpace(r.UserAgent())
	}
	_, err = database.DB.Exec(
		`INSERT INTO user_sessions (id, user_id, token_hash, status, ip_address, user_agent, created_at, updated_at, expires_at, last_seen_at, logged_out_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		idgen.New(),
		user.ID,
		hashedToken,
		sessionStatusActive,
		ipAddress,
		userAgent,
		now.Format(time.RFC3339),
		now.Format(time.RFC3339),
		expiresAt,
		now.Format(time.RFC3339),
		nil,
	)
	if err != nil {
		return "", "", fmt.Errorf("insert session: %w", err)
	}
	return rawToken, expiresAt, nil
}

func LookupSession(token string) (*Actor, *Session, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, nil, fmt.Errorf("session token is required")
	}
	var item sessionWithUser
	err := database.DB.Get(&item, `SELECT s.id, s.user_id, s.token_hash, s.status, s.ip_address, s.user_agent, s.created_at, s.updated_at, s.expires_at, s.last_seen_at, s.logged_out_at,
		u.username, COALESCE(u.display_name, '') AS display_name, COALESCE(u.is_admin, 0) AS is_admin, COALESCE(u.status, '') AS user_status
		FROM user_sessions s
		JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = ?`, hashToken(token))
	if err != nil {
		return nil, nil, err
	}
	if item.Status != sessionStatusActive {
		return nil, nil, fmt.Errorf("session is not active")
	}
	if strings.EqualFold(strings.TrimSpace(item.UserStatus), string(users.StatusDisabled)) {
		return nil, nil, fmt.Errorf("user is disabled")
	}
	if item.ExpiresAt != nil && strings.TrimSpace(*item.ExpiresAt) != "" {
		if expiresAt, err := time.Parse(time.RFC3339, *item.ExpiresAt); err == nil && time.Now().UTC().After(expiresAt) {
			return nil, nil, fmt.Errorf("session is expired")
		}
	}
	return &Actor{
		UserID:      item.UserID,
		Username:    item.Username,
		DisplayName: item.DisplayName,
		IsAdmin:     item.IsAdmin,
	}, &item.Session, nil
}

func RevokeSession(sessionID string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := database.DB.Exec(`UPDATE user_sessions SET status = ?, updated_at = ?, logged_out_at = ? WHERE id = ?`, sessionStatusRevoked, now, now, strings.TrimSpace(sessionID))
	return err
}

func ExtractToken(r *http.Request) string {
	if r == nil {
		return ""
	}
	authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
		return strings.TrimSpace(authHeader[7:])
	}
	if token := strings.TrimSpace(r.Header.Get("X-Auth-Token")); token != "" {
		return token
	}
	return ""
}

func newSessionToken() (string, string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("generate session token: %w", err)
	}
	raw := base64.RawURLEncoding.EncodeToString(buf)
	return raw, hashToken(raw), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}

func requestIP(r *http.Request) string {
	return strings.TrimSpace(firstNonEmpty(
		strings.TrimSpace(r.Header.Get("X-Forwarded-For")),
		strings.TrimSpace(r.Header.Get("X-Real-IP")),
		strings.TrimSpace(r.RemoteAddr),
	))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			if host, _, err := net.SplitHostPort(value); err == nil {
				return host
			}
			if strings.Contains(value, ",") {
				return strings.TrimSpace(strings.Split(value, ",")[0])
			}
			return value
		}
	}
	return ""
}
