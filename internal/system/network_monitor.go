package system

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	commandrunner "nas-server/pkg/shell"
)

type interfaceCounterSample struct {
	rx int64
	tx int64
}

type vnstatJSON struct {
	Interfaces []vnstatInterface `json:"interfaces"`
}

type vnstatInterface struct {
	Name    string         `json:"name"`
	Alias   string         `json:"alias"`
	Traffic map[string]any `json:"traffic"`
}

type vnstatTrafficEntry struct {
	Rx        uint64 `json:"rx"`
	Tx        uint64 `json:"tx"`
	Timestamp int64  `json:"timestamp"`
	Date      struct {
		Year  int `json:"year"`
		Month int `json:"month"`
		Day   int `json:"day"`
	} `json:"date"`
	Time struct {
		Hour   int `json:"hour"`
		Minute int `json:"minute"`
	} `json:"time"`
}

func CollectNetworkInterfaces(ctx context.Context, networkRange NetworkRange, sampleInterval time.Duration) (NetworkInterfacesSnapshot, error) {
	if sampleInterval <= 0 {
		sampleInterval = time.Second
	}
	if sampleInterval > 10*time.Second {
		sampleInterval = 10 * time.Second
	}

	ifaces, aliases, err := listVNStatInterfaces(ctx)
	if err != nil {
		return NetworkInterfacesSnapshot{}, err
	}
	if len(ifaces) == 0 {
		ifaces = listSystemInterfaces()
	}

	before := readInterfaceCounterSamples(ifaces)
	select {
	case <-ctx.Done():
		return NetworkInterfacesSnapshot{}, ctx.Err()
	case <-time.After(sampleInterval):
	}
	after := readInterfaceCounterSamples(ifaces)

	stepSeconds := networkRangeStepSeconds(networkRange)
	statuses := make([]NetworkInterfaceStatus, 0, len(ifaces))
	history := map[string][]NetworkTrafficPoint{}
	if networkRange != NetworkRangeRealtime {
		history = collectVNStatHistory(ctx, networkRange)
	}
	for _, name := range ifaces {
		status := buildNetworkInterfaceStatus(name, aliases[name], before[name], after[name], sampleInterval)
		status.Points = history[name]
		statuses = append(statuses, status)
	}
	return NetworkInterfacesSnapshot{
		Range:       networkRange,
		StepSeconds: stepSeconds,
		Interfaces:  statuses,
		CheckedAt:   time.Now().UTC().Format(time.RFC3339),
	}, nil
}

func listVNStatInterfaces(ctx context.Context) ([]string, map[string]string, error) {
	result, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{SuppressSuccessLogs: true}, "vnstat", "--dbiflist")
	if err != nil {
		return nil, nil, fmt.Errorf("list vnstat interfaces: %w", err)
	}
	aliases := map[string]string{}
	seen := map[string]struct{}{}
	items := []string{}
	for _, token := range strings.FieldsFunc(result.Stdout, func(r rune) bool {
		return r == '\n' || r == '\r' || r == '\t' || r == ' ' || r == ',' || r == ';'
	}) {
		token = strings.TrimSpace(strings.Trim(token, `"'[]`))
		if token == "" || strings.Contains(token, ":") {
			continue
		}
		lower := strings.ToLower(token)
		if lower == "available" || lower == "interfaces" || lower == "interface" || lower == "in" || lower == "database" {
			continue
		}
		if _, ok := seen[token]; ok {
			continue
		}
		seen[token] = struct{}{}
		items = append(items, token)
	}
	return items, aliases, nil
}

func listSystemInterfaces() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	items := make([]string, 0, len(ifaces))
	for _, iface := range ifaces {
		if iface.Name == "lo" {
			continue
		}
		items = append(items, iface.Name)
	}
	return items
}

func buildNetworkInterfaceStatus(name string, alias string, before interfaceCounterSample, after interfaceCounterSample, interval time.Duration) NetworkInterfaceStatus {
	seconds := interval.Seconds()
	if seconds <= 0 {
		seconds = 1
	}
	rxSpeed := int64(float64(after.rx-before.rx) / seconds)
	txSpeed := int64(float64(after.tx-before.tx) / seconds)
	if rxSpeed < 0 {
		rxSpeed = 0
	}
	if txSpeed < 0 {
		txSpeed = 0
	}
	iface, _ := net.InterfaceByName(name)
	status := NetworkInterfaceStatus{
		Name:  name,
		Alias: strings.TrimSpace(alias),
		IPs:   interfaceIPs(name),
		Speed: NetworkInterfaceSpeed{RxBytesPerSec: rxSpeed, TxBytesPerSec: txSpeed},
	}
	if iface != nil {
		status.MAC = iface.HardwareAddr.String()
		status.MTU = iface.MTU
	}
	status.OperState = readTrimmedFile("/sys/class/net/" + name + "/operstate")
	return status
}

func interfaceIPs(name string) []string {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return []string{}
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return []string{}
	}
	ips := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		ips = append(ips, addr.String())
	}
	return ips
}

func readTrimmedFile(path string) string {
	content, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(content))
}

func readInterfaceCounterSamples(ifaces []string) map[string]interfaceCounterSample {
	samples := map[string]interfaceCounterSample{}
	content, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return samples
	}
	wanted := map[string]struct{}{}
	for _, iface := range ifaces {
		wanted[iface] = struct{}{}
	}
	for _, line := range strings.Split(string(content), "\n") {
		parts := strings.Split(line, ":")
		if len(parts) != 2 {
			continue
		}
		name := strings.TrimSpace(parts[0])
		if _, ok := wanted[name]; !ok {
			continue
		}
		fields := strings.Fields(parts[1])
		if len(fields) < 16 {
			continue
		}
		rx, _ := strconv.ParseInt(fields[0], 10, 64)
		tx, _ := strconv.ParseInt(fields[8], 10, 64)
		samples[name] = interfaceCounterSample{rx: rx, tx: tx}
	}
	return samples
}

func collectVNStatHistory(ctx context.Context, networkRange NetworkRange) map[string][]NetworkTrafficPoint {
	mode, limit := vnstatModeAndLimit(networkRange)
	if mode == "" {
		return nil
	}
	result, err := commandrunner.RunWithOptions(ctx, commandrunner.Options{}, "vnstat", "--json")
	if err != nil {
		return map[string][]NetworkTrafficPoint{}
	}
	var payload vnstatJSON
	if err := json.Unmarshal([]byte(result.Stdout), &payload); err != nil {
		return map[string][]NetworkTrafficPoint{}
	}
	pointsByInterface := map[string][]NetworkTrafficPoint{}
	for _, iface := range payload.Interfaces {
		rawEntries := vnstatTrafficForMode(iface.Traffic, mode)
		entries := decodeVNStatEntries(rawEntries)
		entries = limitVNStatEntries(entries, limit)
		points := entriesToTrafficPoints(entries, networkRangeSourceStepSeconds(networkRange))
		pointsByInterface[iface.Name] = aggregateTrafficPoints(points, networkRangeStepSeconds(networkRange))
	}
	return pointsByInterface
}

func vnstatModeAndLimit(networkRange NetworkRange) (string, int) {
	switch networkRange {
	case NetworkRangeHour:
		return "5", 12
	case NetworkRangeDay:
		return "h", 24
	case NetworkRangeWeek:
		return "d", 7
	case NetworkRangeMonth:
		return "d", 30
	default:
		return "", 0
	}
}

func networkRangeStepSeconds(networkRange NetworkRange) int64 {
	switch networkRange {
	case NetworkRangeHour:
		return 5 * 60
	case NetworkRangeDay:
		return 2 * 60 * 60
	case NetworkRangeWeek:
		return 24 * 60 * 60
	case NetworkRangeMonth:
		return 2 * 24 * 60 * 60
	default:
		return 1
	}
}

func networkRangeSourceStepSeconds(networkRange NetworkRange) int64 {
	switch networkRange {
	case NetworkRangeHour:
		return 5 * 60
	case NetworkRangeDay:
		return 60 * 60
	default:
		return 24 * 60 * 60
	}
}

func firstExistingTraffic(traffic map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := traffic[key]; ok {
			return value
		}
	}
	return nil
}

func vnstatTrafficForMode(traffic map[string]any, mode string) any {
	switch mode {
	case "5":
		return firstExistingTraffic(traffic, "5", "fiveminute", "fiveminutes", "five_minute", "five_minutes", "5minute", "5minutes", "5min")
	case "h":
		return firstExistingTraffic(traffic, "h", "hour", "hours", "hourly")
	case "d":
		return firstExistingTraffic(traffic, "d", "day", "days", "daily")
	default:
		return traffic[mode]
	}
}

func limitVNStatEntries(entries []vnstatTrafficEntry, limit int) []vnstatTrafficEntry {
	if limit <= 0 || len(entries) <= limit {
		return entries
	}
	return entries[len(entries)-limit:]
}

func decodeVNStatEntries(value any) []vnstatTrafficEntry {
	if value == nil {
		return nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var entries []vnstatTrafficEntry
	if err := json.Unmarshal(encoded, &entries); err != nil {
		return nil
	}
	return entries
}

func entriesToTrafficPoints(entries []vnstatTrafficEntry, durationSeconds int64) []NetworkTrafficPoint {
	points := make([]NetworkTrafficPoint, 0, len(entries))
	for _, entry := range entries {
		timestamp := vnstatEntryTime(entry)
		points = append(points, NetworkTrafficPoint{
			Timestamp:       timestamp.UTC().Format(time.RFC3339),
			RxBytes:         entry.Rx,
			TxBytes:         entry.Tx,
			RxBytesPerSec:   round1(float64(entry.Rx) / float64(durationSeconds)),
			TxBytesPerSec:   round1(float64(entry.Tx) / float64(durationSeconds)),
			DurationSeconds: durationSeconds,
		})
	}
	return points
}

func aggregateTrafficPoints(points []NetworkTrafficPoint, targetDuration int64) []NetworkTrafficPoint {
	if len(points) == 0 || targetDuration <= 0 {
		return points
	}
	result := []NetworkTrafficPoint{}
	var current *NetworkTrafficPoint
	for _, point := range points {
		if current == nil {
			copyPoint := point
			current = &copyPoint
			continue
		}
		if current.DurationSeconds < targetDuration {
			current.RxBytes += point.RxBytes
			current.TxBytes += point.TxBytes
			current.DurationSeconds += point.DurationSeconds
			continue
		}
		current.RxBytesPerSec = round1(float64(current.RxBytes) / float64(current.DurationSeconds))
		current.TxBytesPerSec = round1(float64(current.TxBytes) / float64(current.DurationSeconds))
		result = append(result, *current)
		copyPoint := point
		current = &copyPoint
	}
	if current != nil {
		current.RxBytesPerSec = round1(float64(current.RxBytes) / float64(current.DurationSeconds))
		current.TxBytesPerSec = round1(float64(current.TxBytes) / float64(current.DurationSeconds))
		result = append(result, *current)
	}
	return result
}

func vnstatEntryTime(entry vnstatTrafficEntry) time.Time {
	if entry.Timestamp > 0 {
		return time.Unix(entry.Timestamp, 0)
	}
	year := entry.Date.Year
	if year == 0 {
		year = time.Now().Year()
	}
	month := time.Month(entry.Date.Month)
	if month == 0 {
		month = time.Now().Month()
	}
	day := entry.Date.Day
	if day == 0 {
		day = 1
	}
	return time.Date(year, month, day, entry.Time.Hour, entry.Time.Minute, 0, 0, time.Local)
}
