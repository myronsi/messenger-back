// Package elastic is the Elasticsearch store: the message search index.
package elastic

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Store talks to Elasticsearch over HTTP.
type Store struct {
	baseURL string
	client  *http.Client // health probes: short timeout
	api     *http.Client // everything else: the caller's context decides
}

// New validates the base URL (credentials may be given as user:password@host) without connecting.
func New(baseURL string) (*Store, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("parse elasticsearch url: want http(s)://host[:port][/path] without query or fragment")
	}
	return &Store{
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  &http.Client{Timeout: 5 * time.Second},
		api:     &http.Client{},
	}, nil
}

// Ping checks that the cluster answers its health endpoint.
func (s *Store) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL+"/_cluster/health?local=true", http.NoBody)
	if err != nil {
		return fmt.Errorf("build elasticsearch request: invalid url")
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("elasticsearch unreachable: %w", err)
	}
	defer func() {
		// Draining lets the transport reuse the connection for the next probe.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		_ = resp.Body.Close()
	}()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("elasticsearch health returned status %d", resp.StatusCode)
	}
	return nil
}

// Close releases idle connections.
func (s *Store) Close() {
	s.client.CloseIdleConnections()
	s.api.CloseIdleConnections()
}
