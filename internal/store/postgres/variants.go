package postgres

import (
	"encoding/json"
	"sort"
)

type StoredVariant struct {
	Key         string `json:"key"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	Width       int    `json:"width,omitempty"`
	Height      int    `json:"height,omitempty"`
}

type Variants map[string]StoredVariant

func (v Variants) Get(name string) (StoredVariant, bool) {
	sv, ok := v[name]
	return sv, ok && sv.Key != ""
}

func (v Variants) Keys() []string {
	keys := make([]string, 0, len(v))
	for _, sv := range v {
		if sv.Key != "" {
			keys = append(keys, sv.Key)
		}
	}
	sort.Strings(keys)
	return keys
}

func (v Variants) encode() []byte {
	if len(v) == 0 {
		return []byte("{}")
	}
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("{}")
	}
	return b
}

func decodeVariants(b []byte) Variants {
	if len(b) == 0 {
		return Variants{}
	}
	var v Variants
	if err := json.Unmarshal(b, &v); err != nil || v == nil {
		return Variants{}
	}
	return v
}
