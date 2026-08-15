package storage

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"nas-server/pkg/idgen"
	commandrunner "nas-server/pkg/shell"
)

const defaultNetworkMountRoot = "/srv/yesnas/network"

type NetworkProtocol string

const (
	ProtocolFTP    NetworkProtocol = "ftp"
	ProtocolWebDAV NetworkProtocol = "webdav"
	ProtocolSMB    NetworkProtocol = "smb"
	ProtocolNFS    NetworkProtocol = "nfs"
)

type CreateNetworkRequest struct {
	Name      string          `json:"name"`
	Protocol  NetworkProtocol `json:"protocol"`
	Host      string          `json:"host"`
	Port      int             `json:"port,omitempty"`
	URL       string          `json:"url,omitempty"`
	Username  string          `json:"username,omitempty"`
	Password  string          `json:"password,omitempty"`
	Domain    string          `json:"domain,omitempty"`
	ShareName string          `json:"shareName,omitempty"`
	RootPath  string          `json:"rootPath,omitempty"`
}

type CreateNetworkResponse struct {
	Connected bool    `json:"connected"`
	StorageID string  `json:"storageId"`
	Storage   Storage `json:"storage"`
}

type ListSMBSharesRequest struct {
	Host     string `json:"host"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	Domain   string `json:"domain,omitempty"`
}

type SMBShare struct {
	Name    string `json:"name"`
	Comment string `json:"comment,omitempty"`
}

type ListSMBSharesResponse struct {
	Items []SMBShare `json:"items"`
}

func CreateNetworkStorage(ctx context.Context, req CreateNetworkRequest) (*CreateNetworkResponse, error) {
	if err := validateNetworkRequest(req); err != nil {
		return nil, err
	}

	item := Storage{
		ID:        idgen.New(),
		Name:      strings.TrimSpace(req.Name),
		Location:  "network",
		Type:      Type(req.Protocol),
		Provider:  string(req.Protocol),
		Host:      strings.TrimSpace(req.Host),
		Port:      req.Port,
		URL:       strings.TrimSpace(req.URL),
		Username:  strings.TrimSpace(req.Username),
		Password:  req.Password,
		Domain:    strings.TrimSpace(req.Domain),
		ShareName: strings.TrimSpace(req.ShareName),
		RootPath:  normalizeNetworkRootPath(req.RootPath),
		Status:    StatusOffline,
	}
	item.MountPath = networkMountPath(item.Provider, item.ID)
	item.ExtraConfig = BuildExtraConfig(map[string]any{
		"protocol": string(req.Protocol),
	})

	if err := EnsureNetworkStorageMounted(ctx, &item); err != nil {
		cleanupGeneratedNetworkMountPath(ctx, item.MountPath)
		return nil, err
	}

	total, free := mountedFilesystemUsage(item.MountPath)
	item.TotalSize = total
	item.FreeSize = free
	item.Status = StatusOnline
	if _, err := Add(item); err != nil {
		cleanupGeneratedNetworkMountPath(ctx, item.MountPath)
		return nil, fmt.Errorf("create network storage record: %w", err)
	}

	return &CreateNetworkResponse{Connected: true, StorageID: item.ID, Storage: item}, nil
}

func ListSMBShares(ctx context.Context, req ListSMBSharesRequest) (*ListSMBSharesResponse, error) {
	host := strings.Trim(strings.TrimSpace(req.Host), "/")
	if host == "" {
		return nil, fmt.Errorf("SMB server address is required")
	}

	shares, err := listSMBSharesWithAuth(ctx, req)
	if err != nil {
		return nil, err
	}
	return &ListSMBSharesResponse{Items: shares}, nil
}

func EnsureNetworkStorageMounted(ctx context.Context, item *Storage) error {
	if item == nil || !IsNetworkProvider(item.Provider) {
		return nil
	}
	if strings.TrimSpace(item.MountPath) == "" {
		item.MountPath = networkMountPath(item.Provider, item.ID)
	}
	if isMountpoint(ctx, item.MountPath) {
		return nil
	}
	if err := ensureMountPath(ctx, item.MountPath); err != nil {
		return err
	}

	switch NetworkProtocol(item.Provider) {
	case ProtocolSMB:
		return mountSMB(ctx, *item)
	case ProtocolNFS:
		return mountNFS(ctx, *item)
	case ProtocolFTP, ProtocolWebDAV:
		return mountRcloneNetwork(ctx, *item)
	default:
		return fmt.Errorf("unsupported network storage protocol: %s", item.Provider)
	}
}

func RefreshNetworkStorageUsage(ctx context.Context, item *Storage) error {
	if item == nil || !IsNetworkProvider(item.Provider) {
		return nil
	}
	if err := EnsureNetworkStorageMounted(ctx, item); err != nil {
		_ = UpdateRuntime(item.ID, item.MountPath, StatusError, item.TotalSize, item.FreeSize, item.ExtraConfig)
		return err
	}

	total, free := mountedFilesystemUsage(item.MountPath)
	item.TotalSize = total
	item.FreeSize = free
	item.Status = StatusOnline
	return UpdateRuntime(item.ID, item.MountPath, item.Status, item.TotalSize, item.FreeSize, item.ExtraConfig)
}

func IsNetworkProvider(provider string) bool {
	switch NetworkProtocol(strings.TrimSpace(provider)) {
	case ProtocolFTP, ProtocolWebDAV, ProtocolSMB, ProtocolNFS:
		return true
	default:
		return false
	}
}

func validateNetworkRequest(req CreateNetworkRequest) error {
	if strings.TrimSpace(req.Name) == "" {
		return fmt.Errorf("storage name is required")
	}
	switch req.Protocol {
	case ProtocolFTP, ProtocolWebDAV, ProtocolSMB, ProtocolNFS:
	default:
		return fmt.Errorf("unsupported network storage protocol: %s", req.Protocol)
	}
	if req.Protocol == ProtocolWebDAV {
		if strings.TrimSpace(req.URL) == "" {
			return fmt.Errorf("WebDAV URL is required")
		}
		if _, err := url.ParseRequestURI(strings.TrimSpace(req.URL)); err != nil {
			return fmt.Errorf("invalid WebDAV URL")
		}
		return nil
	}
	if strings.TrimSpace(req.Host) == "" {
		return fmt.Errorf("host is required")
	}
	if req.Protocol == ProtocolSMB && strings.TrimSpace(req.ShareName) == "" {
		return fmt.Errorf("SMB share name is required")
	}
	if req.Protocol == ProtocolNFS && strings.TrimSpace(req.ShareName) == "" {
		return fmt.Errorf("NFS export path is required")
	}
	return nil
}

func normalizeNetworkRootPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "/"
	}
	return value
}

func networkMountPath(provider string, storageID string) string {
	return filepath.Join(defaultNetworkMountRoot, provider+"_"+storageID)
}

func ensureMountPath(ctx context.Context, mountPath string) error {
	if strings.TrimSpace(mountPath) == "" {
		return fmt.Errorf("mount path is required")
	}
	if result, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "mkdir", "-p", mountPath); err != nil {
		return fmt.Errorf("prepare mount path: %w%s", err, commandStderrSuffix(result.Stderr))
	}
	if result, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "chmod", "0777", mountPath); err != nil {
		return fmt.Errorf("prepare mount path permissions: %w%s", err, commandStderrSuffix(result.Stderr))
	}
	return nil
}

func cleanupGeneratedNetworkMountPath(ctx context.Context, mountPath string) {
	mountPath = filepath.Clean(strings.TrimSpace(mountPath))
	root := filepath.Clean(defaultNetworkMountRoot)
	if mountPath == "." || mountPath == root || !strings.HasPrefix(mountPath, root+string(os.PathSeparator)) {
		return
	}
	if isMountpoint(ctx, mountPath) {
		_, _ = commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true, SuppressSuccessLogs: true}, "umount", mountPath)
	}
	_, _ = commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true, SuppressSuccessLogs: true}, "rm", "-rf", mountPath)
}

func isMountpoint(ctx context.Context, mountPath string) bool {
	result, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{SuppressSuccessLogs: true}, "mount")
	if err != nil {
		return false
	}
	needle := " on " + mountPath + " "
	for _, line := range strings.Split(result.Stdout, "\n") {
		if strings.Contains(line, needle) {
			return true
		}
	}
	return false
}

func mountSMB(ctx context.Context, item Storage) error {
	source := "//" + strings.Trim(strings.TrimSpace(item.Host), "/") + "/" + strings.Trim(strings.TrimSpace(item.ShareName), "/")
	options := []string{
		"iocharset=utf8",
		"rw",
		"uid=" + strconv.Itoa(os.Getuid()),
		"gid=" + strconv.Itoa(os.Getgid()),
		"file_mode=0664",
		"dir_mode=0775",
	}
	credentialsFile, err := smbCredentialsFile(item)
	if err != nil {
		return err
	}
	if credentialsFile != "" {
		defer os.Remove(credentialsFile)
		options = append(options, "credentials="+credentialsFile)
	} else {
		options = append(options, "guest")
	}
	if item.RootPath != "" && item.RootPath != "/" {
		options = append(options, "prefixpath="+strings.Trim(item.RootPath, "/"))
	}
	result, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "mount", "-t", "cifs", source, item.MountPath, "-o", strings.Join(options, ","))
	if err != nil {
		return fmt.Errorf("%s%s", explainSMBMountError(result.Stderr, credentialsFile == ""), commandStderrSuffix(result.Stderr))
	}
	return nil
}

func explainSMBMountError(stderr string, guest bool) string {
	normalized := strings.ToLower(stderr)
	authHint := "当前账号"
	if guest {
		authHint = "匿名用户"
	}
	switch {
	case strings.Contains(normalized, "permission denied"):
		return "SMB 认证失败或共享权限不足，请检查用户名、密码、域和共享目录权限"
	case strings.Contains(normalized, "no such file") || strings.Contains(normalized, "object name not found"):
		return "SMB 共享名称不存在，或" + authHint + "无权访问该共享"
	case strings.Contains(normalized, "could not resolve address") || strings.Contains(normalized, "name or service not known"):
		return "无法解析 SMB 服务器地址，请检查主机名或 IP"
	case strings.Contains(normalized, "connection refused"):
		return "SMB 服务器拒绝连接，请检查 SMB 服务是否开启"
	case strings.Contains(normalized, "host is down") || strings.Contains(normalized, "no route to host"):
		return "无法连接 SMB 服务器，请检查网络和防火墙"
	case strings.Contains(normalized, "operation not supported"):
		return "SMB 协议版本不兼容，可尝试调整服务端 SMB 版本"
	default:
		return "SMB 挂载失败"
	}
}

func listSMBSharesWithAuth(ctx context.Context, req ListSMBSharesRequest) ([]SMBShare, error) {
	path, err := exec.LookPath("smbclient")
	if err != nil {
		return nil, fmt.Errorf("未找到 smbclient，请先安装 samba-client 或 smbclient")
	}

	host := strings.Trim(strings.TrimSpace(req.Host), "/")
	args := []string{"-L", "//" + host, "-g"}
	if strings.TrimSpace(req.Username) == "" {
		args = append(args, "-N")
	} else {
		args = append(args, "-U", strings.TrimSpace(req.Username))
		if strings.TrimSpace(req.Domain) != "" {
			args = append(args, "-W", strings.TrimSpace(req.Domain))
		}
	}

	cmd := exec.CommandContext(ctx, path, args...)
	if strings.TrimSpace(req.Username) != "" {
		cmd.Env = append(os.Environ(), "PASSWD="+req.Password)
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%s%s", explainSMBListError(string(output), strings.TrimSpace(req.Username) == ""), commandStderrSuffix(string(output)))
	}

	shares := parseSMBClientShares(string(output))
	if len(shares) == 0 {
		return nil, fmt.Errorf("没有发现可挂载的 SMB 共享，请检查服务器是否发布了共享目录")
	}
	return shares, nil
}

func parseSMBClientShares(output string) []SMBShare {
	shares := make([]SMBShare, 0)
	for _, line := range strings.Split(output, "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "|", 3)
		if len(parts) < 2 || !strings.EqualFold(parts[0], "Disk") {
			continue
		}
		name := strings.TrimSpace(parts[1])
		if name == "" || strings.EqualFold(name, "IPC$") || strings.EqualFold(name, "print$") {
			continue
		}
		comment := ""
		if len(parts) == 3 {
			comment = strings.TrimSpace(parts[2])
		}
		shares = append(shares, SMBShare{Name: name, Comment: comment})
	}
	return shares
}

func explainSMBListError(output string, guest bool) string {
	normalized := strings.ToLower(output)
	switch {
	case strings.Contains(normalized, "logon failure") || strings.Contains(normalized, "access denied") || strings.Contains(normalized, "permission denied"):
		if guest {
			return "匿名访问 SMB 共享列表失败，请填写用户名和密码后重试"
		}
		return "SMB 登录失败，请检查用户名、密码和域"
	case strings.Contains(normalized, "connection refused"):
		return "SMB 服务器拒绝连接，请检查 SMB 服务是否开启"
	case strings.Contains(normalized, "name or service not known") || strings.Contains(normalized, "could not resolve"):
		return "无法解析 SMB 服务器地址，请检查主机名或 IP"
	case strings.Contains(normalized, "no route to host") || strings.Contains(normalized, "host is down"):
		return "无法连接 SMB 服务器，请检查网络和防火墙"
	default:
		return "读取 SMB 共享列表失败"
	}
}

func smbCredentialsFile(item Storage) (string, error) {
	if strings.TrimSpace(item.Username) == "" && item.Password == "" && strings.TrimSpace(item.Domain) == "" {
		return "", nil
	}

	credentialsFile, err := os.CreateTemp("", "yesnas-smb-credentials-*.conf")
	if err != nil {
		return "", fmt.Errorf("create smb credentials file: %w", err)
	}

	lines := []string{
		"username=" + item.Username,
		"password=" + item.Password,
	}
	if item.Domain != "" {
		lines = append(lines, "domain="+item.Domain)
	}
	if _, err := credentialsFile.WriteString(strings.Join(lines, "\n") + "\n"); err != nil {
		credentialsFile.Close()
		os.Remove(credentialsFile.Name())
		return "", fmt.Errorf("write smb credentials file: %w", err)
	}
	if err := credentialsFile.Close(); err != nil {
		os.Remove(credentialsFile.Name())
		return "", fmt.Errorf("close smb credentials file: %w", err)
	}
	if err := os.Chmod(credentialsFile.Name(), 0600); err != nil {
		os.Remove(credentialsFile.Name())
		return "", fmt.Errorf("protect smb credentials file: %w", err)
	}
	return credentialsFile.Name(), nil
}

func mountNFS(ctx context.Context, item Storage) error {
	exportPath := strings.TrimSpace(item.ShareName)
	if !strings.HasPrefix(exportPath, "/") {
		exportPath = "/" + exportPath
	}
	source := strings.TrimSpace(item.Host) + ":" + exportPath
	if result, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{UseSudo: true}, "mount", "-t", "nfs", source, item.MountPath); err != nil {
		return fmt.Errorf("%s: %w%s", explainNFSMountError(result.Stderr), err, commandStderrSuffix(result.Stderr))
	}
	return nil
}

func explainNFSMountError(stderr string) string {
	normalized := strings.ToLower(stderr)
	switch {
	case strings.Contains(normalized, "access denied") || strings.Contains(normalized, "permission denied"):
		return "NFS 访问被拒绝，请检查导出路径和客户端授权"
	case strings.Contains(normalized, "no such file"):
		return "NFS 导出路径不存在，请检查导出路径"
	case strings.Contains(normalized, "connection refused"):
		return "NFS 服务器拒绝连接，请检查 NFS 服务是否开启"
	case strings.Contains(normalized, "no route to host") || strings.Contains(normalized, "host is down"):
		return "无法连接 NFS 服务器，请检查网络和防火墙"
	default:
		return "NFS 挂载失败"
	}
}

func mountRcloneNetwork(ctx context.Context, item Storage) error {
	remotePath := rcloneRemotePath(item.RootPath)
	remote := ":"
	args := []string{"mount"}
	switch NetworkProtocol(item.Provider) {
	case ProtocolFTP:
		remote += "ftp:" + remotePath
		args = append(args, remote, item.MountPath, "--ftp-host", item.Host)
		if item.Port > 0 {
			args = append(args, "--ftp-port", strconv.Itoa(item.Port))
		}
		if item.Username != "" {
			args = append(args, "--ftp-user", item.Username)
		}
		if item.Password != "" {
			obscured, err := obscureRclonePassword(ctx, item.Password)
			if err != nil {
				return err
			}
			args = append(args, "--ftp-pass", obscured)
		}
	case ProtocolWebDAV:
		remote += "webdav:" + remotePath
		args = append(args, remote, item.MountPath, "--webdav-url", item.URL, "--webdav-vendor", "other")
		if item.Username != "" {
			args = append(args, "--webdav-user", item.Username)
		}
		if item.Password != "" {
			obscured, err := obscureRclonePassword(ctx, item.Password)
			if err != nil {
				return err
			}
			args = append(args, "--webdav-pass", obscured)
		}
	default:
		return fmt.Errorf("unsupported rclone network protocol: %s", item.Provider)
	}
	args = append(args,
		"--daemon",
		"--daemon-timeout", "15s",
		"--allow-other",
		"--umask", "000",
		"--vfs-cache-mode", "writes",
	)
	if _, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{}, "rclone", args...); err != nil {
		return fmt.Errorf("mount %s storage: %w", item.Provider, err)
	}
	return nil
}

func rcloneRemotePath(rootPath string) string {
	rootPath = strings.TrimSpace(rootPath)
	if rootPath == "" || rootPath == "/" || rootPath == "root" {
		return ""
	}
	if strings.HasPrefix(rootPath, "/") {
		return rootPath
	}
	return "/" + rootPath
}

func obscureRclonePassword(ctx context.Context, password string) (string, error) {
	output, err := exec.CommandContext(ctx, "rclone", "obscure", password).Output()
	if err != nil {
		return "", fmt.Errorf("prepare rclone password: %w", err)
	}
	obscured := strings.TrimSpace(string(output))
	if obscured == "" {
		return "", fmt.Errorf("prepare rclone password: empty result")
	}
	return obscured, nil
}

func mountedFilesystemUsage(mountPath string) (int64, int64) {
	info, err := os.Stat(mountPath)
	if err != nil || !info.IsDir() {
		return 0, 0
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(mountPath, &stat); err != nil {
		return 0, 0
	}
	total := int64(stat.Blocks) * int64(stat.Bsize)
	free := int64(stat.Bavail) * int64(stat.Bsize)
	return total, free
}
