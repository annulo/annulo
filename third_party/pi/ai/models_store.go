package ai

import (
	"context"
	"sync"
)

// Model-catalog persistence ported from pi packages/ai/src/models-store.ts
// (ff28097a; entry shape bd9e09db): dynamic providers restore their last-known
// catalog from a ModelsStore and persist refreshed lists back to it.
//
// Cancellation is caller-owned (upstream fed6009c added
// ModelsStoreOperationOptions.signal): every operation takes a context.Context,
// the Go idiom for pi's optional AbortSignal.

// ModelsStoreEntry is one provider's stored catalog (pi ModelsStoreEntry).
type ModelsStoreEntry struct {
	// Models holds persisted models of every type (pi AnyModel[], upstream
	// a328aa89a). The Models runtime hands a provider its stored entry with the
	// models of types this version does not know already dropped.
	Models []*Model `json:"models"`
	// LastModified is the Unix-millisecond timestamp from the remote catalog's
	// Last-Modified header; 0 when unknown (pi lastModified?, upstream 54fad505).
	// Latent in the SDK: consumed by hosts that compare a stored catalog's mtime
	// against remote/built-in catalogs.
	LastModified int64 `json:"lastModified,omitempty"`
	// CheckedAt is the Unix-millisecond timestamp of the last completed remote
	// check; 0 when never checked (pi checkedAt?).
	CheckedAt int64 `json:"checkedAt,omitempty"`
	// Etag is the opaque validator from the remote catalog's ETag header, stored
	// verbatim (quotes included) and echoed back as If-None-Match; "" when unknown
	// (pi etag?, upstream b1c444d9). Latent in the SDK: consumed by hosts that
	// revalidate remote catalogs against their last-seen ETag.
	Etag string `json:"etag,omitempty"`
}

// ModelsStore is persistent model-catalog storage keyed by provider id. Apps
// inject persistent stores; the default is in-memory. Read returns (nil, nil)
// when nothing is stored.
type ModelsStore interface {
	Read(ctx context.Context, providerID string) (*ModelsStoreEntry, error)
	Write(ctx context.Context, providerID string, entry ModelsStoreEntry) error
	Delete(ctx context.Context, providerID string) error
}

// InMemoryModelsStore is the default in-memory ModelsStore.
//
// pi structuredClones entries on read/write so callers cannot mutate stored
// state. The Go SDK treats *Model as immutable shared catalog pointers
// (Provider.GetModels returns its backing slice by reference), so the store
// copies the entry and its slice and shares the Model pointers.
type InMemoryModelsStore struct {
	mu      sync.Mutex
	entries map[string]ModelsStoreEntry
}

// NewInMemoryModelsStore returns an empty in-memory store.
func NewInMemoryModelsStore() *InMemoryModelsStore {
	return &InMemoryModelsStore{entries: map[string]ModelsStoreEntry{}}
}

// Read returns the stored entry for a provider, or (nil, nil) when none.
func (s *InMemoryModelsStore) Read(ctx context.Context, providerID string) (*ModelsStoreEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, ok := s.entries[providerID]
	if !ok {
		return nil, nil
	}
	out := ModelsStoreEntry{Models: make([]*Model, len(stored.Models)), LastModified: stored.LastModified, CheckedAt: stored.CheckedAt, Etag: stored.Etag}
	copy(out.Models, stored.Models)
	return &out, nil
}

// Write stores the entry for a provider, replacing any previous one.
func (s *InMemoryModelsStore) Write(ctx context.Context, providerID string, entry ModelsStoreEntry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	stored := ModelsStoreEntry{Models: make([]*Model, len(entry.Models)), LastModified: entry.LastModified, CheckedAt: entry.CheckedAt, Etag: entry.Etag}
	copy(stored.Models, entry.Models)
	s.entries[providerID] = stored
	return nil
}

// Delete removes a provider's stored entry.
func (s *InMemoryModelsStore) Delete(ctx context.Context, providerID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, providerID)
	return nil
}

// clone returns a defensive copy of an entry (pi's structuredClone on the
// snapshot handed to providers and on the entry a provider asks to persist).
// The Go SDK treats *Model as immutable shared catalog pointers, so the copy
// covers the entry and its slice and shares the Model pointers.
func (e *ModelsStoreEntry) clone() *ModelsStoreEntry {
	if e == nil {
		return nil
	}
	out := *e
	out.Models = make([]*Model, len(e.Models))
	copy(out.Models, e.Models)
	return &out
}
