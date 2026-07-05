package storage

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"nas-server/database"
	"nas-server/pkg/idgen"
)

const (
	defaultOAuthBrokerBaseURL            = "https://oauth.yesnas.com"
	defaultOAuthBrokerRegistrationSecret = "affc5ba6555a784cf0accf2ba8788e3cae4699a15b167ad4d4bb2fd892516bed"
	oauthBrokerGoogleProvider            = "google"
)

type OAuthBrokerStatusResponse struct {
	SessionID string         `json:"sessionId"`
	Provider  string         `json:"provider"`
	Status    string         `json:"status"`
	ExpiresAt string         `json:"expiresAt"`
	Error     map[string]any `json:"error,omitempty"`
}

type CloudConnectCompleteRequest struct {
	SessionID string `json:"sessionId"`
}

type oauthBrokerConnection struct {
	ID            string  `db:"id"`
	BrokerBaseURL string  `db:"broker_base_url"`
	DeviceID      string  `db:"device_id"`
	DeviceName    string  `db:"device_name"`
	PublicKeyPEM  string  `db:"public_key_pem"`
	PrivateKeyPEM string  `db:"private_key_pem"`
	RegisteredAt  *string `db:"registered_at"`
	LastAuthAt    *string `db:"last_auth_at"`
}

type oauthBrokerSession struct {
	SessionID     string  `db:"session_id"`
	BrokerBaseURL string  `db:"broker_base_url"`
	Provider      string  `db:"provider"`
	StorageID     string  `db:"storage_id"`
	Name          string  `db:"name"`
	RootPath      string  `db:"root_path"`
	Status        string  `db:"status"`
	AuthorizeURL  string  `db:"authorize_url"`
	ExpiresAt     *string `db:"expires_at"`
}

type oauthBrokerStorageToken struct {
	ID                 string `db:"id"`
	StorageID          string `db:"storage_id"`
	BrokerBaseURL      string `db:"broker_base_url"`
	Provider           string `db:"provider"`
	BrokerRefreshToken string `db:"broker_refresh_token"`
	RcloneTokenURL     string `db:"rclone_token_url"`
	RcloneConfigJSON   string `db:"rclone_config_json"`
}

type brokerJWTResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int64  `json:"expires_in"`
	ExpiresAt   string `json:"expires_at"`
}

type brokerCreateSessionResponse struct {
	SessionID    string `json:"session_id"`
	Provider     string `json:"provider"`
	Status       string `json:"status"`
	AuthorizeURL string `json:"authorize_url"`
	ExpiresAt    string `json:"expires_at"`
}

type brokerStatusResponse struct {
	SessionID string         `json:"session_id"`
	Provider  string         `json:"provider"`
	Status    string         `json:"status"`
	ExpiresAt string         `json:"expires_at"`
	Error     map[string]any `json:"error,omitempty"`
}

type brokerExchangeResponse struct {
	Provider string             `json:"provider"`
	Rclone   brokerRcloneConfig `json:"rclone"`
}

type brokerRcloneConfig struct {
	Type         string            `json:"type"`
	ClientID     string            `json:"client_id"`
	ClientSecret string            `json:"client_secret"`
	Scope        string            `json:"scope"`
	TokenURL     string            `json:"token_url"`
	Token        brokerRcloneToken `json:"token"`
	Extra        map[string]any    `json:"-"`
}

type brokerRcloneToken struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	Expiry       string `json:"expiry"`
	ExpiresAt    string `json:"expires_at"`
}

func StartCloudOAuthViaBroker(ctx context.Context, providerSlug string, req CloudConnectRequest) (*CloudConnectResponse, error) {
	providerInfo, err := oauthBrokerProviderInfo(providerSlug)
	if err != nil {
		return nil, err
	}
	brokerBaseURL := oauthBrokerBaseURL()
	client := newOAuthBrokerClient(brokerBaseURL)
	if _, err := client.ensureConnection(ctx); err != nil {
		return nil, err
	}
	jwt, err := client.issueJWT(ctx)
	if err != nil {
		return nil, err
	}
	session, err := client.createSession(ctx, jwt.AccessToken, providerInfo.BrokerProvider)
	if err != nil {
		return nil, err
	}
	if err := saveOAuthBrokerSession(oauthBrokerSession{
		SessionID:     session.SessionID,
		BrokerBaseURL: brokerBaseURL,
		Provider:      providerInfo.BrokerProvider,
		StorageID:     strings.TrimSpace(req.StorageID),
		Name:          strings.TrimSpace(req.Name),
		RootPath:      normalizeGoogleDriveRootPath(req.RootPath),
		Status:        session.Status,
		AuthorizeURL:  session.AuthorizeURL,
		ExpiresAt:     stringPtr(session.ExpiresAt),
	}); err != nil {
		return nil, fmt.Errorf("save oauth broker session: %w", err)
	}
	return &CloudConnectResponse{
		Provider:    providerInfo.StorageProvider,
		AuthURL:     session.AuthorizeURL,
		State:       session.SessionID,
		RedirectURL: session.AuthorizeURL,
		ExpiresAt:   session.ExpiresAt,
	}, nil
}

func GetCloudOAuthBrokerStatus(ctx context.Context, sessionID string) (*OAuthBrokerStatusResponse, error) {
	local, err := getOAuthBrokerSession(sessionID)
	if err != nil {
		return nil, err
	}
	client := newOAuthBrokerClient(local.BrokerBaseURL)
	jwt, err := client.issueJWT(ctx)
	if err != nil {
		return nil, err
	}
	status, err := client.status(ctx, jwt.AccessToken, sessionID)
	if err != nil {
		return nil, err
	}
	_ = updateOAuthBrokerSessionStatus(sessionID, status.Status)
	return &OAuthBrokerStatusResponse{
		SessionID: status.SessionID,
		Provider:  status.Provider,
		Status:    status.Status,
		ExpiresAt: status.ExpiresAt,
		Error:     status.Error,
	}, nil
}

func CompleteCloudOAuthViaBroker(ctx context.Context, providerSlug string, sessionID string) (*CloudConnectCompleteResponse, error) {
	providerInfo, err := oauthBrokerProviderInfo(providerSlug)
	if err != nil {
		return nil, err
	}
	local, err := getOAuthBrokerSession(sessionID)
	if err != nil {
		return nil, err
	}
	if local.Provider != providerInfo.BrokerProvider {
		return nil, fmt.Errorf("oauth session provider mismatch")
	}
	client := newOAuthBrokerClient(local.BrokerBaseURL)
	jwt, err := client.issueJWT(ctx)
	if err != nil {
		return nil, err
	}
	status, err := client.status(ctx, jwt.AccessToken, sessionID)
	if err != nil {
		return nil, err
	}
	_ = updateOAuthBrokerSessionStatus(sessionID, status.Status)
	if status.Status != "success" {
		return nil, fmt.Errorf("oauth session is %s", status.Status)
	}
	exchanged, err := client.exchange(ctx, jwt.AccessToken, sessionID)
	if err != nil {
		return nil, err
	}
	storageID, item, token, err := createOrReconnectCloudStorageFromBroker(ctx, providerInfo, *local, exchanged)
	if err != nil {
		return nil, err
	}
	if err := saveOAuthBrokerStorageToken(storageID, local.BrokerBaseURL, exchanged); err != nil {
		return nil, err
	}
	if err := EnsureGoogleDriveMounted(ctx, &item, token); err != nil {
		return nil, err
	}
	warnings := make([]string, 0)
	if _, err := RefreshGoogleDriveUsage(ctx, &item); err != nil {
		warnings = append(warnings, "failed to refresh cloud storage usage: "+err.Error())
	}
	_ = updateOAuthBrokerSessionStatus(sessionID, "used")
	return &CloudConnectCompleteResponse{
		Connected:        true,
		Provider:         providerInfo.StorageProvider,
		StorageID:        storageID,
		Storage:          item,
		RcloneRemoteName: cloudRemoteName(providerInfo.StorageProvider, storageID),
		Warnings:         warnings,
	}, nil
}

func createOrReconnectCloudStorageFromBroker(ctx context.Context, providerInfo oauthBrokerProvider, session oauthBrokerSession, exchanged *brokerExchangeResponse) (string, Storage, *Token, error) {
	storageID := strings.TrimSpace(session.StorageID)
	rootPath := normalizeGoogleDriveRootPath(session.RootPath)
	name := strings.TrimSpace(session.Name)
	if name == "" {
		name = providerInfo.DefaultName
	}
	token := tokenFromBrokerExchange(storageID, exchanged)
	var item Storage
	if storageID == "" {
		storageID = idgen.New()
		token.StorageID = storageID
		item = Storage{
			ID:        storageID,
			Name:      name,
			Location:  "cloud",
			MountPath: cloudMountPath(providerInfo.StorageProvider, storageID),
			Type:      Cloud,
			Provider:  providerInfo.StorageProvider,
			URL:       providerInfo.APIURL,
			RootPath:  rootPath,
			Status:    StatusOnline,
			ExtraConfig: BuildExtraConfig(map[string]any{
				"backend":          exchanged.Rclone.Type,
				"provider":         providerInfo.StorageProvider,
				"oauthBroker":      true,
				"brokerProvider":   exchanged.Provider,
				"brokerBaseURL":    session.BrokerBaseURL,
				"rcloneRemoteName": cloudRemoteName(providerInfo.StorageProvider, storageID),
				"scope":            exchanged.Rclone.Scope,
				"rootPath":         rootPath,
			}),
		}
		if providerInfo.StorageProvider == string(ProviderGoogleDrive) {
			if email, err := fetchGoogleDriveAccountEmail(ctx, token.AccessToken); err == nil {
				item.Username = email
			}
		}
		if providerInfo.StorageProvider != string(ProviderGoogleDrive) && item.Username == "" {
			item.Username = exchanged.Provider
		}
		if _, err := Add(item); err != nil {
			return "", Storage{}, nil, fmt.Errorf("create %s storage: %w", providerInfo.DefaultName, err)
		}
	} else {
		loaded, err := Get(storageID)
		if err != nil {
			return "", Storage{}, nil, fmt.Errorf("load %s storage: %w", providerInfo.DefaultName, err)
		}
		if loaded == nil || loaded.Provider != providerInfo.StorageProvider {
			return "", Storage{}, nil, fmt.Errorf("%s storage not found", providerInfo.DefaultName)
		}
		item = *loaded
		if item.RootPath == "" {
			item.RootPath = rootPath
		}
		if item.MountPath == "" {
			item.MountPath = cloudMountPath(providerInfo.StorageProvider, storageID)
		}
		if providerInfo.StorageProvider == string(ProviderGoogleDrive) {
			if email, err := fetchGoogleDriveAccountEmail(ctx, token.AccessToken); err == nil {
				item.Username = email
				_ = UpdateIdentity(storageID, email)
			}
		}
		item.Status = StatusOnline
		item.ExtraConfig = BuildExtraConfig(map[string]any{
			"backend":          exchanged.Rclone.Type,
			"provider":         providerInfo.StorageProvider,
			"oauthBroker":      true,
			"brokerProvider":   exchanged.Provider,
			"brokerBaseURL":    session.BrokerBaseURL,
			"rcloneRemoteName": cloudRemoteName(providerInfo.StorageProvider, storageID),
			"scope":            exchanged.Rclone.Scope,
			"rootPath":         item.RootPath,
		})
		if err := UpdateRuntime(storageID, item.MountPath, item.Status, item.TotalSize, item.FreeSize, item.ExtraConfig); err != nil {
			return "", Storage{}, nil, fmt.Errorf("update %s storage: %w", providerInfo.DefaultName, err)
		}
	}
	if _, err := UpsertToken(*token); err != nil {
		return "", Storage{}, nil, fmt.Errorf("save %s broker token: %w", providerInfo.DefaultName, err)
	}
	return storageID, item, token, nil
}

type oauthBrokerProvider struct {
	Slug            string
	BrokerProvider  string
	StorageProvider string
	DefaultName     string
	RemotePrefix    string
	APIURL          string
}

func oauthBrokerProviderInfo(providerSlug string) (oauthBrokerProvider, error) {
	switch normalizeOAuthBrokerProviderSlug(providerSlug) {
	case "google", "google-drive", "google_drive", "gdrive":
		return oauthBrokerProvider{
			Slug:            "google-drive",
			BrokerProvider:  "google",
			StorageProvider: string(ProviderGoogleDrive),
			DefaultName:     "Google Drive",
			RemotePrefix:    "gdrive",
			APIURL:          "https://www.googleapis.com/drive/v3",
		}, nil
	case "onedrive", "one-drive", "one_drive":
		return oauthBrokerProvider{
			Slug:            "onedrive",
			BrokerProvider:  "onedrive",
			StorageProvider: string(ProviderOneDrive),
			DefaultName:     "OneDrive",
			RemotePrefix:    "onedrive",
			APIURL:          "https://graph.microsoft.com/v1.0/me/drive",
		}, nil
	case "dropbox":
		return oauthBrokerProvider{
			Slug:            "dropbox",
			BrokerProvider:  "dropbox",
			StorageProvider: string(ProviderDropbox),
			DefaultName:     "Dropbox",
			RemotePrefix:    "dropbox",
			APIURL:          "https://api.dropboxapi.com/2",
		}, nil
	default:
		return oauthBrokerProvider{}, fmt.Errorf("unsupported oauth provider %q", providerSlug)
	}
}

func normalizeOAuthBrokerProviderSlug(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func cloudRemoteName(provider string, storageID string) string {
	info, err := oauthBrokerProviderInfo(provider)
	if err != nil {
		return "cloud_" + storageID
	}
	return info.RemotePrefix + "_" + storageID
}

func cloudMountPath(provider string, storageID string) string {
	return filepath.Join(defaultGoogleDriveMountRoot, cloudRemoteName(provider, storageID))
}

func tokenFromBrokerExchange(storageID string, exchanged *brokerExchangeResponse) *Token {
	rcloneToken := exchanged.Rclone.Token
	expiry := firstNonEmpty(rcloneToken.Expiry, rcloneToken.ExpiresAt)
	raw, _ := json.Marshal(exchanged.Rclone)
	return &Token{
		StorageID:    storageID,
		TokenType:    firstNonEmpty(rcloneToken.TokenType, "Bearer"),
		AccessToken:  strings.TrimSpace(rcloneToken.AccessToken),
		RefreshToken: strings.TrimSpace(rcloneToken.RefreshToken),
		Expiry:       stringPtr(expiry),
		Scope:        strings.TrimSpace(exchanged.Rclone.Scope),
		RawJSON:      string(raw),
	}
}

type oauthBrokerClient struct {
	baseURL string
	http    *http.Client
}

func newOAuthBrokerClient(baseURL string) oauthBrokerClient {
	return oauthBrokerClient{
		baseURL: strings.TrimRight(firstNonEmpty(baseURL, oauthBrokerBaseURL()), "/"),
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

func (c oauthBrokerClient) ensureConnection(ctx context.Context) (*oauthBrokerConnection, error) {
	existing, err := getOAuthBrokerConnection(c.baseURL)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		if existing.RegisteredAt != nil && strings.TrimSpace(*existing.RegisteredAt) != "" {
			return existing, nil
		}
		if err := c.registerDevice(ctx); err != nil {
			return nil, err
		}
		return getOAuthBrokerConnection(c.baseURL)
	}
	conn, err := newOAuthBrokerConnection(c.baseURL)
	if err != nil {
		return nil, err
	}
	if err := saveOAuthBrokerConnection(*conn); err != nil {
		return nil, err
	}
	if err := c.registerDevice(ctx); err != nil {
		return nil, err
	}
	return getOAuthBrokerConnection(c.baseURL)
}

func (c oauthBrokerClient) registerDevice(ctx context.Context) error {
	conn, err := getOAuthBrokerConnection(c.baseURL)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]any{
		"name":           conn.DeviceName,
		"public_key_pem": conn.PublicKeyPEM,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.baseURL+"/api/devices/"+url.PathEscape(conn.DeviceID), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if secret := oauthBrokerRegistrationSecret(); secret != "" {
		req.Header.Set("X-Registration-Secret", secret)
	}
	if err := c.doJSON(req, nil); err != nil {
		return fmt.Errorf("register oauth broker device: %w", err)
	}
	_, err = database.DB.Exec(`UPDATE oauth_broker_connection SET registered_at = ?, updated_at = ? WHERE broker_base_url = ?`, time.Now().UTC(), time.Now().UTC(), c.baseURL)
	return err
}

func (c oauthBrokerClient) issueJWT(ctx context.Context) (*brokerJWTResponse, error) {
	conn, err := getOAuthBrokerConnection(c.baseURL)
	if err != nil {
		return nil, err
	}
	nonce := idgen.New()
	timestamp := time.Now().Unix()
	payload := []byte(fmt.Sprintf("%s\n%s\n%d", conn.DeviceID, nonce, timestamp))
	signature, err := signOAuthBrokerPayload(conn.PrivateKeyPEM, payload)
	if err != nil {
		return nil, err
	}
	body, _ := json.Marshal(map[string]any{
		"device_id": conn.DeviceID,
		"nonce":     nonce,
		"timestamp": timestamp,
		"signature": base64.RawURLEncoding.EncodeToString(signature),
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/auth/token", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	var result brokerJWTResponse
	if err := c.doJSON(req, &result); err != nil {
		return nil, fmt.Errorf("issue oauth broker token: %w", err)
	}
	_, _ = database.DB.Exec(`UPDATE oauth_broker_connection SET last_auth_at = ?, updated_at = ? WHERE broker_base_url = ?`, time.Now().UTC(), time.Now().UTC(), c.baseURL)
	return &result, nil
}

func (c oauthBrokerClient) createSession(ctx context.Context, jwt string, provider string) (*brokerCreateSessionResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/oauth/"+url.PathEscape(provider)+"/session", bytes.NewReader([]byte(`{}`)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Content-Type", "application/json")
	var result brokerCreateSessionResponse
	if err := c.doJSON(req, &result); err != nil {
		return nil, fmt.Errorf("create oauth broker session: %w", err)
	}
	return &result, nil
}

func (c oauthBrokerClient) status(ctx context.Context, jwt string, sessionID string) (*brokerStatusResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/oauth/status/"+url.PathEscape(sessionID), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	var result brokerStatusResponse
	if err := c.doJSON(req, &result); err != nil {
		return nil, fmt.Errorf("get oauth broker status: %w", err)
	}
	return &result, nil
}

func (c oauthBrokerClient) exchange(ctx context.Context, jwt string, sessionID string) (*brokerExchangeResponse, error) {
	body, _ := json.Marshal(map[string]string{"session_id": sessionID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/oauth/exchange", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Content-Type", "application/json")
	var result brokerExchangeResponse
	if err := c.doJSON(req, &result); err != nil {
		return nil, fmt.Errorf("exchange oauth broker session: %w", err)
	}
	return &result, nil
}

func (c oauthBrokerClient) doJSON(req *http.Request, out any) error {
	requestBody := readAndRestoreRequestBody(req)
	log.Printf(
		"\n=== OAuth Broker Request ===\nmethod: %s\nurl: %s\nheaders:\n%s\nbody:\n%s\n=== End OAuth Broker Request ===",
		req.Method,
		req.URL.String(),
		indentOAuthBrokerLog(prettyOAuthBrokerLogValue(sanitizeOAuthBrokerHeaders(req.Header))),
		indentOAuthBrokerLog(sanitizeOAuthBrokerBody(requestBody)),
	)
	resp, err := c.http.Do(req)
	if err != nil {
		log.Printf(
			"\n=== OAuth Broker Response ===\nmethod: %s\nurl: %s\nerror: %v\n=== End OAuth Broker Response ===",
			req.Method,
			req.URL.String(),
			err,
		)
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	log.Printf(
		"\n=== OAuth Broker Response ===\nmethod: %s\nurl: %s\nstatus: %d\nheaders:\n%s\nbody:\n%s\n=== End OAuth Broker Response ===",
		req.Method,
		req.URL.String(),
		resp.StatusCode,
		indentOAuthBrokerLog(prettyOAuthBrokerLogValue(sanitizeOAuthBrokerHeaders(resp.Header))),
		indentOAuthBrokerLog(sanitizeOAuthBrokerBody(body)),
	)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func readAndRestoreRequestBody(req *http.Request) []byte {
	if req == nil || req.Body == nil {
		return nil
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return []byte("<read request body failed: " + err.Error() + ">")
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	return body
}

func sanitizeOAuthBrokerHeaders(headers http.Header) map[string][]string {
	result := make(map[string][]string, len(headers))
	for key, values := range headers {
		lower := strings.ToLower(key)
		if lower == "authorization" || lower == "x-registration-secret" {
			result[key] = []string{"***"}
			continue
		}
		copied := make([]string, len(values))
		copy(copied, values)
		result[key] = copied
	}
	return result
}

func sanitizeOAuthBrokerBody(body []byte) string {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return ""
	}
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return trimmed
	}
	return prettyOAuthBrokerLogValue(sanitizeOAuthBrokerJSON(payload))
}

func sanitizeOAuthBrokerJSON(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			if isOAuthBrokerSensitiveKey(key) {
				result[key] = "***"
				continue
			}
			result[key] = sanitizeOAuthBrokerJSON(item)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for i, item := range typed {
			result[i] = sanitizeOAuthBrokerJSON(item)
		}
		return result
	default:
		return value
	}
}

func isOAuthBrokerSensitiveKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "access_token", "refresh_token", "signature", "private_key_pem", "public_key_pem", "client_secret", "authorization":
		return true
	default:
		return false
	}
}

func prettyOAuthBrokerLogValue(value any) string {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Sprintf("%v", value)
	}
	return string(encoded)
}

func indentOAuthBrokerLog(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "  <empty>"
	}
	lines := strings.Split(value, "\n")
	for i, line := range lines {
		lines[i] = "  " + line
	}
	return strings.Join(lines, "\n")
}

func oauthBrokerBaseURL() string {
	return strings.TrimRight(firstNonEmpty(strings.TrimSpace(os.Getenv("OAUTH_BROKER_BASE_URL")), defaultOAuthBrokerBaseURL), "/")
}

func oauthBrokerRegistrationSecret() string {
	return strings.TrimSpace(firstNonEmpty(
		os.Getenv("OAUTH_BROKER_REGISTRATION_SECRET"),
		os.Getenv("DEVICE_REGISTRATION_SECRET"),
		defaultOAuthBrokerRegistrationSecret,
	))
}

func newOAuthBrokerConnection(baseURL string) (*oauthBrokerConnection, error) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	publicDER, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		return nil, err
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return nil, err
	}
	hostname, _ := os.Hostname()
	deviceName := firstNonEmpty(strings.TrimSpace(os.Getenv("OAUTH_BROKER_DEVICE_NAME")), hostname, "YesNAS")
	return &oauthBrokerConnection{
		ID:            idgen.New(),
		BrokerBaseURL: baseURL,
		DeviceID:      "yesnas-" + idgen.New(),
		DeviceName:    deviceName,
		PublicKeyPEM:  string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER})),
		PrivateKeyPEM: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})),
	}, nil
}

func signOAuthBrokerPayload(privateKeyPEM string, payload []byte) ([]byte, error) {
	key, err := parseOAuthBrokerPrivateKeyPEM(privateKeyPEM)
	if err != nil {
		return nil, err
	}
	switch privateKey := key.(type) {
	case *rsa.PrivateKey:
		digest := sha256.Sum256(payload)
		return rsa.SignPKCS1v15(rand.Reader, privateKey, crypto.SHA256, digest[:])
	case ed25519.PrivateKey:
		return ed25519.Sign(privateKey, payload), nil
	default:
		return nil, fmt.Errorf("unsupported private key type")
	}
}

func parseOAuthBrokerPrivateKeyPEM(value string) (any, error) {
	block, _ := pem.Decode([]byte(value))
	if block == nil {
		return nil, fmt.Errorf("invalid private key PEM")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err == nil {
		return key, nil
	}
	rsaKey, rsaErr := x509.ParsePKCS1PrivateKey(block.Bytes)
	if rsaErr == nil {
		return rsaKey, nil
	}
	return nil, err
}

func getOAuthBrokerConnection(baseURL string) (*oauthBrokerConnection, error) {
	var item oauthBrokerConnection
	err := database.DB.Get(&item, `SELECT id, broker_base_url, device_id, COALESCE(device_name, '') AS device_name, public_key_pem, private_key_pem, registered_at, last_auth_at FROM oauth_broker_connection WHERE broker_base_url = ?`, strings.TrimRight(baseURL, "/"))
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &item, nil
}

func saveOAuthBrokerConnection(item oauthBrokerConnection) error {
	_, err := database.DB.Exec(`INSERT INTO oauth_broker_connection (id, broker_base_url, device_id, device_name, public_key_pem, private_key_pem, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, item.ID, item.BrokerBaseURL, item.DeviceID, item.DeviceName, item.PublicKeyPEM, item.PrivateKeyPEM, time.Now().UTC())
	return err
}

func saveOAuthBrokerSession(item oauthBrokerSession) error {
	_, err := database.DB.Exec(
		`INSERT INTO oauth_broker_session (session_id, broker_base_url, provider, storage_id, name, root_path, status, authorize_url, expires_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(session_id) DO UPDATE SET
		 status = excluded.status, authorize_url = excluded.authorize_url, expires_at = excluded.expires_at, updated_at = excluded.updated_at`,
		item.SessionID, item.BrokerBaseURL, item.Provider, item.StorageID, item.Name, item.RootPath, item.Status, item.AuthorizeURL, item.ExpiresAt, time.Now().UTC(),
	)
	return err
}

func getOAuthBrokerSession(sessionID string) (*oauthBrokerSession, error) {
	var item oauthBrokerSession
	err := database.DB.Get(&item, `SELECT session_id, broker_base_url, provider, COALESCE(storage_id, '') AS storage_id, COALESCE(name, '') AS name, COALESCE(root_path, '') AS root_path, COALESCE(status, '') AS status, COALESCE(authorize_url, '') AS authorize_url, expires_at FROM oauth_broker_session WHERE session_id = ?`, strings.TrimSpace(sessionID))
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("oauth session not found")
		}
		return nil, err
	}
	return &item, nil
}

func updateOAuthBrokerSessionStatus(sessionID string, status string) error {
	_, err := database.DB.Exec(`UPDATE oauth_broker_session SET status = ?, updated_at = ? WHERE session_id = ?`, strings.TrimSpace(status), time.Now().UTC(), strings.TrimSpace(sessionID))
	return err
}

func saveOAuthBrokerStorageToken(storageID string, brokerBaseURL string, exchanged *brokerExchangeResponse) error {
	configJSON, _ := json.Marshal(exchanged.Rclone)
	id := idgen.New()
	existing, err := getOAuthBrokerStorageToken(storageID)
	if err != nil {
		return err
	}
	if existing != nil {
		id = existing.ID
	}
	_, err = database.DB.Exec(
		`INSERT INTO oauth_broker_storage_token (id, storage_id, broker_base_url, provider, broker_refresh_token, rclone_token_url, rclone_config_json, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(storage_id) DO UPDATE SET
		 broker_base_url = excluded.broker_base_url,
		 provider = excluded.provider,
		 broker_refresh_token = excluded.broker_refresh_token,
		 rclone_token_url = excluded.rclone_token_url,
		 rclone_config_json = excluded.rclone_config_json,
		 updated_at = excluded.updated_at`,
		id, storageID, brokerBaseURL, exchanged.Provider, exchanged.Rclone.Token.RefreshToken, exchanged.Rclone.TokenURL, string(configJSON), time.Now().UTC(),
	)
	return err
}

func getOAuthBrokerStorageToken(storageID string) (*oauthBrokerStorageToken, error) {
	var item oauthBrokerStorageToken
	err := database.DB.Get(&item, `SELECT id, storage_id, broker_base_url, provider, broker_refresh_token, rclone_token_url, COALESCE(rclone_config_json, '') AS rclone_config_json FROM oauth_broker_storage_token WHERE storage_id = ?`, storageID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &item, nil
}

func deleteOAuthBrokerStorageToken(storageID string) error {
	_, err := database.DB.Exec(`DELETE FROM oauth_broker_storage_token WHERE storage_id = ?`, strings.TrimSpace(storageID))
	return err
}

func deleteOAuthBrokerSessionsByStorageID(storageID string) error {
	_, err := database.DB.Exec(`DELETE FROM oauth_broker_session WHERE storage_id = ?`, strings.TrimSpace(storageID))
	return err
}

func stringFromAny(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case fmt.Stringer:
		return strings.TrimSpace(v.String())
	default:
		return ""
	}
}
