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

type statsRow struct {
	ID       string `json:"ID"`
	Name     string `json:"Name"`
	CPUPerc  string `json:"CPUPerc"`
	MemUsage string `json:"MemUsage"`
	MemPerc  string `json:"MemPerc"`
	NetIO    string `json:"NetIO"`
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
	listResult, err := shell.Run(ctx, "docker", "ps", "-aq", "--no-trunc")
	if err != nil {
		return nil, err
	}
	ids := strings.Fields(strings.TrimSpace(listResult.Stdout))
	if len(ids) == 0 {
		return []Container{}, nil
	}

	inspectArgs := append([]string{"inspect"}, ids...)
	inspectResult, err := shell.Run(ctx, "docker", inspectArgs...)
	if err != nil {
		return nil, err
	}
	var inspected []inspectContainer
	if err := json.Unmarshal([]byte(inspectResult.Stdout), &inspected); err != nil {
		return nil, fmt.Errorf("decode docker inspect: %w", err)
	}

	statsByID, err := listContainerStats(ctx)
	if err != nil {
		return nil, err
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
	runningResult, err := shell.Run(ctx, "docker", "ps", "-q", "--no-trunc")
	if err != nil {
		return nil, err
	}
	runningIDs := strings.Fields(strings.TrimSpace(runningResult.Stdout))
	if len(runningIDs) == 0 {
		return map[string]containerStats{}, nil
	}

	statsResult, err := shell.Run(ctx, "docker", "stats", "--no-stream", "--no-trunc", "--format", "{{json .}}")
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
	return parsed.Format(time.RFC3339)
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
