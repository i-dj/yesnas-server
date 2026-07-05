package storage

import (
	"bufio"
	"bytes"
	"context"
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
	defaultGoogleDriveRcloneFile  = "oauth/rclone.conf"
	defaultGoogleDriveMountRoot   = "/srv/yesnas/cloud"
	defaultGoogleDriveUserInfoURL = "https://www.googleapis.com/drive/v3/about?fields=user(emailAddress)"
	defaultGoogleDriveFilesURL    = "https://www.googleapis.com/drive/v3/files"
	defaultGoogleDriveUploadURL   = "https://www.googleapis.com/upload/drive/v3/files?uploadType=multipart"
	googleDriveFolderMimeType     = "application/vnd.google-apps.folder"
)

type CloudConnectRequest struct {
	StorageID string `json:"storageId"`
	Name      string `json:"name"`
	RootPath  string `json:"rootPath"`
}

type CloudConnectResponse struct {
	Provider    string `json:"provider"`
	AuthURL     string `json:"authUrl"`
	State       string `json:"state"`
	RedirectURL string `json:"redirectUrl"`
	ExpiresAt   string `json:"expiresAt"`
}

type CloudConnectCompleteResponse struct {
	Connected        bool     `json:"connected"`
	Provider         string   `json:"provider"`
	StorageID        string   `json:"storageId"`
	Storage          Storage  `json:"storage"`
	RcloneRemoteName string   `json:"rcloneRemoteName"`
	Warnings         []string `json:"warnings,omitempty"`
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

var defaultGoogleDriveRcloneFilePath = resolveProjectPath(defaultGoogleDriveRcloneFile)

func EnsureGoogleDriveMounted(ctx context.Context, item *Storage, token *Token) error {
	if item == nil || token == nil || !isOAuthBrokerCloudProvider(item.Provider) {
		return nil
	}

	desiredMountPath := cloudMountPath(item.Provider, item.ID)
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

	remote := cloudRemoteName(item.Provider, item.ID) + ":"
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
	if item == nil || !isOAuthBrokerCloudProvider(item.Provider) {
		return nil, fmt.Errorf("cloud storage is required")
	}
	remote := cloudRemoteName(item.Provider, item.ID) + ":"
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
		return nil, fmt.Errorf("rclone about cloud storage: %w", err)
	}
	var about rcloneAboutResponse
	if err := json.Unmarshal([]byte(result.Stdout), &about); err != nil {
		return nil, fmt.Errorf("decode rclone about cloud storage: %w", err)
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
	if item == nil || !isOAuthBrokerCloudProvider(item.Provider) {
		return nil, fmt.Errorf("cloud storage is required")
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
	remote := cloudRemoteName(item.Provider, item.ID) + ":" + remotePath
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

func normalizeGoogleDriveRootPath(value string) string {
	clean := strings.TrimSpace(value)
	if clean == "" {
		return "root"
	}
	return clean
}

func isOAuthBrokerCloudProvider(provider string) bool {
	switch strings.TrimSpace(provider) {
	case string(ProviderGoogleDrive), string(ProviderOneDrive), string(ProviderDropbox):
		return true
	default:
		return false
	}
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

	refreshURL := ""
	if brokerToken, err := getOAuthBrokerStorageToken(storageID); err == nil && brokerToken != nil {
		refreshURL = brokerToken.RcloneTokenURL
	}
	if refreshURL == "" && strings.TrimSpace(token.RawJSON) != "" {
		var raw map[string]any
		if err := json.Unmarshal([]byte(token.RawJSON), &raw); err == nil {
			refreshURL = stringFromAny(raw["token_url"])
		}
	}

	values := url.Values{}
	values.Set("refresh_token", refreshToken)
	values.Set("grant_type", "refresh_token")
	if refreshURL == "" {
		return nil, fmt.Errorf("oauth broker rclone token_url is missing")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, refreshURL, strings.NewReader(values.Encode()))
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
		"token_url":     refreshURL,
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

	rcloneType := "drive"
	scope := "drive"
	clientID := ""
	clientSecret := ""
	tokenURL := ""
	if strings.TrimSpace(token.RawJSON) != "" {
		var raw map[string]any
		if err := json.Unmarshal([]byte(token.RawJSON), &raw); err == nil {
			rcloneType = firstNonEmpty(stringFromAny(raw["type"]), rcloneType)
			scope = firstNonEmpty(stringFromAny(raw["scope"]), scope)
			clientID = stringFromAny(raw["client_id"])
			clientSecret = stringFromAny(raw["client_secret"])
			tokenURL = stringFromAny(raw["token_url"])
		}
	}
	if brokerToken, err := getOAuthBrokerStorageToken(item.ID); err == nil && brokerToken != nil {
		tokenURL = firstNonEmpty(brokerToken.RcloneTokenURL, tokenURL)
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

	if tokenURL == "" {
		return fmt.Errorf("oauth broker rclone token_url is missing")
	}

	var builder strings.Builder
	section := cloudRemoteName(item.Provider, item.ID)
	builder.WriteString("[" + section + "]\n")
	builder.WriteString("type = " + rcloneType + "\n")
	builder.WriteString("scope = " + scope + "\n")
	if clientID != "" {
		builder.WriteString("client_id = " + clientID + "\n")
	}
	if clientSecret != "" {
		builder.WriteString("client_secret = " + clientSecret + "\n")
	}
	if tokenURL != "" {
		builder.WriteString("token_url = " + tokenURL + "\n")
	}
	builder.WriteString("token = " + string(tokenJSON) + "\n")
	if rootPath := strings.TrimSpace(item.RootPath); item.Provider == string(ProviderGoogleDrive) && rootPath != "" && rootPath != "root" {
		builder.WriteString("root_folder_id = " + rootPath + "\n")
	}

	content, err := os.ReadFile(defaultGoogleDriveRcloneFilePath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	updated := upsertINISection(string(content), section, strings.TrimSpace(builder.String()))
	return os.WriteFile(defaultGoogleDriveRcloneFilePath, []byte(updated), 0o600)
}

func CleanupOAuthBrokerCloudStorage(ctx context.Context, item *Storage) error {
	if item == nil || !isOAuthBrokerCloudProvider(item.Provider) {
		return nil
	}
	mountPath := strings.TrimSpace(item.MountPath)
	if mountPath != "" {
		if mounted, _ := googleDriveMountStatus(ctx, mountPath); mounted {
			if err := unmountGoogleDrive(ctx, mountPath); err != nil {
				return err
			}
		}
	}
	if err := removeGoogleDriveRcloneConfig(item.Provider, item.ID); err != nil {
		return fmt.Errorf("remove rclone config: %w", err)
	}
	if err := DeleteTokenByStorageID(item.ID); err != nil {
		return fmt.Errorf("delete storage token: %w", err)
	}
	if err := deleteOAuthBrokerStorageToken(item.ID); err != nil {
		return fmt.Errorf("delete oauth broker storage token: %w", err)
	}
	if err := deleteOAuthBrokerSessionsByStorageID(item.ID); err != nil {
		return fmt.Errorf("delete oauth broker sessions: %w", err)
	}
	return nil
}

func removeGoogleDriveRcloneConfig(provider string, storageID string) error {
	content, err := os.ReadFile(defaultGoogleDriveRcloneFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	updated := removeINISection(string(content), cloudRemoteName(provider, storageID))
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

func removeINISection(content string, sectionName string) string {
	if strings.TrimSpace(content) == "" {
		return ""
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
		return strings.TrimSpace(content) + "\n"
	}
	merged := make([]string, 0, len(lines)-(end-start))
	merged = append(merged, lines[:start]...)
	merged = append(merged, lines[end:]...)
	trimmed := strings.TrimSpace(strings.Join(merged, "\n"))
	if trimmed == "" {
		return ""
	}
	return trimmed + "\n"
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
