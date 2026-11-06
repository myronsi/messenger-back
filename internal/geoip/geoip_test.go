package geoip_test

import (
	"bytes"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maxmind/mmdbwriter/mmdbtype"

	"github.com/myronsi/messenger-back/internal/geoip"
	"github.com/myronsi/messenger-back/internal/geoip/geoiptest"
)

func str(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

func fixture(t *testing.T) *geoip.Locator {
	return geoiptest.Open(t, map[string]mmdbtype.Map{
		"81.2.69.0/24":    geoiptest.City("Berlin", "DE", "Germany"),
		"2a02:8100::/32":  geoiptest.City("", "de", "Germany"),
		"10.0.0.0/8":      geoiptest.City("Intranet", "ZZ", "Nowhere"),
		"198.51.100.0/24": {"continent": mmdbtype.Map{"code": mmdbtype.String("EU")}},
	})
}

func TestLookup(t *testing.T) {
	l := fixture(t)
	got := l.Lookup(netip.MustParseAddr("81.2.69.160"))
	if got == nil || str(got.City) != "Berlin" || str(got.CountryCode) != "DE" || str(got.Country) != "Germany" {
		t.Fatalf("lookup = %+v", got)
	}
	if got := l.Lookup(netip.MustParseAddr("::ffff:81.2.69.1")); got == nil || str(got.City) != "Berlin" {
		t.Fatalf("mapped address = %+v", got)
	}
	got = l.Lookup(netip.MustParseAddr("2a02:8100::1"))
	if got == nil || got.City != nil || str(got.CountryCode) != "DE" || str(got.Country) != "Germany" {
		t.Fatalf("country only = %+v", got)
	}
	for _, ip := range []string{"8.8.8.8", "198.51.100.7"} {
		if got := l.Lookup(netip.MustParseAddr(ip)); got != nil {
			t.Fatalf("%s: want nil, got %+v", ip, got)
		}
	}
}

func TestLookupSkipsNonPublicAddresses(t *testing.T) {
	l := fixture(t)
	for _, ip := range []string{"10.1.2.3", "127.0.0.1", "::1", "169.254.1.1", "fe80::1", "192.168.0.1", "fd00::1", "0.0.0.0", "224.0.0.1", "::ffff:10.0.0.1"} {
		if got := l.Lookup(netip.MustParseAddr(ip)); got != nil {
			t.Fatalf("%s: want nil, got %+v", ip, got)
		}
	}
	if got := l.Lookup(netip.Addr{}); got != nil {
		t.Fatalf("invalid address: %+v", got)
	}
}

func TestMissingDatabase(t *testing.T) {
	for name, path := range map[string]string{"unset": "", "missing": filepath.Join(t.TempDir(), "absent.mmdb")} {
		t.Run(name, func(t *testing.T) {
			var logs bytes.Buffer
			l := geoip.Open(path, slog.New(slog.NewTextHandler(&logs, nil)))
			if l.Enabled() {
				t.Fatal("no database must be loaded")
			}
			if got := l.Lookup(netip.MustParseAddr("81.2.69.160")); got != nil {
				t.Fatalf("want nil, got %+v", got)
			}
			if n := strings.Count(logs.String(), "level=WARN"); n != 1 {
				t.Fatalf("want one warning, got %d: %s", n, logs.String())
			}
			if err := l.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
	var none *geoip.Locator
	if none.Lookup(netip.MustParseAddr("81.2.69.160")) != nil || none.Close() != nil || none.Enabled() {
		t.Fatal("a nil locator finds nothing")
	}
}

func TestCorruptDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.mmdb")
	if err := os.WriteFile(path, []byte("not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if l := geoip.Open(path, nil); l.Enabled() {
		t.Fatal("a corrupt file must not be loaded")
	}
}
