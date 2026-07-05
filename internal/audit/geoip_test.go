package audit

import (
	"path/filepath"
	"testing"
)

func TestLookupGeoIP(t *testing.T) {
	path := filepath.Join("..", "..", "data", "GeoLite2-City.mmdb")
	if err := InitGeoIP(path); err != nil {
		t.Skipf("GeoLite2 City database is not available: %v", err)
	}

	location := lookupGeoIP("8.8.8.8")
	if location.IPType != ipTypePublic {
		t.Fatalf("expected public IP type, got %q", location.IPType)
	}
	if location.CountryCode == "" {
		t.Fatal("expected a country code for public IP")
	}

	privateLocation := lookupGeoIP("192.168.1.10")
	if privateLocation.IPType != ipTypePrivate || privateLocation.Country != "" || privateLocation.City != "" {
		t.Fatalf("expected private IP without geolocation, got %+v", privateLocation)
	}

	localLocation := lookupGeoIP("127.0.0.1")
	if localLocation.IPType != ipTypeLocal || localLocation.Country != "" || localLocation.City != "" {
		t.Fatalf("expected local IP without geolocation, got %+v", localLocation)
	}
}

func TestAuditTextDefaults(t *testing.T) {
	entry := Entry{
		Event:        "storage_pool_delete_failed",
		Action:       "delete",
		ResourceType: "storage_pool",
		ResourceID:   "pool-1",
		ResourceName: "Main Pool",
		Message:      "device is busy",
	}

	keyword := defaultKeyword(entry)
	if keyword != "Storage pool delete failed" {
		t.Fatalf("unexpected keyword: %q", keyword)
	}
	content := defaultContent(entry, keyword)
	if content != "device is busy · Main Pool" {
		t.Fatalf("unexpected content: %q", content)
	}
}
