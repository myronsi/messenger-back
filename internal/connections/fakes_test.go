package connections

import (
	"context"
	"sync"
	"time"

	"github.com/myronsi/messenger-back/internal/store/postgres"
)

type memState struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newMemState() *memState { return &memState{data: map[string][]byte{}} }

func (m *memState) Put(_ context.Context, key string, value []byte, _ time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = value
	return nil
}

func (m *memState) Get(_ context.Context, key string) ([]byte, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.data[key]
	return v, ok, nil
}

func (m *memState) Take(_ context.Context, key string) ([]byte, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.data[key]
	delete(m.data, key)
	return v, ok, nil
}

func (m *memState) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	return nil
}

type memStore struct {
	mu   sync.Mutex
	next int64
	rows []postgres.Connection
}

func (m *memStore) List(_ context.Context, userID int64) ([]postgres.Connection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []postgres.Connection
	for _, r := range m.rows {
		if r.UserID == userID {
			out = append(out, r)
		}
	}
	return out, nil
}

func (m *memStore) Connect(_ context.Context, userID int64, a postgres.ConnectedAccount) (postgres.Connection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for i, r := range m.rows {
		if r.UserID != userID {
			continue
		}
		n++
		if r.Provider == a.Provider && r.ExternalID == a.ExternalID {
			m.rows[i].DisplayName = a.DisplayName
			return m.rows[i], nil
		}
	}
	if n >= postgres.MaxConnections {
		return postgres.Connection{}, postgres.ErrConnectionLimit
	}
	m.next++
	c := postgres.Connection{ID: m.next, UserID: userID, Provider: a.Provider, ExternalID: a.ExternalID, DisplayName: a.DisplayName, ProfileURL: a.ProfileURL}
	m.rows = append(m.rows, c)
	return c, nil
}

func (m *memStore) Delete(context.Context, int64, int64) error { return nil }

func (m *memStore) Reorder(context.Context, int64, []int64) ([]postgres.Connection, error) {
	return nil, nil
}

type fakeVerifier struct {
	name    string
	checked []string
	account Account
	err     error
}

func (f *fakeVerifier) Name() string { return f.name }

func (f *fakeVerifier) Normalize(input string) (string, error) {
	if input == "" {
		return "", ErrInvalidHandle
	}
	return input, nil
}

func (f *fakeVerifier) Check(_ context.Context, identifier, code string) (Account, error) {
	f.checked = append(f.checked, identifier+" "+code)
	return f.account, f.err
}
