package geoip

import (
	"log/slog"
	"net/netip"
	"strings"

	"github.com/oschwald/maxminddb-golang/v2"
)

type Location struct {
	City        *string `json:"city"`
	CountryCode *string `json:"country_code"`
	Country     *string `json:"country"`
}

type Locator struct {
	db *maxminddb.Reader
}

type record struct {
	City struct {
		Names struct {
			En string `maxminddb:"en"`
		} `maxminddb:"names"`
	} `maxminddb:"city"`
	Country struct {
		ISOCode string `maxminddb:"iso_code"`
		Names   struct {
			En string `maxminddb:"en"`
		} `maxminddb:"names"`
	} `maxminddb:"country"`
}

func Open(path string, log *slog.Logger) *Locator {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if strings.TrimSpace(path) == "" {
		log.Warn("GEOIP_DB_PATH is not set: sessions and security events are shown without a location")
		return &Locator{}
	}
	db, err := maxminddb.Open(path)
	if err != nil {
		log.Warn("GeoIP database not loaded: sessions and security events are shown without a location", "path", path, "err", err)
		return &Locator{}
	}
	return &Locator{db: db}
}

func (l *Locator) Enabled() bool { return l != nil && l.db != nil }

func (l *Locator) Lookup(ip netip.Addr) *Location {
	if !l.Enabled() || !public(ip) {
		return nil
	}
	var r record
	if err := l.db.Lookup(ip.Unmap().WithZone("")).Decode(&r); err != nil {
		return nil
	}
	loc := Location{City: optional(r.City.Names.En), Country: optional(r.Country.Names.En)}
	if code := strings.ToUpper(strings.TrimSpace(r.Country.ISOCode)); len(code) == 2 {
		loc.CountryCode = &code
	}
	if loc.City == nil && loc.CountryCode == nil && loc.Country == nil {
		return nil
	}
	return &loc
}

func (l *Locator) Close() error {
	if !l.Enabled() {
		return nil
	}
	err := l.db.Close()
	l.db = nil
	return err
}

func public(ip netip.Addr) bool {
	if !ip.IsValid() {
		return false
	}
	ip = ip.Unmap()
	return !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsMulticast() && !ip.IsUnspecified()
}

func optional(s string) *string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return &s
}
