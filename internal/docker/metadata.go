package docker

import (
	"context"
	"fmt"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"

	"nas-server/database"
)

const defaultImageIcon = "docker"

var commonImageIconSlugs = map[string]string{
	"adguard":       "adguard",
	"apache":        "apache",
	"arcane":        "docker",
	"caddy":         "caddy",
	"cloudflare":    "cloudflare",
	"debian":        "debian",
	"elasticsearch": "elastic",
	"fedora":        "fedora",
	"gitea":         "gitea",
	"grafana":       "grafana",
	"homeassistant": "homeassistant",
	"influxdb":      "influxdb",
	"jellyfin":      "jellyfin",
	"mariadb":       "mariadb",
	"mongo":         "mongodb",
	"mongodb":       "mongodb",
	"mysql":         "mysql",
	"nextcloud":     "nextcloud",
	"nginx":         "nginx",
	"node":          "nodedotjs",
	"nodejs":        "nodedotjs",
	"openwrt":       "openwrt",
	"plex":          "plex",
	"portainer":     "portainer",
	"postgres":      "postgresql",
	"postgresql":    "postgresql",
	"prometheus":    "prometheus",
	"python":        "python",
	"qbittorrent":   "qbittorrent",
	"redis":         "redis",
	"sonarr":        "sonarr",
	"syncthing":     "syncthing",
	"tomcat":        "apachetomcat",
	"traefik":       "traefikproxy",
	"ubuntu":        "ubuntu",
	"wordpress":     "wordpress",
}

var nonIconSlugChars = regexp.MustCompile(`[^a-z0-9]+`)

func upsertImageMetadata(imageRef, icon string, pulledAt time.Time) error {
	imageRef = normalizeImageRef(imageRef)
	icon = normalizeImageIcon(icon)
	if imageRef == "" {
		return fmt.Errorf("image ref is required")
	}
	if database.DB == nil {
		return fmt.Errorf("database is not initialized")
	}
	_, err := database.DB.Exec(`
INSERT INTO docker_image_metadata (image_ref, icon, created_at, updated_at, last_pulled_at)
VALUES (?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, ?)
ON CONFLICT(image_ref) DO UPDATE SET
	icon = excluded.icon,
	updated_at = CURRENT_TIMESTAMP,
	last_pulled_at = excluded.last_pulled_at
`, imageRef, icon, pulledAt.UTC())
	if err != nil {
		return fmt.Errorf("upsert docker image metadata: %w", err)
	}
	return nil
}

func imageIconMap() map[string]string {
	icons := map[string]string{}
	if database.DB == nil {
		return icons
	}
	rows, err := database.DB.Query(`SELECT image_ref, icon FROM docker_image_metadata`)
	if err != nil {
		return icons
	}
	defer rows.Close()

	for rows.Next() {
		var imageRef string
		var icon string
		if err := rows.Scan(&imageRef, &icon); err != nil {
			continue
		}
		imageRef = normalizeImageRef(imageRef)
		if imageRef != "" {
			icons[imageRef] = normalizeImageIcon(icon)
		}
	}
	return icons
}

func ResolveImageIcon(ctx context.Context, imageRef string) string {
	for _, slug := range imageIconCandidates(imageRef) {
		iconURL := simpleIconURL(slug)
		if remoteIconAvailable(ctx, iconURL) {
			return iconURL
		}
	}
	return defaultImageIcon
}

func imageIconCandidates(imageRef string) []string {
	repository := imageRepositoryName(imageRef)
	parts := strings.Split(repository, "/")
	candidates := make([]string, 0, len(parts)*2)
	seen := map[string]bool{}
	add := func(value string) {
		value = strings.Trim(strings.ToLower(value), " ._-")
		if value == "" {
			return
		}
		if mapped := commonImageIconSlugs[value]; mapped != "" {
			value = mapped
		} else {
			value = nonIconSlugChars.ReplaceAllString(value, "")
		}
		if value == "" || seen[value] {
			return
		}
		seen[value] = true
		candidates = append(candidates, value)
	}

	if len(parts) > 0 {
		add(parts[len(parts)-1])
	}
	for i := len(parts) - 2; i >= 0; i-- {
		add(parts[i])
	}
	return candidates
}

func imageRepositoryName(imageRef string) string {
	imageRef = strings.TrimSpace(imageRef)
	imageRef = strings.TrimPrefix(imageRef, "docker.io/")
	imageRef = strings.TrimPrefix(imageRef, "library/")
	if imageRef == "" {
		return ""
	}
	if at := strings.Index(imageRef, "@"); at >= 0 {
		imageRef = imageRef[:at]
	}
	lastSlash := strings.LastIndex(imageRef, "/")
	if colon := strings.LastIndex(imageRef, ":"); colon > lastSlash {
		imageRef = imageRef[:colon]
	}
	return strings.Trim(imageRef, "/")
}

func simpleIconURL(slug string) string {
	return "https://cdn.simpleicons.org/" + path.Clean("/" + slug)[1:]
}

func remoteIconAvailable(ctx context.Context, iconURL string) bool {
	ctx, cancel := context.WithTimeout(ctx, 1800*time.Millisecond)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, iconURL, nil)
	if err != nil {
		return false
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer res.Body.Close()
	return res.StatusCode >= 200 && res.StatusCode < 300
}

func imageRef(repository, tag string) string {
	repository = strings.TrimSpace(repository)
	tag = strings.TrimSpace(tag)
	if repository == "" {
		return ""
	}
	if tag == "" {
		tag = "latest"
	}
	return repository + ":" + tag
}

func normalizeImageRef(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "docker.io/library/")
	if value == "" {
		return ""
	}
	if !strings.Contains(value, ":") {
		return value + ":latest"
	}
	return value
}

func normalizeImageIcon(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return defaultImageIcon
	}
	if len(value) > 64 {
		return value[:64]
	}
	return value
}
