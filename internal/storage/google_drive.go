package storage

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"nas-server/pkg/idgen"
	commandrunner "nas-server/pkg/shell"
)

const (
	defaultGoogleDriveScope       = "https://www.googleapis.com/auth/drive"
	defaultGoogleDriveAuthBaseURL = "https://accounts.google.com/o/oauth2/v2/auth"
	defaultGoogleDriveTokenURL    = "https://oauth2.googleapis.com/token"
	defaultGoogleDriveOAuthFile   = "oauth/google_drive_web.json"
	defaultGoogleDriveRcloneFile  = "oauth/rclone.conf"
	defaultGoogleDriveMountRoot   = "/srv/yesnas/cloud"
	defaultGoogleDriveUserInfoURL = "https://www.googleapis.com/drive/v3/about?fields=user(emailAddress)"
	defaultGoogleDriveFilesURL    = "https://www.googleapis.com/drive/v3/files"
	defaultGoogleDriveUploadURL   = "https://www.googleapis.com/upload/drive/v3/files?uploadType=multipart"
	googleDriveFolderMimeType     = "application/vnd.google-apps.folder"
)

type GoogleDriveConnectRequest struct {
	StorageID          string `json:"storageId"`
	Name               string `json:"name"`
	RootPath           string `json:"rootPath"`
	Scope              string `json:"scope"`
	SuccessRedirectURL string `json:"successRedirectURL"`
	FailureRedirectURL string `json:"failureRedirectURL"`
}

type GoogleDriveConnectResponse struct {
	Provider    string `json:"provider"`
	AuthURL     string `json:"authUrl"`
	State       string `json:"state"`
	RedirectURL string `json:"redirectUrl"`
	ExpiresAt   string `json:"expiresAt"`
}

type GoogleDriveCallbackResponse struct {
	Connected        bool    `json:"connected"`
	Provider         string  `json:"provider"`
	StorageID        string  `json:"storageId"`
	Storage          Storage `json:"storage"`
	RcloneRemoteName string  `json:"rcloneRemoteName"`
}

type googleDriveOAuthState struct {
	StorageID          string
	Name               string
	RootPath           string
	Scope              string
	SuccessRedirectURL string
	FailureRedirectURL string
	RedirectURL        string
	ExpiresAt          time.Time
}

type googleDriveTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
}

type googleDriveAPIError struct {
	Body       string
	Operation  string
	StatusCode int
}

func (e *googleDriveAPIError) Error() string {
	return fmt.Sprintf("%s failed: status=%d body=%s", e.Operation, e.StatusCode, strings.TrimSpace(e.Body))
}

type googleDriveUserInfo struct {
	User struct {
		EmailAddress string `json:"emailAddress"`
	} `json:"user"`
}

type googleDriveFileListResponse struct {
	Files []googleDriveFile `json:"files"`
}

type googleDriveFile struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type CloudUsage struct {
	TotalBytes int64
	UsedBytes  int64
	FreeBytes  int64
}

type CloudBenchmarkProgress struct {
	Stage                   string
	TotalBytes              int64
	CompletedBytes          int64
	CurrentSpeedBytesPerSec float64
	StartedAt               time.Time
}

type CloudBenchmarkResult struct {
	RemotePath            string
	SizeBytes             int64
	WriteSpeedBytesPerSec float64
	ReadSpeedBytesPerSec  float64
}

type rcloneAboutResponse struct {
	Total   int64 `json:"total"`
	Used    int64 `json:"used"`
	Free    int64 `json:"free"`
	Trashed int64 `json:"trashed"`
	Other   int64 `json:"other"`
}

type googleOAuthClientFile struct {
	Web googleOAuthClientWeb `json:"web"`
}

type googleOAuthClientWeb struct {
	ClientID     string   `json:"client_id"`
	ClientSecret string   `json:"client_secret"`
	RedirectURIs []string `json:"redirect_uris"`
}

type googleOAuthClientConfig struct {
	ClientID     string
	ClientSecret string
	RedirectURL  string
}

var googleDriveStateStore = struct {
	mu    sync.Mutex
	items map[string]googleDriveOAuthState
}{
	items: map[string]googleDriveOAuthState{},
}

var (
	defaultGoogleDriveOAuthFilePath  = resolveProjectPath(defaultGoogleDriveOAuthFile)
	defaultGoogleDriveRcloneFilePath = resolveProjectPath(defaultGoogleDriveRcloneFile)
)

func StartGoogleDriveOAuth(r *http.Request, req GoogleDriveConnectRequest) (*GoogleDriveConnectResponse, error) {
	clientConfig, err := loadGoogleOAuthClientConfig("")
	if err != nil {
		return nil, err
	}

	redirectURL := strings.TrimSpace(clientConfig.RedirectURL)
	if redirectURL == "" {
		redirectURL = inferGoogleDriveRedirectURL(r)
	}

	scope := strings.TrimSpace(req.Scope)
	if scope == "" {
		scope = defaultGoogleDriveScope
	}

	state, err := newGoogleDriveStateToken()
	if err != nil {
		return nil, fmt.Errorf("generate oauth state: %w", err)
	}

	expiresAt := time.Now().Add(10 * time.Minute)
	googleDriveStateStore.mu.Lock()
	googleDriveStateStore.items[state] = googleDriveOAuthState{
		StorageID:          strings.TrimSpace(req.StorageID),
		Name:               strings.TrimSpace(req.Name),
		RootPath:           normalizeGoogleDriveRootPath(req.RootPath),
		Scope:              scope,
		SuccessRedirectURL: strings.TrimSpace(req.SuccessRedirectURL),
		FailureRedirectURL: strings.TrimSpace(req.FailureRedirectURL),
		RedirectURL:        redirectURL,
		ExpiresAt:          expiresAt,
	}
	googleDriveStateStore.mu.Unlock()

	authURL, err := buildGoogleDriveAuthURL(clientConfig.ClientID, redirectURL, state, scope)
	if err != nil {
		return nil, err
	}

	return &GoogleDriveConnectResponse{
		Provider:    string(ProviderGoogleDrive),
		AuthURL:     authURL,
		State:       state,
		RedirectURL: redirectURL,
		ExpiresAt:   expiresAt.Format(time.RFC3339),
	}, nil
}

func CompleteGoogleDriveOAuth(ctx context.Context, code string, state string) (*GoogleDriveCallbackResponse, string, error) {
	code = strings.TrimSpace(code)
	state = strings.TrimSpace(state)
	if code == "" {
		return nil, "", fmt.Errorf("authorization code is required")
	}
	if state == "" {
		return nil, "", fmt.Errorf("state is required")
	}

	stateData, ok := consumeGoogleDriveState(state)
	if !ok {
		return nil, "", fmt.Errorf("oauth state is invalid or expired")
	}

	tokenResp, err := exchangeGoogleDriveCode(ctx, code, stateData.RedirectURL)
	if err != nil {
		return nil, stateData.FailureRedirectURL, err
	}

	accountEmail, err := fetchGoogleDriveAccountEmail(ctx, tokenResp.AccessToken)
	if err != nil {
		return nil, stateData.FailureRedirectURL, err
	}

	storageID, item, err := createOrReconnectGoogleDriveStorage(ctx, stateData, tokenResp, accountEmail)
	if err != nil {
		return nil, stateData.FailureRedirectURL, err
	}

	rcloneRemoteName := googleDriveRemoteName(storageID)
	extraConfig := BuildExtraConfig(map[string]any{
		"backend":          "drive",
		"provider":         string(ProviderGoogleDrive),
		"rcloneRemoteName": rcloneRemoteName,
		"scope":            stateData.Scope,
		"rootPath":         stateData.RootPath,
		"redirectURL":      stateData.RedirectURL,
	})
	if err := UpdateRuntime(storageID, item.MountPath, item.Status, item.TotalSize, item.FreeSize, extraConfig); err != nil {
		return nil, stateData.FailureRedirectURL, fmt.Errorf("update google drive storage config: %w", err)
	}
	item.ExtraConfig = extraConfig

	expiry := tokenExpiryString(tokenResp)
	rawJSON, _ := json.Marshal(map[string]any{
		"access_token":  tokenResp.AccessToken,
		"refresh_token": tokenResp.RefreshToken,
		"token_type":    tokenResp.TokenType,
		"scope":         tokenResp.Scope,
		"expiry":        expiry,
	})
	if _, err := UpsertToken(Token{
		StorageID:    storageID,
		TokenType:    tokenResp.TokenType,
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		Expiry:       stringPtr(expiry),
		Scope:        firstNonEmpty(strings.TrimSpace(tokenResp.Scope), stateData.Scope),
		RawJSON:      string(rawJSON),
	}); err != nil {
		_ = Delete(storageID)
		return nil, stateData.FailureRedirectURL, fmt.Errorf("save google drive token: %w", err)
	}

	token, err := GetTokenByStorageID(storageID)
	if err != nil || token == nil {
		_ = DeleteTokenByStorageID(storageID)
		_ = Delete(storageID)
		return nil, stateData.FailureRedirectURL, fmt.Errorf("load google drive token for mount: %w", err)
	}

	if err := EnsureGoogleDriveMounted(ctx, &item, token); err != nil {
		_ = DeleteTokenByStorageID(storageID)
		_ = Delete(storageID)
		return nil, stateData.FailureRedirectURL, err
	}

	return &GoogleDriveCallbackResponse{
		Connected:        true,
		Provider:         string(ProviderGoogleDrive),
		StorageID:        storageID,
		Storage:          item,
		RcloneRemoteName: rcloneRemoteName,
	}, stateData.SuccessRedirectURL, nil
}

func BuildGoogleDriveRcloneConfig(item Storage, token Token) map[string]any {
	clientConfig, _ := loadGoogleOAuthClientConfig("")
	config := map[string]any{
		"type":          "drive",
		"scope":         "drive",
		"client_id":     clientConfig.ClientID,
		"client_secret": clientConfig.ClientSecret,
		"token": map[string]any{
			"access_token":  token.AccessToken,
			"refresh_token": token.RefreshToken,
			"token_type":    token.TokenType,
			"expiry":        derefString(token.Expiry),
		},
	}
	if rootPath := strings.TrimSpace(item.RootPath); rootPath != "" && rootPath != "root" {
		config["root_folder_id"] = rootPath
	}
	return config
}

func buildGoogleDriveAuthURL(clientID string, redirectURL string, state string, scope string) (string, error) {
	values := url.Values{}
	values.Set("client_id", clientID)
	values.Set("redirect_uri", redirectURL)
	values.Set("response_type", "code")
	values.Set("scope", scope)
	values.Set("state", state)
	values.Set("access_type", "offline")
	values.Set("prompt", "consent")
	values.Set("include_granted_scopes", "true")

	parsed, err := url.Parse(defaultGoogleDriveAuthBaseURL)
	if err != nil {
		return "", fmt.Errorf("build google drive auth url: %w", err)
	}
	parsed.RawQuery = values.Encode()
	return parsed.String(), nil
}

func exchangeGoogleDriveCode(ctx context.Context, code string, redirectURL string) (*googleDriveTokenResponse, error) {
	clientConfig, err := loadGoogleOAuthClientConfig(redirectURL)
	if err != nil {
		return nil, err
	}

	values := url.Values{}
	values.Set("code", code)
	values.Set("client_id", clientConfig.ClientID)
	values.Set("client_secret", clientConfig.ClientSecret)
	values.Set("redirect_uri", redirectURL)
	values.Set("grant_type", "authorization_code")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, defaultGoogleDriveTokenURL, strings.NewReader(values.Encode()))
	if err != nil {
		return nil, fmt.Errorf("build google drive token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("exchange google drive code: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("google drive token exchange failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var tokenResp googleDriveTokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("decode google drive token response: %w", err)
	}
	if strings.TrimSpace(tokenResp.AccessToken) == "" {
		return nil, fmt.Errorf("google drive token response does not include access_token")
	}
	return &tokenResp, nil
}

func createGoogleDriveStorage(stateData googleDriveOAuthState, tokenResp *googleDriveTokenResponse, accountEmail string) (string, Storage, error) {
	storageID := idgen.New()
	rootPath := normalizeGoogleDriveRootPath(stateData.RootPath)
	name := strings.TrimSpace(stateData.Name)
	if name == "" {
		name = "Google Drive"
	}

	item := Storage{
		ID:        storageID,
		Name:      name,
		Location:  "cloud",
		MountPath: googleDriveMountPath(storageID),
		Type:      Cloud,
		Provider:  string(ProviderGoogleDrive),
		URL:       "https://www.googleapis.com/drive/v3",
		Username:  strings.TrimSpace(accountEmail),
		RootPath:  rootPath,
		Status:    StatusOnline,
		ExtraConfig: BuildExtraConfig(map[string]any{
			"backend":          "drive",
			"provider":         string(ProviderGoogleDrive),
			"rcloneRemoteName": googleDriveRemoteName(storageID),
			"scope":            firstNonEmpty(strings.TrimSpace(tokenResp.Scope), stateData.Scope),
			"rootPath":         rootPath,
			"redirectURL":      stateData.RedirectURL,
		}),
	}

	if _, err := Add(item); err != nil {
		return "", Storage{}, fmt.Errorf("create google drive storage: %w", err)
	}

	return storageID, item, nil
}

func createOrReconnectGoogleDriveStorage(ctx context.Context, stateData googleDriveOAuthState, tokenResp *googleDriveTokenResponse, accountEmail string) (string, Storage, error) {
	if strings.TrimSpace(stateData.StorageID) == "" {
		return createGoogleDriveStorage(stateData, tokenResp, accountEmail)
	}
	return reconnectGoogleDriveStorage(ctx, stateData, tokenResp, accountEmail)
}

func reconnectGoogleDriveStorage(ctx context.Context, stateData googleDriveOAuthState, tokenResp *googleDriveTokenResponse, accountEmail string) (string, Storage, error) {
	storageID := strings.TrimSpace(stateData.StorageID)
	item, err := Get(storageID)
	if err != nil {
		return "", Storage{}, fmt.Errorf("load google drive storage for reconnect: %w", err)
	}
	if item == nil || item.Provider != string(ProviderGoogleDrive) {
		return "", Storage{}, fmt.Errorf("google drive storage not found")
	}

	if strings.TrimSpace(item.RootPath) == "" {
		item.RootPath = normalizeGoogleDriveRootPath(stateData.RootPath)
	}
	if strings.TrimSpace(item.MountPath) == "" {
		item.MountPath = googleDriveMountPath(storageID)
	}
	item.Username = strings.TrimSpace(accountEmail)
	item.Status = StatusOnline
	item.ExtraConfig = BuildExtraConfig(map[string]any{
		"backend":          "drive",
		"provider":         string(ProviderGoogleDrive),
		"rcloneRemoteName": googleDriveRemoteName(storageID),
		"scope":            firstNonEmpty(strings.TrimSpace(tokenResp.Scope), stateData.Scope),
		"rootPath":         item.RootPath,
		"redirectURL":      stateData.RedirectURL,
	})

	if err := UpdateIdentity(storageID, item.Username); err != nil {
		return "", Storage{}, fmt.Errorf("update google drive account identity: %w", err)
	}
	if err := UpdateRuntime(storageID, item.MountPath, item.Status, item.TotalSize, item.FreeSize, item.ExtraConfig); err != nil {
		return "", Storage{}, fmt.Errorf("update google drive runtime config: %w", err)
	}

	expiry := tokenExpiryString(tokenResp)
	rawJSON, _ := json.Marshal(map[string]any{
		"access_token":  tokenResp.AccessToken,
		"refresh_token": tokenResp.RefreshToken,
		"token_type":    tokenResp.TokenType,
		"scope":         tokenResp.Scope,
		"expiry":        expiry,
	})
	if _, err := UpsertToken(Token{
		StorageID:    storageID,
		TokenType:    tokenResp.TokenType,
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		Expiry:       stringPtr(expiry),
		Scope:        firstNonEmpty(strings.TrimSpace(tokenResp.Scope), stateData.Scope),
		RawJSON:      string(rawJSON),
	}); err != nil {
		return "", Storage{}, fmt.Errorf("save google drive token: %w", err)
	}

	token, err := GetTokenByStorageID(storageID)
	if err != nil || token == nil {
		return "", Storage{}, fmt.Errorf("load google drive token for mount: %w", err)
	}
	if err := EnsureGoogleDriveMounted(ctx, item, token); err != nil {
		return "", Storage{}, err
	}
	return storageID, *item, nil
}

func EnsureGoogleDriveMounted(ctx context.Context, item *Storage, token *Token) error {
	if item == nil || token == nil || item.Provider != string(ProviderGoogleDrive) {
		return nil
	}

	desiredMountPath := googleDriveMountPath(item.ID)
	if strings.TrimSpace(item.MountPath) == "" || !strings.HasPrefix(strings.TrimSpace(item.MountPath), "/") {
		item.MountPath = desiredMountPath
	}

	if err := ensureGoogleDriveMountRoot(ctx); err != nil {
		_ = UpdateRuntime(item.ID, item.MountPath, StatusError, item.TotalSize, item.FreeSize, item.ExtraConfig)
		item.Status = StatusError
		return fmt.Errorf("prepare google drive mount root: %w", err)
	}

	if err := upsertGoogleDriveRcloneConfig(*item, *token); err != nil {
		_ = UpdateRuntime(item.ID, item.MountPath, StatusError, item.TotalSize, item.FreeSize, item.ExtraConfig)
		item.Status = StatusError
		return fmt.Errorf("write google drive rclone config: %w", err)
	}

	mounted, shareable := googleDriveMountStatus(ctx, item.MountPath)
	if mounted && shareable {
		if item.MountPath != desiredMountPath || item.Status != StatusOnline {
			if err := UpdateRuntime(item.ID, desiredMountPath, StatusOnline, item.TotalSize, item.FreeSize, item.ExtraConfig); err == nil {
				item.MountPath = desiredMountPath
				item.Status = StatusOnline
			}
		}
		return nil
	}
	if mounted && !shareable {
		if err := unmountGoogleDrive(ctx, item.MountPath); err != nil {
			_ = UpdateRuntime(item.ID, item.MountPath, StatusError, item.TotalSize, item.FreeSize, item.ExtraConfig)
			item.Status = StatusError
			return fmt.Errorf("remount google drive with smb access: %w", err)
		}
	}

	if err := ensureGoogleDriveMountPath(ctx, item.MountPath); err != nil {
		_ = UpdateRuntime(item.ID, item.MountPath, StatusError, item.TotalSize, item.FreeSize, item.ExtraConfig)
		item.Status = StatusError
		return fmt.Errorf("prepare google drive mount path: %w", err)
	}

	remote := googleDriveRemoteName(item.ID) + ":"
	if err := mountGoogleDrive(ctx, remote, item.MountPath); err != nil {
		_ = UpdateRuntime(item.ID, item.MountPath, StatusError, item.TotalSize, item.FreeSize, item.ExtraConfig)
		item.Status = StatusError
		return fmt.Errorf("mount google drive: %w", err)
	}

	if err := UpdateRuntime(item.ID, desiredMountPath, StatusOnline, item.TotalSize, item.FreeSize, item.ExtraConfig); err != nil {
		return fmt.Errorf("update google drive runtime after mount: %w", err)
	}
	item.MountPath = desiredMountPath
	item.Status = StatusOnline
	return nil
}

func fetchGoogleDriveAccountEmail(ctx context.Context, accessToken string) (string, error) {
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return "", fmt.Errorf("google drive access token is missing")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, defaultGoogleDriveUserInfoURL, nil)
	if err != nil {
		return "", fmt.Errorf("build google drive about request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch google drive account email: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", &googleDriveAPIError{Operation: "google drive about", StatusCode: resp.StatusCode, Body: string(body)}
	}

	var payload googleDriveUserInfo
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("decode google drive about: %w", err)
	}
	email := strings.TrimSpace(payload.User.EmailAddress)
	if email == "" {
		return "", fmt.Errorf("google drive about does not include email")
	}
	return email, nil
}

func BackfillGoogleDriveAccountEmail(ctx context.Context, item *Storage) {
	if item == nil || item.Provider != string(ProviderGoogleDrive) || strings.TrimSpace(item.Username) != "" {
		return
	}

	token, err := GetTokenByStorageID(item.ID)
	if err != nil || token == nil || strings.TrimSpace(token.AccessToken) == "" {
		return
	}

	email, err := fetchGoogleDriveAccountEmail(ctx, token.AccessToken)
	if isGoogleDriveUnauthorized(err) {
		token, err = refreshGoogleDriveToken(ctx, item.ID, token)
		if err != nil || token == nil {
			return
		}
		email, err = fetchGoogleDriveAccountEmail(ctx, token.AccessToken)
	}
	if err != nil || strings.TrimSpace(email) == "" {
		return
	}

	item.Username = email
	_ = UpdateIdentity(item.ID, email)
}

func RefreshGoogleDriveUsage(ctx context.Context, item *Storage) (*CloudUsage, error) {
	if item == nil || item.Provider != string(ProviderGoogleDrive) {
		return nil, fmt.Errorf("google drive storage is required")
	}
	remote := googleDriveRemoteName(item.ID) + ":"
	result, err := commandrunner.RunWithOptions(
		ctx,
		commandrunner.Options{},
		"rclone",
		"about",
		remote,
		"--json",
		"--config", defaultGoogleDriveRcloneFilePath,
	)
	if err != nil {
		return nil, fmt.Errorf("rclone about google drive: %w", err)
	}
	var about rcloneAboutResponse
	if err := json.Unmarshal([]byte(result.Stdout), &about); err != nil {
		return nil, fmt.Errorf("decode rclone about google drive: %w", err)
	}
	usage := &CloudUsage{
		TotalBytes: about.Total,
		UsedBytes:  about.Used,
		FreeBytes:  about.Free,
	}
	if usage.TotalBytes <= 0 && usage.UsedBytes > 0 && usage.FreeBytes > 0 {
		usage.TotalBytes = usage.UsedBytes + usage.FreeBytes
	}
	if usage.FreeBytes <= 0 && usage.TotalBytes > usage.UsedBytes {
		usage.FreeBytes = usage.TotalBytes - usage.UsedBytes
	}
	item.TotalSize = usage.TotalBytes
	item.FreeSize = usage.FreeBytes
	item.Status = StatusOnline
	if err := UpdateRuntime(item.ID, item.MountPath, StatusOnline, usage.TotalBytes, usage.FreeBytes, item.ExtraConfig); err != nil {
		return usage, fmt.Errorf("update google drive usage: %w", err)
	}
	return usage, nil
}

func BenchmarkGoogleDrive(ctx context.Context, item *Storage, sizeBytes int64, emit func(CloudBenchmarkProgress) bool) (*CloudBenchmarkResult, error) {
	if item == nil || item.Provider != string(ProviderGoogleDrive) {
		return nil, fmt.Errorf("google drive storage is required")
	}
	if sizeBytes <= 0 {
		return nil, fmt.Errorf("benchmark size must be greater than 0")
	}
	token, err := GetTokenByStorageID(item.ID)
	if err != nil {
		return nil, fmt.Errorf("load google drive token: %w", err)
	}
	if token == nil || strings.TrimSpace(token.AccessToken) == "" {
		return nil, fmt.Errorf("google drive token is missing")
	}
	if err := upsertGoogleDriveRcloneConfig(*item, *token); err != nil {
		return nil, fmt.Errorf("write google drive rclone config: %w", err)
	}

	localFile, err := os.CreateTemp("", "yesnas-cloud-benchmark-*.tmp")
	if err != nil {
		return nil, fmt.Errorf("create local benchmark file: %w", err)
	}
	localPath := localFile.Name()
	if err := localFile.Close(); err != nil {
		_ = os.Remove(localPath)
		return nil, fmt.Errorf("close local benchmark file: %w", err)
	}
	defer os.Remove(localPath)

	if err := writeGoogleDriveBenchmarkFile(ctx, localPath, sizeBytes); err != nil {
		return nil, err
	}

	downloadFile, err := os.CreateTemp("", "yesnas-cloud-benchmark-download-*.tmp")
	if err != nil {
		return nil, fmt.Errorf("create local download file: %w", err)
	}
	downloadPath := downloadFile.Name()
	_ = downloadFile.Close()
	defer os.Remove(downloadPath)

	remotePath := ".yesnas-benchmark-" + idgen.New() + ".tmp"
	remote := googleDriveRemoteName(item.ID) + ":" + remotePath
	defer func() {
		_, _ = commandrunner.RunWithOptions(context.Background(), commandrunner.Options{}, "rclone", "deletefile", remote, "--config", defaultGoogleDriveRcloneFilePath)
	}()

	writeSpeed, err := runRcloneTransfer(ctx, "write", sizeBytes, emit, "copyto", localPath, remote, "--config", defaultGoogleDriveRcloneFilePath, "--stats", "5s", "--stats-one-line", "--stats-log-level", "NOTICE")
	if err != nil {
		return nil, fmt.Errorf("upload benchmark file: %w", err)
	}
	readSpeed, err := runRcloneTransfer(ctx, "read", sizeBytes, emit, "copyto", remote, downloadPath, "--config", defaultGoogleDriveRcloneFilePath, "--stats", "5s", "--stats-one-line", "--stats-log-level", "NOTICE")
	if err != nil {
		return nil, fmt.Errorf("download benchmark file: %w", err)
	}

	return &CloudBenchmarkResult{
		RemotePath:            remote,
		SizeBytes:             sizeBytes,
		WriteSpeedBytesPerSec: writeSpeed,
		ReadSpeedBytesPerSec:  readSpeed,
	}, nil
}

func writeGoogleDriveBenchmarkFile(ctx context.Context, path string, sizeBytes int64) error {
	const chunkSize int64 = 8 * 1024 * 1024
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open local benchmark file: %w", err)
	}
	defer file.Close()

	chunk := make([]byte, chunkSize)
	remaining := sizeBytes
	for remaining > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		writeSize := int64(len(chunk))
		if remaining < writeSize {
			writeSize = remaining
		}
		if _, err := file.Write(chunk[:writeSize]); err != nil {
			return fmt.Errorf("write local benchmark file: %w", err)
		}
		remaining -= writeSize
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync local benchmark file: %w", err)
	}
	return nil
}

func runRcloneTransfer(ctx context.Context, stage string, sizeBytes int64, emit func(CloudBenchmarkProgress) bool, args ...string) (float64, error) {
	startedAt := time.Now()
	if emit != nil && !emit(CloudBenchmarkProgress{Stage: stage, TotalBytes: sizeBytes, CompletedBytes: 0, StartedAt: startedAt}) {
		return 0, context.Canceled
	}

	rclonePath, err := exec.LookPath("rclone")
	if err != nil {
		rclonePath = "/usr/bin/rclone"
	}
	cmd := exec.CommandContext(ctx, rclonePath, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 0, fmt.Errorf("open rclone stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return 0, fmt.Errorf("open rclone stderr: %w", err)
	}

	progressCh := make(chan rcloneTransferProgress, 16)
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("start rclone: %w", err)
	}

	var readers sync.WaitGroup
	readOutput := func(name string, reader io.Reader) {
		defer readers.Done()
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 1024), 1024*1024)
		scanner.Split(splitRcloneLogLine)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			log.Printf("[RCLONE %s %s] %s", stage, name, line)
			if progress, ok := parseRcloneTransferProgress(line, sizeBytes); ok {
				select {
				case progressCh <- progress:
				case <-ctx.Done():
					return
				default:
				}
			}
		}
		if err := scanner.Err(); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("[RCLONE %s %s] read output: %v", stage, name, err)
		}
	}
	readers.Add(2)
	go readOutput("stdout", stdout)
	go readOutput("stderr", stderr)

	done := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		readers.Wait()
		done <- err
	}()

	lastProgress := rcloneTransferProgress{}
	lastEmitAt := startedAt
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case progress := <-progressCh:
			if progress.CompletedBytes > lastProgress.CompletedBytes {
				lastProgress.CompletedBytes = progress.CompletedBytes
			}
			if progress.SpeedBytesPerSec > 0 {
				lastProgress.SpeedBytesPerSec = progress.SpeedBytesPerSec
			}
			if time.Since(lastEmitAt) >= time.Second {
				if !emitRcloneTransferProgress(stage, sizeBytes, lastProgress, startedAt, emit) {
					return 0, context.Canceled
				}
				lastEmitAt = time.Now()
			}
		case err := <-done:
			if err != nil {
				return 0, err
			}
			elapsed := time.Since(startedAt).Seconds()
			speed := lastProgress.SpeedBytesPerSec
			if speed <= 0 && elapsed > 0 {
				speed = float64(sizeBytes) / elapsed
			}
			if emit != nil && !emit(CloudBenchmarkProgress{Stage: stage, TotalBytes: sizeBytes, CompletedBytes: sizeBytes, CurrentSpeedBytesPerSec: speed, StartedAt: startedAt}) {
				return 0, context.Canceled
			}
			return speed, nil
		case <-ticker.C:
			if !emitRcloneTransferProgress(stage, sizeBytes, lastProgress, startedAt, emit) {
				return 0, context.Canceled
			}
			lastEmitAt = time.Now()
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
}

type rcloneTransferProgress struct {
	CompletedBytes   int64
	SpeedBytesPerSec float64
}

func emitRcloneTransferProgress(stage string, totalBytes int64, progress rcloneTransferProgress, startedAt time.Time, emit func(CloudBenchmarkProgress) bool) bool {
	if emit == nil {
		return true
	}
	completedBytes := progress.CompletedBytes
	if completedBytes < 0 {
		completedBytes = 0
	}
	if totalBytes > 0 && completedBytes > totalBytes {
		completedBytes = totalBytes
	}
	elapsed := time.Since(startedAt).Seconds()
	speed := progress.SpeedBytesPerSec
	if speed <= 0 && elapsed > 0 && completedBytes > 0 {
		speed = float64(completedBytes) / elapsed
	}
	return emit(CloudBenchmarkProgress{
		Stage:                   stage,
		TotalBytes:              totalBytes,
		CompletedBytes:          completedBytes,
		CurrentSpeedBytesPerSec: speed,
		StartedAt:               startedAt,
	})
}

func splitRcloneLogLine(data []byte, atEOF bool) (advance int, token []byte, err error) {
	for i, b := range data {
		if b == '\n' || b == '\r' {
			return i + 1, data[:i], nil
		}
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func parseRcloneTransferProgress(line string, totalBytes int64) (rcloneTransferProgress, bool) {
	index := strings.Index(line, "Transferred:")
	if index >= 0 {
		line = strings.TrimSpace(line[index+len("Transferred:"):])
	} else if index := strings.Index(line, "NOTICE:"); index >= 0 {
		line = strings.TrimSpace(line[index+len("NOTICE:"):])
	} else {
		return rcloneTransferProgress{}, false
	}
	parts := strings.Split(line, ",")
	completedPart := strings.TrimSpace(parts[0])
	if slash := strings.Index(completedPart, "/"); slash >= 0 {
		completedPart = strings.TrimSpace(completedPart[:slash])
	}
	fields := strings.Fields(strings.ReplaceAll(completedPart, ",", ""))
	if len(fields) < 2 {
		return rcloneTransferProgress{}, false
	}
	amount, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return rcloneTransferProgress{}, false
	}
	completed := int64(amount * rcloneSizeUnitMultiplier(fields[1]))
	if completed < 0 {
		completed = 0
	}
	if totalBytes > 0 && completed > totalBytes {
		completed = totalBytes
	}
	return rcloneTransferProgress{
		CompletedBytes:   completed,
		SpeedBytesPerSec: parseRcloneSpeedBytesPerSec(parts),
	}, true
}

func parseRcloneSpeedBytesPerSec(parts []string) float64 {
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if !strings.HasSuffix(strings.ToLower(part), "/s") {
			continue
		}
		part = strings.TrimSuffix(part, "/s")
		part = strings.TrimSuffix(part, "/S")
		fields := strings.Fields(strings.ReplaceAll(part, ",", ""))
		if len(fields) < 2 {
			continue
		}
		amount, err := strconv.ParseFloat(fields[0], 64)
		if err != nil {
			continue
		}
		return amount * rcloneSizeUnitMultiplier(fields[1])
	}
	return 0
}

func rcloneSizeUnitMultiplier(unit string) float64 {
	switch strings.ToLower(strings.TrimSpace(unit)) {
	case "b", "byte", "bytes":
		return 1
	case "kb", "kbyte", "kbytes":
		return 1000
	case "mb", "mbyte", "mbytes":
		return 1000 * 1000
	case "gb", "gbyte", "gbytes":
		return 1000 * 1000 * 1000
	case "tb", "tbyte", "tbytes":
		return 1000 * 1000 * 1000 * 1000
	case "kib", "kibyte", "kibytes":
		return 1024
	case "mib", "mibyte", "mibytes":
		return 1024 * 1024
	case "gib", "gibyte", "gibytes":
		return 1024 * 1024 * 1024
	case "tib", "tibyte", "tibytes":
		return 1024 * 1024 * 1024 * 1024
	default:
		return 1
	}
}

func UploadGoogleDriveReader(ctx context.Context, item *Storage, targetPath string, source io.Reader, contentType string) error {
	if item == nil || item.Provider != string(ProviderGoogleDrive) {
		return fmt.Errorf("google drive storage is required")
	}

	token, err := GetTokenByStorageID(item.ID)
	if err != nil {
		return fmt.Errorf("load google drive token: %w", err)
	}
	if token == nil || strings.TrimSpace(token.AccessToken) == "" {
		return fmt.Errorf("google drive token is missing")
	}

	relativePath, err := filepath.Rel(item.MountPath, targetPath)
	if err != nil {
		return fmt.Errorf("resolve target path: %w", err)
	}
	relativePath = filepath.Clean(relativePath)
	if relativePath == "." || strings.HasPrefix(relativePath, "..") || filepath.IsAbs(relativePath) {
		return fmt.Errorf("target path out of storage root")
	}

	contentType = strings.TrimSpace(contentType)
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	for attempt := 0; attempt < 2; attempt++ {
		if seeker, ok := source.(io.Seeker); ok {
			if _, err := seeker.Seek(0, io.SeekStart); err != nil {
				return fmt.Errorf("reset upload stream: %w", err)
			}
		} else if attempt > 0 {
			return fmt.Errorf("google drive upload failed and upload stream cannot be retried")
		}

		parentID, err := ensureGoogleDriveParentPath(ctx, item, token, filepath.Dir(relativePath))
		if err != nil {
			if attempt == 0 && isGoogleDriveUnauthorized(err) {
				token, err = refreshGoogleDriveToken(ctx, item.ID, token)
				if err == nil && token != nil {
					continue
				}
			}
			return err
		}

		fileName := filepath.Base(relativePath)
		existingID, err := findGoogleDriveChild(ctx, token, parentID, fileName, "")
		if err != nil {
			if attempt == 0 && isGoogleDriveUnauthorized(err) {
				token, err = refreshGoogleDriveToken(ctx, item.ID, token)
				if err == nil && token != nil {
					continue
				}
			}
			return err
		}
		if strings.TrimSpace(existingID) != "" {
			return fmt.Errorf("target file already exists")
		}

		err = uploadGoogleDriveMultipartReader(ctx, token, parentID, fileName, contentType, source)
		if err != nil {
			if attempt == 0 && isGoogleDriveUnauthorized(err) {
				token, err = refreshGoogleDriveToken(ctx, item.ID, token)
				if err == nil && token != nil {
					continue
				}
			}
			return err
		}
		return nil
	}

	return fmt.Errorf("google drive upload failed after retry")
}

func consumeGoogleDriveState(state string) (googleDriveOAuthState, bool) {
	googleDriveStateStore.mu.Lock()
	defer googleDriveStateStore.mu.Unlock()

	item, ok := googleDriveStateStore.items[state]
	if !ok {
		return googleDriveOAuthState{}, false
	}
	delete(googleDriveStateStore.items, state)
	if time.Now().After(item.ExpiresAt) {
		return googleDriveOAuthState{}, false
	}
	return item, true
}

func newGoogleDriveStateToken() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func inferGoogleDriveRedirectURL(r *http.Request) string {
	scheme := "http"
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); forwarded != "" {
		scheme = forwarded
	} else if r.TLS != nil {
		scheme = "https"
	}
	host := strings.TrimSpace(r.Header.Get("X-Forwarded-Host"))
	if host == "" {
		host = r.Host
	}
	return fmt.Sprintf("%s://%s/api/v1/storages/google-drive/callback", scheme, host)
}

func normalizeGoogleDriveRootPath(value string) string {
	clean := strings.TrimSpace(value)
	if clean == "" {
		return "root"
	}
	return clean
}

func googleDriveRemoteName(storageID string) string {
	return "gdrive_" + storageID
}

func googleDriveMountPath(storageID string) string {
	return filepath.Join(defaultGoogleDriveMountRoot, googleDriveRemoteName(storageID))
}

func tokenExpiryString(tokenResp *googleDriveTokenResponse) string {
	if tokenResp == nil || tokenResp.ExpiresIn <= 0 {
		return ""
	}
	return time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second).UTC().Format(time.RFC3339)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func ensureGoogleDriveParentPath(ctx context.Context, item *Storage, token *Token, relativeDir string) (string, error) {
	parentID := normalizeGoogleDriveRootPath(item.RootPath)
	relativeDir = filepath.Clean(strings.TrimSpace(relativeDir))
	if relativeDir == "" || relativeDir == "." {
		return parentID, nil
	}

	for _, segment := range strings.Split(filepath.ToSlash(relativeDir), "/") {
		segment = strings.TrimSpace(segment)
		if segment == "" || segment == "." {
			continue
		}
		existingID, err := findGoogleDriveChild(ctx, token, parentID, segment, googleDriveFolderMimeType)
		if err != nil {
			return "", err
		}
		if existingID == "" {
			existingID, err = createGoogleDriveFolder(ctx, token, parentID, segment)
			if err != nil {
				return "", err
			}
		}
		parentID = existingID
	}
	return parentID, nil
}

func findGoogleDriveChild(ctx context.Context, token *Token, parentID string, name string, mimeType string) (string, error) {
	query := fmt.Sprintf(
		"name = '%s' and '%s' in parents and trashed = false",
		escapeGoogleDriveQueryValue(name),
		escapeGoogleDriveQueryValue(parentID),
	)
	if strings.TrimSpace(mimeType) != "" {
		query += fmt.Sprintf(" and mimeType = '%s'", escapeGoogleDriveQueryValue(mimeType))
	}

	values := url.Values{}
	values.Set("q", query)
	values.Set("fields", "files(id,name)")
	values.Set("pageSize", "1")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, defaultGoogleDriveFilesURL+"?"+values.Encode(), nil)
	if err != nil {
		return "", fmt.Errorf("build google drive list request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token.AccessToken))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("query google drive folder contents: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", &googleDriveAPIError{Operation: "google drive child lookup", StatusCode: resp.StatusCode, Body: string(body)}
	}

	var payload googleDriveFileListResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("decode google drive child lookup: %w", err)
	}
	if len(payload.Files) == 0 {
		return "", nil
	}
	return strings.TrimSpace(payload.Files[0].ID), nil
}

func createGoogleDriveFolder(ctx context.Context, token *Token, parentID string, name string) (string, error) {
	body, err := json.Marshal(map[string]any{
		"name":     name,
		"mimeType": googleDriveFolderMimeType,
		"parents":  []string{parentID},
	})
	if err != nil {
		return "", fmt.Errorf("encode google drive folder create request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, defaultGoogleDriveFilesURL, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build google drive folder create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token.AccessToken))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("create google drive folder: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", &googleDriveAPIError{Operation: "google drive folder create", StatusCode: resp.StatusCode, Body: string(respBody)}
	}

	var payload googleDriveFile
	if err := json.Unmarshal(respBody, &payload); err != nil {
		return "", fmt.Errorf("decode google drive folder create response: %w", err)
	}
	if strings.TrimSpace(payload.ID) == "" {
		return "", fmt.Errorf("google drive folder create response missing id")
	}
	return strings.TrimSpace(payload.ID), nil
}

func uploadGoogleDriveMultipartReader(ctx context.Context, token *Token, parentID string, name string, contentType string, source io.Reader) error {
	pipeReader, pipeWriter := io.Pipe()
	writer := multipart.NewWriter(pipeWriter)
	writeErrCh := make(chan error, 1)

	go func() {
		metadata, err := json.Marshal(map[string]any{
			"name":    name,
			"parents": []string{parentID},
		})
		if err != nil {
			writeErrCh <- fmt.Errorf("encode google drive upload metadata: %w", err)
			_ = pipeWriter.CloseWithError(err)
			return
		}

		metaPart, err := writer.CreatePart(textprotoMIMEHeader("application/json; charset=UTF-8"))
		if err != nil {
			writeErrCh <- fmt.Errorf("create google drive metadata part: %w", err)
			_ = pipeWriter.CloseWithError(err)
			return
		}
		if _, err := metaPart.Write(metadata); err != nil {
			writeErrCh <- fmt.Errorf("write google drive metadata part: %w", err)
			_ = pipeWriter.CloseWithError(err)
			return
		}

		filePart, err := writer.CreatePart(textprotoMIMEHeader(contentType))
		if err != nil {
			writeErrCh <- fmt.Errorf("create google drive file part: %w", err)
			_ = pipeWriter.CloseWithError(err)
			return
		}
		if _, err := io.Copy(filePart, source); err != nil {
			writeErrCh <- fmt.Errorf("write google drive file part: %w", err)
			_ = pipeWriter.CloseWithError(err)
			return
		}
		if err := writer.Close(); err != nil {
			writeErrCh <- fmt.Errorf("finalize google drive multipart body: %w", err)
			_ = pipeWriter.CloseWithError(err)
			return
		}
		writeErrCh <- pipeWriter.Close()
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, defaultGoogleDriveUploadURL, pipeReader)
	if err != nil {
		return fmt.Errorf("build google drive upload request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token.AccessToken))
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if writeErr := <-writeErrCh; writeErr != nil {
			return writeErr
		}
		return fmt.Errorf("upload file to google drive: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if writeErr := <-writeErrCh; writeErr != nil {
		return writeErr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &googleDriveAPIError{Operation: "google drive upload", StatusCode: resp.StatusCode, Body: string(body)}
	}
	return nil
}

func refreshGoogleDriveToken(ctx context.Context, storageID string, token *Token) (*Token, error) {
	if token == nil {
		return nil, fmt.Errorf("google drive token is missing")
	}
	refreshToken := strings.TrimSpace(token.RefreshToken)
	if refreshToken == "" {
		return nil, fmt.Errorf("google drive refresh token is missing")
	}

	clientConfig, err := loadGoogleOAuthClientConfig("")
	if err != nil {
		return nil, err
	}

	values := url.Values{}
	values.Set("client_id", clientConfig.ClientID)
	values.Set("client_secret", clientConfig.ClientSecret)
	values.Set("refresh_token", refreshToken)
	values.Set("grant_type", "refresh_token")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, defaultGoogleDriveTokenURL, strings.NewReader(values.Encode()))
	if err != nil {
		return nil, fmt.Errorf("build google drive refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("refresh google drive token: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("google drive token refresh failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var tokenResp googleDriveTokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("decode google drive refresh response: %w", err)
	}
	if strings.TrimSpace(tokenResp.AccessToken) == "" {
		return nil, fmt.Errorf("google drive refresh response missing access_token")
	}

	updated := *token
	updated.StorageID = storageID
	updated.AccessToken = strings.TrimSpace(tokenResp.AccessToken)
	updated.TokenType = firstNonEmpty(strings.TrimSpace(tokenResp.TokenType), strings.TrimSpace(token.TokenType), "Bearer")
	updated.Scope = firstNonEmpty(strings.TrimSpace(tokenResp.Scope), strings.TrimSpace(token.Scope))
	if strings.TrimSpace(tokenResp.RefreshToken) != "" {
		updated.RefreshToken = strings.TrimSpace(tokenResp.RefreshToken)
	}
	expiry := tokenExpiryString(&tokenResp)
	updated.Expiry = stringPtr(expiry)
	rawJSON, _ := json.Marshal(map[string]any{
		"access_token":  updated.AccessToken,
		"refresh_token": updated.RefreshToken,
		"token_type":    updated.TokenType,
		"scope":         updated.Scope,
		"expiry":        expiry,
	})
	updated.RawJSON = string(rawJSON)

	if _, err := UpsertToken(updated); err != nil {
		return nil, fmt.Errorf("persist refreshed google drive token: %w", err)
	}
	return &updated, nil
}

func isGoogleDriveUnauthorized(err error) bool {
	var apiErr *googleDriveAPIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusUnauthorized
}

func escapeGoogleDriveQueryValue(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	return strings.ReplaceAll(value, `'`, `\'`)
}

func textprotoMIMEHeader(contentType string) textproto.MIMEHeader {
	header := make(textproto.MIMEHeader)
	header.Set("Content-Type", contentType)
	return header
}

func stringPtr(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	v := value
	return &v
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func loadGoogleOAuthClientConfig(expectedRedirectURL string) (googleOAuthClientConfig, error) {
	content, err := os.ReadFile(defaultGoogleDriveOAuthFilePath)
	if err != nil {
		return googleOAuthClientConfig{}, fmt.Errorf("read google drive credentials file %s: %w", defaultGoogleDriveOAuthFilePath, err)
	}

	var payload googleOAuthClientFile
	if err := json.Unmarshal(content, &payload); err != nil {
		return googleOAuthClientConfig{}, fmt.Errorf("decode google drive credentials file: %w", err)
	}
	if strings.TrimSpace(payload.Web.ClientID) == "" || strings.TrimSpace(payload.Web.ClientSecret) == "" {
		return googleOAuthClientConfig{}, fmt.Errorf("google drive credentials file is missing client_id or client_secret")
	}

	config := googleOAuthClientConfig{
		ClientID:     strings.TrimSpace(payload.Web.ClientID),
		ClientSecret: strings.TrimSpace(payload.Web.ClientSecret),
		RedirectURL:  selectGoogleRedirectURI(payload.Web.RedirectURIs, expectedRedirectURL),
	}
	return config, nil
}

func selectGoogleRedirectURI(values []string, expected string) string {
	expected = strings.TrimSpace(expected)
	if expected != "" {
		for _, value := range values {
			if strings.TrimSpace(value) == expected {
				return expected
			}
		}
	}
	if len(values) == 0 {
		return ""
	}
	return strings.TrimSpace(values[0])
}

func ensureGoogleDriveMountPath(ctx context.Context, mountPath string) error {
	if strings.TrimSpace(mountPath) == "" {
		return fmt.Errorf("mount path is required")
	}
	if err := ensureGoogleDriveMountRoot(ctx); err != nil {
		return err
	}
	if result, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "mkdir", "-p", mountPath); err != nil {
		return fmt.Errorf("create google drive mount path %s: %w%s", mountPath, err, commandStderrSuffix(result.Stderr))
	}
	if result, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "chmod", "0777", mountPath); err != nil {
		return fmt.Errorf("chmod google drive mount path %s: %w%s", mountPath, err, commandStderrSuffix(result.Stderr))
	}
	return nil
}

func ensureGoogleDriveMountRoot(ctx context.Context) error {
	if result, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "mkdir", "-p", defaultGoogleDriveMountRoot); err != nil {
		return fmt.Errorf("create google drive mount root %s: %w%s", defaultGoogleDriveMountRoot, err, commandStderrSuffix(result.Stderr))
	}
	if result, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "chmod", "0777", defaultGoogleDriveMountRoot); err != nil {
		return fmt.Errorf("chmod google drive mount root %s: %w%s", defaultGoogleDriveMountRoot, err, commandStderrSuffix(result.Stderr))
	}
	return nil
}

func commandStderrSuffix(stderr string) string {
	stderr = strings.TrimSpace(stderr)
	if stderr == "" {
		return ""
	}
	return ": " + stderr
}

func upsertGoogleDriveRcloneConfig(item Storage, token Token) error {
	if err := os.MkdirAll(filepath.Dir(defaultGoogleDriveRcloneFilePath), 0o755); err != nil {
		return err
	}

	tokenJSON, err := json.Marshal(map[string]any{
		"access_token":  token.AccessToken,
		"refresh_token": token.RefreshToken,
		"token_type":    token.TokenType,
		"expiry":        derefString(token.Expiry),
	})
	if err != nil {
		return err
	}

	clientConfig, err := loadGoogleOAuthClientConfig("")
	if err != nil {
		return err
	}

	var builder strings.Builder
	section := googleDriveRemoteName(item.ID)
	builder.WriteString("[" + section + "]\n")
	builder.WriteString("type = drive\n")
	builder.WriteString("scope = drive\n")
	builder.WriteString("client_id = " + clientConfig.ClientID + "\n")
	builder.WriteString("client_secret = " + clientConfig.ClientSecret + "\n")
	builder.WriteString("token = " + string(tokenJSON) + "\n")
	if rootPath := strings.TrimSpace(item.RootPath); rootPath != "" && rootPath != "root" {
		builder.WriteString("root_folder_id = " + rootPath + "\n")
	}

	content, err := os.ReadFile(defaultGoogleDriveRcloneFilePath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	updated := upsertINISection(string(content), section, strings.TrimSpace(builder.String()))
	return os.WriteFile(defaultGoogleDriveRcloneFilePath, []byte(updated), 0o600)
}

func upsertINISection(content string, sectionName string, replacement string) string {
	if strings.TrimSpace(content) == "" {
		return replacement + "\n"
	}

	lines := strings.Split(content, "\n")
	header := "[" + sectionName + "]"
	start := -1
	end := len(lines)
	for i, line := range lines {
		if strings.TrimSpace(line) == header {
			start = i
			continue
		}
		if start >= 0 && strings.HasPrefix(strings.TrimSpace(line), "[") && strings.HasSuffix(strings.TrimSpace(line), "]") {
			end = i
			break
		}
	}
	if start < 0 {
		content = strings.TrimRight(content, "\n")
		return content + "\n\n" + replacement + "\n"
	}
	var merged []string
	merged = append(merged, lines[:start]...)
	merged = append(merged, strings.Split(replacement, "\n")...)
	merged = append(merged, lines[end:]...)
	return strings.TrimSpace(strings.Join(merged, "\n")) + "\n"
}

func isGoogleDriveMounted(ctx context.Context, mountPath string) bool {
	mounted, _ := googleDriveMountStatus(ctx, mountPath)
	return mounted
}

func googleDriveMountStatus(ctx context.Context, mountPath string) (bool, bool) {
	result, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{}, "mount")
	if err != nil {
		return false, false
	}
	needle := " on " + mountPath + " "
	for _, line := range strings.Split(result.Stdout, "\n") {
		if strings.Contains(line, needle) {
			return true, strings.Contains(line, "allow_other")
		}
	}
	return false, false
}

func unmountGoogleDrive(ctx context.Context, mountPath string) error {
	if strings.TrimSpace(mountPath) == "" {
		return fmt.Errorf("mount path is required")
	}
	if _, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "umount", mountPath); err != nil {
		return fmt.Errorf("unmount google drive %s: %w", mountPath, err)
	}
	return nil
}

func mountGoogleDrive(ctx context.Context, remote string, mountPath string) error {
	_, err := commandrunner.RunWithOptions(
		ctx,
		commandrunner.Options{},
		"rclone",
		"mount",
		remote,
		mountPath,
		"--config", defaultGoogleDriveRcloneFilePath,
		"--daemon",
		"--daemon-timeout", "15s",
		"--allow-other",
		"--umask", "000",
		"--vfs-cache-mode", "writes",
		"--dir-cache-time", "1m",
		"--poll-interval", "30s",
	)
	return err
}

func resolveProjectPath(rel string) string {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		return rel
	}
	projectRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	return filepath.Join(projectRoot, rel)
}
