package darwinplatform

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	storagepkg "nas-server/internal/storage"
	stats "nas-server/pkg/iostatstypes"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	readOpPattern  = regexp.MustCompile(`\b(RdData|PRead|pread|read|read_nocancel)[^\s]*\b`)
	writeOpPattern = regexp.MustCompile(`\b(WrData|PWrite|pwrite|write|write_nocancel)[^\s]*\b`)
	bytesPattern   = regexp.MustCompile(`\b(\d+)\s+bytes\b`)
)

type Stats = stats.Stats

var (
	ErrNotSupported = stats.ErrNotSupported
	ErrTraceBusy    = stats.ErrTraceBusy
)

type Provider struct{}

type smbDebugInfo struct {
	Host               string   `json:"host"`
	ResolvedHosts      []string `json:"resolvedHosts,omitempty"`
	MatchedConnections int      `json:"matchedConnections"`
	ParsedConnections  int      `json:"parsedConnections"`
	MatchedSamples     []string `json:"matchedSamples,omitempty"`
	LastFallbackReason string   `json:"lastFallbackReason,omitempty"`
}

func (p Provider) Sample(ctx context.Context, storage storagepkg.Storage, interval time.Duration) (Stats, error) {
	if interval <= 0 {
		interval = time.Second
	}

	if !supportsDarwinStorage(storage) {
		return Stats{}, ErrNotSupported
	}

	if isDarwinSMBStorage(storage) {
		return sampleDarwinSMB(ctx, storage, interval)
	}

	readBytes, writeBytes, err := sampleFSUsage(ctx, storage.MountPath, interval)
	if err != nil {
		return Stats{}, err
	}

	seconds := interval.Seconds()
	stats := Stats{
		StorageID:   storage.ID,
		StorageType: string(storage.Type),
		Platform:    "darwin",
		Method:      "fs_usage",
		Scope:       "mount",
		MountPath:   storage.MountPath,
		ReadSpeed:   float64(readBytes) / seconds,
		WriteSpeed:  float64(writeBytes) / seconds,
		MeasuredAt:  time.Now(),
		Note:        buildDarwinNote(storage),
	}
	return stats, nil
}

func supportsDarwinStorage(storage storagepkg.Storage) bool {
	switch storage.Type {
	case storagepkg.Local, storagepkg.SMB:
		return true
	}

	url := strings.ToLower(storage.URL)
	return strings.HasPrefix(url, "file://") || strings.HasPrefix(url, "smb://")
}

func buildDarwinNote(storage storagepkg.Storage) string {
	switch storage.Type {
	case storagepkg.SMB:
		return "Sampled from macOS network interface counters for the SMB route."
	default:
		return "Sampled from macOS fs_usage on the mounted local path."
	}
}

func isDarwinSMBStorage(storage storagepkg.Storage) bool {
	if storage.Type == storagepkg.SMB {
		return true
	}
	return strings.HasPrefix(strings.ToLower(storage.URL), "smb://")
}

func sampleDarwinSMB(ctx context.Context, storage storagepkg.Storage, interval time.Duration) (Stats, error) {
	host := resolveStorageHost(storage)
	if host == "" {
		return Stats{}, fmt.Errorf("无法确定 SMB 主机地址，请补充 storage.host 或 url")
	}

	stats, err := sampleDarwinSMBByHost(ctx, storage, host, interval)
	if err == nil {
		return stats, nil
	}
	fallbackReason := err.Error()

	iface, err := resolveRouteInterface(ctx, host)
	if err != nil {
		return Stats{}, err
	}

	first, err := readInterfaceCounters(ctx, iface)
	if err != nil {
		return Stats{}, err
	}

	timer := time.NewTimer(interval)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return Stats{}, ctx.Err()
	case <-timer.C:
	}

	second, err := readInterfaceCounters(ctx, iface)
	if err != nil {
		return Stats{}, err
	}

	readBytes := second.ibytes - first.ibytes
	writeBytes := second.obytes - first.obytes
	if readBytes < 0 {
		readBytes = 0
	}
	if writeBytes < 0 {
		writeBytes = 0
	}

	seconds := interval.Seconds()
	return Stats{
		StorageID:   storage.ID,
		StorageType: string(storage.Type),
		Platform:    "darwin",
		Method:      "netstat",
		Scope:       iface,
		MountPath:   storage.MountPath,
		ReadSpeed:   float64(readBytes) / seconds,
		WriteSpeed:  float64(writeBytes) / seconds,
		MeasuredAt:  time.Now(),
		Note:        buildDarwinNote(storage),
		Debug: smbDebugInfo{
			Host:               host,
			LastFallbackReason: fallbackReason,
		},
	}, nil
}

func sampleDarwinSMBByHost(ctx context.Context, storage storagepkg.Storage, host string, interval time.Duration) (Stats, error) {
	resolvedHosts, err := resolveHostAliases(ctx, host)
	if err != nil {
		return Stats{}, err
	}

	firstIn, firstOut, _, err := readSMBConnectionCounters(ctx, resolvedHosts)
	if err != nil {
		return Stats{}, err
	}

	timer := time.NewTimer(interval)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return Stats{}, ctx.Err()
	case <-timer.C:
	}

	secondIn, secondOut, secondDebug, err := readSMBConnectionCounters(ctx, resolvedHosts)
	if err != nil {
		return Stats{}, err
	}
	if secondDebug.matchedConnections == 0 {
		return Stats{}, fmt.Errorf("nettop 未匹配到 SMB 主机连接")
	}

	readBytes := secondIn - firstIn
	writeBytes := secondOut - firstOut
	if readBytes < 0 {
		readBytes = 0
	}
	if writeBytes < 0 {
		writeBytes = 0
	}

	seconds := interval.Seconds()
	debug := smbDebugInfo{
		Host:               host,
		ResolvedHosts:      mapKeys(resolvedHosts),
		MatchedConnections: secondDebug.matchedConnections,
		ParsedConnections:  secondDebug.parsedConnections,
		MatchedSamples:     secondDebug.matchedSamples,
	}
	return Stats{
		StorageID:   storage.ID,
		StorageType: string(storage.Type),
		Platform:    "darwin",
		Method:      "nettop",
		Scope:       host,
		MountPath:   storage.MountPath,
		ReadSpeed:   float64(readBytes) / seconds,
		WriteSpeed:  float64(writeBytes) / seconds,
		MeasuredAt:  time.Now(),
		Note:        "Sampled from macOS nettop by filtering TCP connections to the SMB host.",
		Debug:       debug,
	}, nil
}

func resolveStorageHost(storage storagepkg.Storage) string {
	if storage.Host != "" {
		return storage.Host
	}

	url := strings.TrimSpace(storage.URL)
	if url == "" {
		return ""
	}
	url = strings.TrimPrefix(url, "smb://")
	if url == storage.URL {
		url = strings.TrimPrefix(url, "file://")
	}
	if idx := strings.Index(url, "/"); idx >= 0 {
		url = url[:idx]
	}
	if idx := strings.Index(url, "@"); idx >= 0 {
		url = url[idx+1:]
	}
	if idx := strings.Index(url, ":"); idx >= 0 {
		url = url[:idx]
	}
	return strings.TrimSpace(url)
}

func resolveRouteInterface(ctx context.Context, host string) (string, error) {
	output, err := runCommand(ctx, "route", "-n", "get", host)
	if err != nil {
		return "", fmt.Errorf("获取主机 %s 的路由接口失败: %w", host, err)
	}

	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "interface:") {
			continue
		}
		iface := strings.TrimSpace(strings.TrimPrefix(line, "interface:"))
		if iface != "" {
			return iface, nil
		}
	}

	return "", fmt.Errorf("未找到主机 %s 的网络接口", host)
}

func resolveHostAliases(ctx context.Context, host string) (map[string]bool, error) {
	aliases := map[string]bool{
		strings.ToLower(host): true,
	}

	output, err := runCommand(ctx, "dscacheutil", "-q", "host", "-a", "name", host)
	if err == nil {
		for _, line := range strings.Split(output, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "ip_address:") {
				ip := strings.TrimSpace(strings.TrimPrefix(line, "ip_address:"))
				if ip != "" {
					aliases[strings.ToLower(ip)] = true
				}
			}
			if strings.HasPrefix(line, "name:") {
				name := strings.TrimSpace(strings.TrimPrefix(line, "name:"))
				if name != "" {
					aliases[strings.ToLower(name)] = true
				}
			}
		}
	}

	return aliases, nil
}

type ifaceCounters struct {
	ibytes int64
	obytes int64
}

type nettopDebugInfo struct {
	matchedConnections int
	parsedConnections  int
	matchedSamples     []string
}

func readInterfaceCounters(ctx context.Context, iface string) (ifaceCounters, error) {
	output, err := runCommand(ctx, "netstat", "-bI", iface)
	if err != nil {
		return ifaceCounters{}, fmt.Errorf("读取接口 %s 统计失败: %w", iface, err)
	}

	lines := strings.Split(output, "\n")
	if len(lines) < 2 {
		return ifaceCounters{}, fmt.Errorf("接口 %s 统计输出为空", iface)
	}

	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) < 10 || fields[0] != iface {
			continue
		}

		ibytes, err1 := strconv.ParseInt(fields[6], 10, 64)
		obytes, err2 := strconv.ParseInt(fields[9], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		return ifaceCounters{ibytes: ibytes, obytes: obytes}, nil
	}

	return ifaceCounters{}, fmt.Errorf("无法解析接口 %s 的字节计数", iface)
}

func readSMBConnectionCounters(ctx context.Context, hosts map[string]bool) (int64, int64, nettopDebugInfo, error) {
	output, err := runCommand(ctx, "nettop", "-n", "-x", "-d", "-m", "tcp", "-l", "1")
	if err != nil {
		return 0, 0, nettopDebugInfo{}, fmt.Errorf("读取 SMB 连接统计失败: %w", err)
	}

	var bytesIn int64
	var bytesOut int64
	debug := nettopDebugInfo{}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if !strings.Contains(line, "<->") {
			continue
		}
		if !lineLooksLikeSMBConnection(line, hosts) {
			continue
		}
		debug.matchedConnections++
		if len(debug.matchedSamples) < 5 {
			debug.matchedSamples = append(debug.matchedSamples, line)
		}

		inVal, outVal, ok := parseNettopBytes(line)
		if !ok {
			continue
		}
		debug.parsedConnections++
		bytesIn += inVal
		bytesOut += outVal
	}

	return bytesIn, bytesOut, debug, nil
}

func lineLooksLikeSMBConnection(line string, hosts map[string]bool) bool {
	leftRight := strings.Split(line, "<->")
	if len(leftRight) != 2 {
		return false
	}
	remote := leftRight[1]
	fields := strings.Fields(remote)
	if len(fields) == 0 {
		return false
	}

	remoteAddr := fields[0]
	hostPart, portPart := splitHostPort(remoteAddr)
	if portPart != "445" {
		return false
	}
	return hosts[strings.ToLower(hostPart)]
}

func splitHostPort(addr string) (string, string) {
	addr = strings.TrimSpace(addr)
	if idx := strings.LastIndex(addr, ":"); idx >= 0 {
		return strings.Trim(addr[:idx], "[]"), addr[idx+1:]
	}
	return addr, ""
}

func parseNettopBytes(line string) (int64, int64, bool) {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return 0, 0, false
	}

	outVal, errOut := strconv.ParseInt(fields[len(fields)-1], 10, 64)
	inVal, errIn := strconv.ParseInt(fields[len(fields)-2], 10, 64)
	if errIn != nil || errOut != nil {
		return 0, 0, false
	}
	return inVal, outVal, true
}

func mapKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

func runCommand(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		errText := strings.TrimSpace(stderr.String())
		if errText != "" {
			return "", fmt.Errorf("%s", errText)
		}
		return "", err
	}
	return stdout.String(), nil
}

func sampleFSUsage(ctx context.Context, mountPath string, interval time.Duration) (int64, int64, error) {
	mountPath = filepath.Clean(mountPath)
	for attempt := 0; attempt < 3; attempt++ {
		readBytes, writeBytes, err := runFSUsageSample(ctx, mountPath, interval)
		if err == nil {
			return readBytes, writeBytes, nil
		}
		if !errors.Is(err, ErrTraceBusy) {
			return 0, 0, err
		}

		select {
		case <-ctx.Done():
			return 0, 0, ctx.Err()
		case <-time.After(750 * time.Millisecond):
		}
	}

	return 0, 0, fmt.Errorf("fs_usage 执行失败: %w", ErrTraceBusy)
}

func runFSUsageSample(ctx context.Context, mountPath string, interval time.Duration) (int64, int64, error) {
	sampleCtx, cancel := context.WithTimeout(ctx, interval)
	defer cancel()

	cmd := exec.CommandContext(sampleCtx, "fs_usage", "-w", "-f", "filesys")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 0, 0, fmt.Errorf("打开 fs_usage 输出失败: %w", err)
	}

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return 0, 0, fmt.Errorf("启动 fs_usage 失败: %w", err)
	}

	var readBytes int64
	var writeBytes int64
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.Contains(line, mountPath) {
			continue
		}

		bytesCount, ok := extractBytes(line)
		if !ok {
			continue
		}

		switch {
		case readOpPattern.MatchString(line):
			readBytes += bytesCount
		case writeOpPattern.MatchString(line):
			writeBytes += bytesCount
		}
	}

	waitErr := cmd.Wait()
	if scanErr := scanner.Err(); scanErr != nil && !errors.Is(sampleCtx.Err(), context.DeadlineExceeded) {
		return 0, 0, fmt.Errorf("读取 fs_usage 输出失败: %w", scanErr)
	}

	if waitErr != nil && !errors.Is(sampleCtx.Err(), context.DeadlineExceeded) {
		errMsg := strings.TrimSpace(stderr.String())
		if strings.Contains(errMsg, "ktrace_start: Resource busy") {
			return 0, 0, ErrTraceBusy
		}
		if errMsg != "" {
			return 0, 0, fmt.Errorf("fs_usage 执行失败: %s", errMsg)
		}
		return 0, 0, fmt.Errorf("fs_usage 执行失败: %w", waitErr)
	}

	return readBytes, writeBytes, nil
}

func extractBytes(line string) (int64, bool) {
	matches := bytesPattern.FindStringSubmatch(line)
	if len(matches) != 2 {
		return 0, false
	}

	var value int64
	for _, ch := range matches[1] {
		value = value*10 + int64(ch-'0')
	}
	return value, true
}
