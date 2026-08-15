package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"nas-server/pkg/shell"
)

type inspectContainer struct {
	ID      string `json:"Id"`
	Name    string `json:"Name"`
	Created string `json:"Created"`
	Config  struct {
		Image string `json:"Image"`
	} `json:"Config"`
	State struct {
		Status     string `json:"Status"`
		Running    bool   `json:"Running"`
		StartedAt  string `json:"StartedAt"`
		FinishedAt string `json:"FinishedAt"`
	} `json:"State"`
	NetworkSettings struct {
		Ports map[string][]struct {
			HostIP   string `json:"HostIp"`
			HostPort string `json:"HostPort"`
		} `json:"Ports"`
	} `json:"NetworkSettings"`
	Mounts []struct {
		Type        string `json:"Type"`
		Source      string `json:"Source"`
		Destination string `json:"Destination"`
		Mode        string `json:"Mode"`
		RW          bool   `json:"RW"`
		Name        string `json:"Name"`
	} `json:"Mounts"`
}

type inspectNetwork struct {
	ID         string `json:"Id"`
	Name       string `json:"Name"`
	Driver     string `json:"Driver"`
	Scope      string `json:"Scope"`
	Internal   bool   `json:"Internal"`
	EnableIPv6 bool   `json:"EnableIPv6"`
	IPAM       struct {
		Config []struct {
			Subnet  string `json:"Subnet"`
			Gateway string `json:"Gateway"`
		} `json:"Config"`
	} `json:"IPAM"`
	Containers map[string]any `json:"Containers"`
}

type inspectVolume struct {
	Name       string `json:"Name"`
	Driver     string `json:"Driver"`
	Mountpoint string `json:"Mountpoint"`
	Scope      string `json:"Scope"`
	CreatedAt  string `json:"CreatedAt"`
}

type imageRow struct {
	ID         string `json:"ID"`
	Repository string `json:"Repository"`
	Tag        string `json:"Tag"`
	Digest     string `json:"Digest"`
	Size       string `json:"Size"`
	CreatedAt  string `json:"CreatedAt"`
	Created    string `json:"CreatedSince"`
}

type networkRow struct {
	ID string `json:"ID"`
}

type volumeRow struct {
	Name string `json:"Name"`
}

type composeProjectRow struct {
	Name        string `json:"Name"`
	Status      string `json:"Status"`
	ConfigFiles string `json:"ConfigFiles"`
	WorkingDir  string `json:"WorkingDir"`
	Environment string `json:"Environment"`
	Services    string `json:"Services"`
}

type statsRow struct {
	ID       string `json:"ID"`
	Name     string `json:"Name"`
	CPUPerc  string `json:"CPUPerc"`
	MemUsage string `json:"MemUsage"`
	MemPerc  string `json:"MemPerc"`
	NetIO    string `json:"NetIO"`
}

func ListImages(ctx context.Context) ([]Image, error) {
	result, err := shell.RunWithOptions(ctx, shell.Options{UseSudo: true}, "docker", "image", "ls", "--no-trunc", "--format", "{{json .}}")
	if err != nil {
		return nil, err
	}
	rows, err := decodeJSONLines[imageRow](result.Stdout)
	if err != nil {
		return nil, fmt.Errorf("decode docker images: %w", err)
	}
	icons := imageIconMap()
	items := make([]Image, 0, len(rows))
	for _, row := range rows {
		repository := normalizeDockerPlaceholder(row.Repository)
		tag := normalizeDockerPlaceholder(row.Tag)
		icon := icons[imageRef(repository, tag)]
		if icon == "" {
			icon = defaultImageIcon
		}
		items = append(items, Image{
			ID:         strings.TrimPrefix(row.ID, "sha256:"),
			Repository: repository,
			Tag:        tag,
			Digest:     normalizeDockerPlaceholder(row.Digest),
			Size:       row.Size,
			CreatedAt:  row.CreatedAt,
			Created:    row.Created,
			Icon:       icon,
		})
	}
	sort.SliceStable(items, func(i, j int) bool {
		left := strings.ToLower(items[i].Repository + ":" + items[i].Tag)
		right := strings.ToLower(items[j].Repository + ":" + items[j].Tag)
		return left < right
	})
	return items, nil
}

func ListNetworks(ctx context.Context) ([]Network, error) {
	listResult, err := shell.RunWithOptions(ctx, shell.Options{UseSudo: true}, "docker", "network", "ls", "--no-trunc", "--format", "{{json .}}")
	if err != nil {
		return nil, err
	}
	rows, err := decodeJSONLines[networkRow](listResult.Stdout)
	if err != nil {
		return nil, fmt.Errorf("decode docker networks: %w", err)
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		if strings.TrimSpace(row.ID) != "" {
			ids = append(ids, strings.TrimSpace(row.ID))
		}
	}
	if len(ids) == 0 {
		return []Network{}, nil
	}
	inspectArgs := append([]string{"network", "inspect"}, ids...)
	inspectResult, err := shell.RunWithOptions(ctx, shell.Options{UseSudo: true}, "docker", inspectArgs...)
	if err != nil {
		return nil, err
	}
	var inspected []inspectNetwork
	if err := json.Unmarshal([]byte(inspectResult.Stdout), &inspected); err != nil {
		return nil, fmt.Errorf("decode docker network inspect: %w", err)
	}
	items := make([]Network, 0, len(inspected))
	for _, entry := range inspected {
		item := Network{
			ID:         entry.ID,
			Name:       entry.Name,
			Driver:     entry.Driver,
			Scope:      entry.Scope,
			Internal:   entry.Internal,
			IPv6:       entry.EnableIPv6,
			Containers: len(entry.Containers),
		}
		if len(entry.IPAM.Config) > 0 {
			item.Subnet = entry.IPAM.Config[0].Subnet
			item.Gateway = entry.IPAM.Config[0].Gateway
		}
		items = append(items, item)
	}
	sort.SliceStable(items, func(i, j int) bool {
		return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name)
	})
	return items, nil
}

func ListVolumes(ctx context.Context) ([]Volume, error) {
	listResult, err := shell.RunWithOptions(ctx, shell.Options{UseSudo: true}, "docker", "volume", "ls", "--format", "{{json .}}")
	if err != nil {
		return nil, err
	}
	rows, err := decodeJSONLines[volumeRow](listResult.Stdout)
	if err != nil {
		return nil, fmt.Errorf("decode docker volumes: %w", err)
	}
	names := make([]string, 0, len(rows))
	for _, row := range rows {
		if strings.TrimSpace(row.Name) != "" {
			names = append(names, strings.TrimSpace(row.Name))
		}
	}
	if len(names) == 0 {
		return []Volume{}, nil
	}
	inspectArgs := append([]string{"volume", "inspect"}, names...)
	inspectResult, err := shell.RunWithOptions(ctx, shell.Options{UseSudo: true}, "docker", inspectArgs...)
	if err != nil {
		return nil, err
	}
	var inspected []inspectVolume
	if err := json.Unmarshal([]byte(inspectResult.Stdout), &inspected); err != nil {
		return nil, fmt.Errorf("decode docker volume inspect: %w", err)
	}
	items := make([]Volume, 0, len(inspected))
	for _, entry := range inspected {
		items = append(items, Volume{
			Name:       entry.Name,
			Driver:     entry.Driver,
			Mountpoint: entry.Mountpoint,
			Scope:      entry.Scope,
			CreatedAt:  normalizeTimestamp(entry.CreatedAt),
		})
	}
	sort.SliceStable(items, func(i, j int) bool {
		return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name)
	})
	return items, nil
}

func ListComposeProjects(ctx context.Context) ([]ComposeProject, error) {
	result, err := shell.RunWithOptions(ctx, shell.Options{UseSudo: true}, "docker", "compose", "ls", "--format", "json")
	if err != nil {
		if strings.Contains(result.Stderr, "not a docker command") || strings.Contains(result.Stderr, "unknown command") {
			return []ComposeProject{}, nil
		}
		return nil, err
	}

	rows, err := decodeComposeProjects(result.Stdout)
	if err != nil {
		return nil, fmt.Errorf("decode docker compose projects: %w", err)
	}
	items := make([]ComposeProject, 0, len(rows))
	for _, row := range rows {
		name := strings.TrimSpace(row.Name)
		if name == "" {
			continue
		}
		items = append(items, ComposeProject{
			ID:          name,
			Name:        name,
			Status:      strings.TrimSpace(row.Status),
			ConfigFiles: strings.TrimSpace(row.ConfigFiles),
			WorkingDir:  strings.TrimSpace(row.WorkingDir),
			Environment: strings.TrimSpace(row.Environment),
			Services:    strings.TrimSpace(row.Services),
		})
	}
	sort.SliceStable(items, func(i, j int) bool {
		return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name)
	})
	return items, nil
}

type containerStats struct {
	CPUPercent       float64
	MemoryUsageBytes uint64
	MemoryLimitBytes uint64
	MemoryPercent    float64
	NetworkRxBytes   uint64
	NetworkTxBytes   uint64
}

func ListContainers(ctx context.Context) ([]Container, error) {
	return listContainers(ctx, true)
}

func ListContainersBasic(ctx context.Context) ([]Container, error) {
	return listContainers(ctx, false)
}

func listContainers(ctx context.Context, includeStats bool) ([]Container, error) {
	listResult, err := shell.RunWithOptions(ctx, shell.Options{UseSudo: true}, "docker", "ps", "-aq", "--no-trunc")
	if err != nil {
		return nil, err
	}
	ids := strings.Fields(strings.TrimSpace(listResult.Stdout))
	if len(ids) == 0 {
		return []Container{}, nil
	}

	inspectArgs := append([]string{"inspect"}, ids...)
	inspectResult, err := shell.RunWithOptions(ctx, shell.Options{UseSudo: true}, "docker", inspectArgs...)
	if err != nil {
		return nil, err
	}
	var inspected []inspectContainer
	if err := json.Unmarshal([]byte(inspectResult.Stdout), &inspected); err != nil {
		return nil, fmt.Errorf("decode docker inspect: %w", err)
	}

	statsByID := map[string]containerStats{}
	if includeStats {
		statsByID, err = listContainerStats(ctx)
		if err != nil {
			return nil, err
		}
	}

	now := time.Now()
	items := make([]Container, 0, len(inspected))
	for _, entry := range inspected {
		item := Container{
			ID:         entry.ID,
			Name:       strings.TrimPrefix(entry.Name, "/"),
			Image:      entry.Config.Image,
			State:      entry.State.Status,
			Status:     entry.State.Status,
			Running:    entry.State.Running,
			CreatedAt:  normalizeTimestamp(entry.Created),
			StartedAt:  normalizeTimestamp(entry.State.StartedAt),
			FinishedAt: normalizeTimestamp(entry.State.FinishedAt),
			Ports:      buildPorts(entry),
			Mounts:     buildMounts(entry),
		}

		startedAt, startedOK := parseDockerTime(entry.State.StartedAt)
		finishedAt, finishedOK := parseDockerTime(entry.State.FinishedAt)
		switch {
		case startedOK && entry.State.Running:
			item.UptimeSeconds = int64(now.Sub(startedAt).Seconds())
		case startedOK && finishedOK && finishedAt.After(startedAt):
			item.UptimeSeconds = int64(finishedAt.Sub(startedAt).Seconds())
		}

		if stats, ok := statsByID[entry.ID]; ok {
			item.CPUPercent = stats.CPUPercent
			item.MemoryUsageBytes = stats.MemoryUsageBytes
			item.MemoryLimitBytes = stats.MemoryLimitBytes
			item.MemoryPercent = stats.MemoryPercent
			item.NetworkRxBytes = stats.NetworkRxBytes
			item.NetworkTxBytes = stats.NetworkTxBytes
		}

		items = append(items, item)
	}

	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Running != items[j].Running {
			return items[i].Running
		}
		return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name)
	})
	return items, nil
}

func listContainerStats(ctx context.Context) (map[string]containerStats, error) {
	runningResult, err := shell.RunWithOptions(ctx, shell.Options{UseSudo: true}, "docker", "ps", "-q", "--no-trunc")
	if err != nil {
		return nil, err
	}
	runningIDs := strings.Fields(strings.TrimSpace(runningResult.Stdout))
	if len(runningIDs) == 0 {
		return map[string]containerStats{}, nil
	}

	statsResult, err := shell.RunWithOptions(ctx, shell.Options{UseSudo: true}, "docker", "stats", "--no-stream", "--no-trunc", "--format", "{{json .}}")
	if err != nil {
		return nil, err
	}

	statsByID := map[string]containerStats{}
	for _, line := range strings.Split(strings.TrimSpace(statsResult.Stdout), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var row statsRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return nil, fmt.Errorf("decode docker stats: %w", err)
		}
		statsByID[row.ID] = containerStats{
			CPUPercent:       parsePercent(row.CPUPerc),
			MemoryUsageBytes: parseUsagePairLeft(row.MemUsage),
			MemoryLimitBytes: parseUsagePairRight(row.MemUsage),
			MemoryPercent:    parsePercent(row.MemPerc),
			NetworkRxBytes:   parseUsagePairLeft(row.NetIO),
			NetworkTxBytes:   parseUsagePairRight(row.NetIO),
		}
	}
	return statsByID, nil
}

func decodeJSONLines[T any](content string) ([]T, error) {
	rows := make([]T, 0)
	for _, line := range strings.Split(strings.TrimSpace(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var row T
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func decodeComposeProjects(content string) ([]composeProjectRow, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return []composeProjectRow{}, nil
	}
	var rows []composeProjectRow
	if strings.HasPrefix(content, "[") {
		if err := json.Unmarshal([]byte(content), &rows); err != nil {
			return nil, err
		}
		return rows, nil
	}
	return decodeJSONLines[composeProjectRow](content)
}

func normalizeDockerPlaceholder(value string) string {
	value = strings.TrimSpace(value)
	if value == "<none>" {
		return ""
	}
	return value
}

func buildPorts(entry inspectContainer) []ContainerPort {
	if len(entry.NetworkSettings.Ports) == 0 {
		return []ContainerPort{}
	}
	keys := make([]string, 0, len(entry.NetworkSettings.Ports))
	for key := range entry.NetworkSettings.Ports {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	ports := make([]ContainerPort, 0)
	for _, key := range keys {
		containerPort, protocol := splitPortKey(key)
		bindings := entry.NetworkSettings.Ports[key]
		if len(bindings) == 0 {
			ports = append(ports, ContainerPort{
				ContainerPort: containerPort,
				Protocol:      protocol,
			})
			continue
		}
		for _, binding := range bindings {
			hostPort, _ := strconv.Atoi(binding.HostPort)
			ports = append(ports, ContainerPort{
				IP:            binding.HostIP,
				HostPort:      hostPort,
				ContainerPort: containerPort,
				Protocol:      protocol,
			})
		}
	}
	return ports
}

func buildMounts(entry inspectContainer) []ContainerMount {
	if len(entry.Mounts) == 0 {
		return []ContainerMount{}
	}
	mounts := make([]ContainerMount, 0, len(entry.Mounts))
	for _, mount := range entry.Mounts {
		mounts = append(mounts, ContainerMount{
			Type:        mount.Type,
			Source:      mount.Source,
			Destination: mount.Destination,
			Mode:        mount.Mode,
			ReadWrite:   mount.RW,
			Name:        mount.Name,
		})
	}
	sort.SliceStable(mounts, func(i, j int) bool {
		return mounts[i].Destination < mounts[j].Destination
	})
	return mounts
}

func splitPortKey(value string) (int, string) {
	parts := strings.SplitN(value, "/", 2)
	port, _ := strconv.Atoi(parts[0])
	protocol := ""
	if len(parts) == 2 {
		protocol = parts[1]
	}
	return port, protocol
}

func parseDockerTime(value string) (time.Time, bool) {
	if value == "" || value == "0001-01-01T00:00:00Z" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err == nil {
		return parsed, true
	}
	return time.Time{}, false
}

func normalizeTimestamp(value string) string {
	parsed, ok := parseDockerTime(value)
	if !ok {
		return ""
	}
	return parsed.UTC().Format(time.RFC3339)
}

func parsePercent(value string) float64 {
	value = strings.TrimSpace(strings.TrimSuffix(value, "%"))
	if value == "" || value == "--" {
		return 0
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0
	}
	return parsed
}

func parseUsagePairLeft(value string) uint64 {
	left, _ := splitUsagePair(value)
	return parseHumanSize(left)
}

func parseUsagePairRight(value string) uint64 {
	_, right := splitUsagePair(value)
	return parseHumanSize(right)
}

func splitUsagePair(value string) (string, string) {
	parts := strings.SplitN(value, "/", 2)
	if len(parts) != 2 {
		return strings.TrimSpace(value), ""
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
}

func parseHumanSize(value string) uint64 {
	value = strings.TrimSpace(value)
	if value == "" || value == "--" {
		return 0
	}

	numberEnd := 0
	for numberEnd < len(value) {
		ch := value[numberEnd]
		if (ch >= '0' && ch <= '9') || ch == '.' {
			numberEnd++
			continue
		}
		break
	}
	if numberEnd == 0 {
		return 0
	}

	numberPart := value[:numberEnd]
	unitPart := strings.TrimSpace(value[numberEnd:])
	number, err := strconv.ParseFloat(numberPart, 64)
	if err != nil {
		return 0
	}

	multipliers := map[string]float64{
		"":    1,
		"B":   1,
		"KB":  1000,
		"MB":  1000 * 1000,
		"GB":  1000 * 1000 * 1000,
		"TB":  1000 * 1000 * 1000 * 1000,
		"KIB": 1024,
		"MIB": 1024 * 1024,
		"GIB": 1024 * 1024 * 1024,
		"TIB": 1024 * 1024 * 1024 * 1024,
	}
	multiplier, ok := multipliers[strings.ToUpper(unitPart)]
	if !ok {
		return 0
	}
	return uint64(number * multiplier)
}
