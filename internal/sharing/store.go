package sharing

import (
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"nas-server/database"
	"nas-server/internal/users"
	"nas-server/pkg/idgen"
)

func ListShares() ([]Share, error) {
	var items []Share
	if err := database.DB.Select(&items, `SELECT id, name, storage_pool_id, path, protocols, user_ids, client_networks, status, created_at, updated_at FROM file_shares ORDER BY created_at DESC`); err != nil {
		return nil, err
	}
	for i := range items {
		if err := hydrateShare(&items[i]); err != nil {
			return nil, err
		}
	}
	return items, nil
}

func GetShare(id string) (*Share, error) {
	var item Share
	if err := database.DB.Get(&item, `SELECT id, name, storage_pool_id, path, protocols, user_ids, client_networks, status, created_at, updated_at FROM file_shares WHERE id = ?`, strings.TrimSpace(id)); err != nil {
		return nil, err
	}
	if err := hydrateShare(&item); err != nil {
		return nil, err
	}
	return &item, nil
}

func UpsertShare(id string, req UpsertShareRequest) (*Share, error) {
	name := strings.TrimSpace(req.Name)
	if err := validateShareName(name); err != nil {
		return nil, err
	}
	path := filepath.Clean(strings.TrimSpace(req.Path))
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("share path must be absolute")
	}
	status, err := normalizeStatus(req.Status, req.Enabled)
	if err != nil {
		return nil, err
	}
	protocols, err := normalizeProtocols(req.Protocols)
	if err != nil {
		return nil, err
	}
	clientNetworks, err := normalizeClientNetworks(req.ClientNetworks)
	if err != nil {
		return nil, err
	}
	userIDs := uniqueStrings(req.UserIDs)
	protocolsJSON, _ := json.Marshal(protocols)
	userIDsJSON, _ := json.Marshal(userIDs)
	clientNetworksJSON, _ := json.Marshal(clientNetworks)

	now := time.Now().UTC().Format(time.RFC3339)
	shareID := strings.TrimSpace(id)
	if shareID == "" {
		shareID = idgen.New()
		if _, err := database.DB.Exec(
			`INSERT INTO file_shares (id, name, storage_pool_id, path, protocols, user_ids, client_networks, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			shareID, name, strings.TrimSpace(req.StoragePoolID), path, string(protocolsJSON), string(userIDsJSON), string(clientNetworksJSON), string(status), now, now,
		); err != nil {
			return nil, fmt.Errorf("create file share: %w", err)
		}
	} else {
		result, err := database.DB.Exec(
			`UPDATE file_shares SET name = ?, storage_pool_id = ?, path = ?, protocols = ?, user_ids = ?, client_networks = ?, status = ?, updated_at = ? WHERE id = ?`,
			name, strings.TrimSpace(req.StoragePoolID), path, string(protocolsJSON), string(userIDsJSON), string(clientNetworksJSON), string(status), now, shareID,
		)
		if err != nil {
			return nil, fmt.Errorf("update file share: %w", err)
		}
		if rows, _ := result.RowsAffected(); rows == 0 {
			return nil, fmt.Errorf("file share not found")
		}
	}
	return GetShare(shareID)
}

func DeleteShare(id string) error {
	_, err := database.DB.Exec(`DELETE FROM file_shares WHERE id = ?`, strings.TrimSpace(id))
	return err
}

func ProtocolSummaries(host string) ([]ProtocolSummary, error) {
	shares, err := ListShares()
	if err != nil {
		return nil, err
	}
	counts := enabledProtocolCounts(shares)
	items := []ProtocolSummary{
		{Protocol: ProtocolSMB, ShareURL: "smb://" + host + ":445"},
		{Protocol: ProtocolFTP, ShareURL: "ftp://" + host + ":21"},
		{Protocol: ProtocolWebDAV, ShareURL: "http://" + host + ":8088"},
		{Protocol: ProtocolNFS, ShareURL: host + ":/"},
	}
	for i := range items {
		items[i].Count = counts[items[i].Protocol]
		items[i].Enabled = items[i].Count > 0
	}
	return items, nil
}

func hydrateShare(item *Share) error {
	if err := json.Unmarshal([]byte(emptyJSONArray(item.ProtocolsJSON)), &item.Protocols); err != nil {
		return fmt.Errorf("parse share protocols: %w", err)
	}
	if err := json.Unmarshal([]byte(emptyJSONArray(item.UserIDsJSON)), &item.UserIDs); err != nil {
		return fmt.Errorf("parse share users: %w", err)
	}
	if err := json.Unmarshal([]byte(emptyJSONArray(item.ClientNetworksJSON)), &item.ClientNetworks); err != nil {
		return fmt.Errorf("parse share client networks: %w", err)
	}
	item.Enabled = item.Status == ShareStatusEnabled
	item.Users = shareUsers(item.UserIDs)
	return nil
}

func shareUsers(userIDs []string) []ShareUser {
	items, err := users.ListByIDs(userIDs)
	if err != nil {
		return []ShareUser{}
	}
	out := make([]ShareUser, 0, len(items))
	for _, item := range items {
		out = append(out, ShareUser{
			ID:          item.ID,
			Username:    item.Username,
			DisplayName: item.DisplayName,
			Avatar:      item.Avatar,
			Status:      item.Status,
		})
	}
	return out
}

func emptyJSONArray(value string) string {
	if strings.TrimSpace(value) == "" {
		return "[]"
	}
	return value
}

func normalizeStatus(status string, enabled *bool) (ShareStatus, error) {
	if enabled != nil {
		if *enabled {
			return ShareStatusEnabled, nil
		}
		return ShareStatusDisabled, nil
	}
	switch ShareStatus(strings.ToLower(strings.TrimSpace(status))) {
	case "", ShareStatusEnabled:
		return ShareStatusEnabled, nil
	case ShareStatusDisabled:
		return ShareStatusDisabled, nil
	default:
		return "", fmt.Errorf("status must be enabled or disabled")
	}
}

func normalizeProtocols(values []Protocol) ([]Protocol, error) {
	seen := map[Protocol]struct{}{}
	protocols := make([]Protocol, 0, len(values))
	for _, value := range values {
		protocol := Protocol(strings.ToLower(strings.TrimSpace(string(value))))
		switch protocol {
		case ProtocolSMB, ProtocolFTP, ProtocolWebDAV, ProtocolNFS:
		default:
			return nil, fmt.Errorf("unsupported protocol: %s", value)
		}
		if _, ok := seen[protocol]; ok {
			continue
		}
		seen[protocol] = struct{}{}
		protocols = append(protocols, protocol)
	}
	if len(protocols) == 0 {
		return nil, fmt.Errorf("at least one protocol is required")
	}
	sort.Slice(protocols, func(i, j int) bool { return protocols[i] < protocols[j] })
	return protocols, nil
}

func normalizeClientNetworks(values []string) ([]string, error) {
	seen := map[string]struct{}{}
	networks := []string{}
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		normalized, err := normalizeClientNetwork(value)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		networks = append(networks, normalized)
	}
	return networks, nil
}

func normalizeClientNetwork(value string) (string, error) {
	if value == "*" {
		return value, nil
	}
	if strings.Contains(value, "/") {
		_, ipNet, err := net.ParseCIDR(value)
		if err != nil {
			return "", fmt.Errorf("invalid client network: %s", value)
		}
		return ipNet.String(), nil
	}
	ip := net.ParseIP(value)
	if ip == nil {
		return "", fmt.Errorf("invalid client IP: %s", value)
	}
	return ip.String(), nil
}

func validateShareName(name string) error {
	if name == "" {
		return fmt.Errorf("share name is required")
	}
	if utf8.RuneCountInString(name) > 64 {
		return fmt.Errorf("share name must be 64 characters or fewer")
	}
	if name == "." || name == ".." {
		return fmt.Errorf("share name cannot be . or ..")
	}
	if strings.ContainsAny(name, `/\:*?"<>|[]`) {
		return fmt.Errorf(`share name cannot contain / \ : * ? " < > | [ ]`)
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("share name cannot contain control characters")
		}
	}
	return nil
}

func uniqueStrings(values []string) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
