package audit

import (
	"fmt"
	"net/netip"
	"strings"
	"sync"

	"github.com/oschwald/maxminddb-golang/v2"
)

type geoLocation struct {
	IPType      string
	CountryCode string
	Country     string
	City        string
}

const (
	ipTypeUnknown = "unknown"
	ipTypeLocal   = "local"
	ipTypePrivate = "private"
	ipTypePublic  = "public"
)

type geoIPRecord struct {
	Country struct {
		ISOCode string            `maxminddb:"iso_code"`
		Names   map[string]string `maxminddb:"names"`
	} `maxminddb:"country"`
	RegisteredCountry struct {
		ISOCode string            `maxminddb:"iso_code"`
		Names   map[string]string `maxminddb:"names"`
	} `maxminddb:"registered_country"`
	City struct {
		Names map[string]string `maxminddb:"names"`
	} `maxminddb:"city"`
}

var (
	geoIPMu     sync.RWMutex
	geoIPReader *maxminddb.Reader
)

func InitGeoIP(path string) error {
	reader, err := maxminddb.Open(strings.TrimSpace(path))
	if err != nil {
		return fmt.Errorf("open GeoLite2 City database: %w", err)
	}

	geoIPMu.Lock()
	old := geoIPReader
	geoIPReader = reader
	geoIPMu.Unlock()

	if old != nil {
		_ = old.Close()
	}
	return nil
}

func lookupGeoIP(rawIP string) geoLocation {
	ip, err := netip.ParseAddr(strings.TrimSpace(rawIP))
	if err != nil {
		return geoLocation{IPType: ipTypeUnknown}
	}
	ip = ip.Unmap()
	if ip.IsLoopback() || ip.IsUnspecified() {
		return geoLocation{IPType: ipTypeLocal}
	}
	if ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return geoLocation{IPType: ipTypePrivate}
	}

	geoIPMu.RLock()
	reader := geoIPReader
	if reader == nil {
		geoIPMu.RUnlock()
		return geoLocation{IPType: ipTypePublic}
	}
	result := reader.Lookup(ip)
	var record geoIPRecord
	err = result.Decode(&record)
	geoIPMu.RUnlock()
	if err != nil {
		return geoLocation{IPType: ipTypePublic}
	}

	countryCode := strings.TrimSpace(record.Country.ISOCode)
	countryNames := record.Country.Names
	if countryCode == "" {
		countryCode = strings.TrimSpace(record.RegisteredCountry.ISOCode)
		countryNames = record.RegisteredCountry.Names
	}
	return geoLocation{
		IPType:      ipTypePublic,
		CountryCode: countryCode,
		Country:     localizedGeoName(countryNames),
		City:        localizedGeoName(record.City.Names),
	}
}

func localizedGeoName(names map[string]string) string {
	for _, language := range []string{"zh-CN", "en"} {
		if value := strings.TrimSpace(names[language]); value != "" {
			return value
		}
	}
	for _, value := range names {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
