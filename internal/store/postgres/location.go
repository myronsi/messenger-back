package postgres

import (
	"encoding/json"

	"github.com/myronsi/messenger-back/internal/geoip"
)

func encodeLocation(l *geoip.Location) []byte {
	if l == nil {
		return nil
	}
	b, err := json.Marshal(l)
	if err != nil {
		return nil
	}
	return b
}

func decodeLocation(b []byte) *geoip.Location {
	if len(b) == 0 {
		return nil
	}
	var l geoip.Location
	if err := json.Unmarshal(b, &l); err != nil || l.City == nil && l.CountryCode == nil && l.Country == nil {
		return nil
	}
	return &l
}
