// Package storagetest provides an in-memory object store for tests, so upload and
// catalog code can be exercised without MinIO.
package storagetest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sort"
	"sync"

	"littleworld/internal/storage"
)

type Mem struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func New() *Mem {
	return &Mem{objects: map[string][]byte{}}
}

type readAtCloser struct{ *bytes.Reader }

func (readAtCloser) Close() error { return nil }

func (m *Mem) Put(_ context.Context, key string, r io.Reader, _ int64, _ string) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[key] = data
	return nil
}

func (m *Mem) Open(_ context.Context, key string) (storage.Object, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.objects[key]
	if !ok {
		return nil, errors.New("object not found: " + key)
	}
	return readAtCloser{bytes.NewReader(data)}, nil
}

func (m *Mem) Promote(_ context.Context, fromKey, toKey string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.objects[fromKey]
	if !ok {
		return errors.New("staging object not found: " + fromKey)
	}
	m.objects[toKey] = data
	delete(m.objects, fromKey)
	return nil
}

func (m *Mem) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects, key)
	return nil
}

func (m *Mem) PresignGet(_ context.Context, key string) (string, error) {
	return "signed:" + key, nil
}

func (m *Mem) Keys() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := make([]string, 0, len(m.objects))
	for key := range m.objects {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (m *Mem) Get(key string) ([]byte, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.objects[key]
	return data, ok
}

// Stat implements the storage size lookup used by the EPUB inspector.
func (m *Mem) Stat(_ context.Context, key string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.objects[key]
	if !ok {
		return 0, errors.New("object not found: " + key)
	}
	return int64(len(data)), nil
}
