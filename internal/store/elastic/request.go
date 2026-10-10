package elastic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// ErrNotFound is a 404 from Elasticsearch (an index, alias or document that does not exist).
var ErrNotFound = errors.New("elasticsearch: not found")

// StatusError is an answer that is not 2xx; Type is Elasticsearch's error type when it sent one.
type StatusError struct {
	Status int
	Type   string
	Reason string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("elasticsearch: status %d %s: %s", e.Status, e.Type, e.Reason)
}

// Do sends a JSON request (body nil for none, or a []byte that is sent as it is, as NDJSON for _bulk) and
// decodes a JSON answer into out (nil to discard it). A 404 is ErrNotFound.
func (s *Store) Do(ctx context.Context, method, path string, body, out any) error {
	var r io.Reader = http.NoBody
	contentType := "application/json"
	switch b := body.(type) {
	case nil:
	case []byte:
		r = bytes.NewReader(b)
		contentType = "application/x-ndjson"
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			return fmt.Errorf("elasticsearch: encode request: %w", err)
		}
		r = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.baseURL+path, r)
	if err != nil {
		return fmt.Errorf("elasticsearch: build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := s.api.Do(req)
	if err != nil {
		return fmt.Errorf("elasticsearch unreachable: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		_ = resp.Body.Close()
	}()
	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error struct {
				Type   string `json:"type"`
				Reason string `json:"reason"`
			} `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&e)
		return &StatusError{Status: resp.StatusCode, Type: e.Error.Type, Reason: e.Error.Reason}
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("elasticsearch: decode answer: %w", err)
	}
	return nil
}
