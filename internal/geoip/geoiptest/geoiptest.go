package geoiptest

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/maxmind/mmdbwriter"
	"github.com/maxmind/mmdbwriter/mmdbtype"

	"github.com/myronsi/messenger-back/internal/geoip"
)

func City(city, countryCode, country string) mmdbtype.Map {
	m := mmdbtype.Map{}
	if countryCode != "" || country != "" {
		m["country"] = mmdbtype.Map{
			"iso_code": mmdbtype.String(countryCode),
			"names":    mmdbtype.Map{"en": mmdbtype.String(country), "de": mmdbtype.String(country + " (de)")},
		}
	}
	if city != "" {
		m["city"] = mmdbtype.Map{"names": mmdbtype.Map{"en": mmdbtype.String(city)}}
	}
	return m
}

func Write(t testing.TB, networks map[string]mmdbtype.Map) string {
	t.Helper()
	tree, err := mmdbwriter.New(mmdbwriter.Options{DatabaseType: "DBIP-City-Lite", IncludeReservedNetworks: true, RecordSize: 24})
	if err != nil {
		t.Fatal(err)
	}
	for cidr, value := range networks {
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			t.Fatal(err)
		}
		if err := tree.Insert(network, value); err != nil {
			t.Fatal(err)
		}
	}
	var buf bytes.Buffer
	if _, err := tree.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "dbip-city-lite.mmdb")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func Open(t testing.TB, networks map[string]mmdbtype.Map) *geoip.Locator {
	t.Helper()
	l := geoip.Open(Write(t, networks), nil)
	if !l.Enabled() {
		t.Fatal("test GeoIP database not loaded")
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

func Default(t testing.TB) *geoip.Locator {
	t.Helper()
	return Open(t, map[string]mmdbtype.Map{
		"203.0.113.0/24":  City("Berlin", "DE", "Germany"),
		"198.51.100.0/24": City("Paris", "FR", "France"),
		"2001:db8::/32":   City("", "NL", "Netherlands"),
		"10.0.0.0/8":      City("Intranet", "ZZ", "Nowhere"),
	})
}
