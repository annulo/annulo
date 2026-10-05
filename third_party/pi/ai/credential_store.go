package ai

import (
	"context"
	"sort"
	"sync"
)

// InMemoryCredentialStore is the default credential store (pi
// packages/ai/src/auth/credential-store.ts). Keyed by provider id, one
// credential per provider; writes are serialized per provider id. Apps inject
// persistent stores.
//
// pi serializes via a per-id promise chain; Go uses a keyedLock, the idiomatic
// equivalent. Upstream fed6009c made the queue cancellable without releasing
// the chain early: a caller whose context is done while it is still queued
// gives up without ever running its task, and a task that has started runs to
// completion. The Go lock has both properties natively — lock() selects on
// ctx.Done() while queued, and the holder is never interrupted.
type InMemoryCredentialStore struct {
	mu    sync.Mutex
	creds map[string]*Credential
	queue *keyedLock
}

// NewInMemoryCredentialStore returns an empty in-memory store.
func NewInMemoryCredentialStore() *InMemoryCredentialStore {
	return &InMemoryCredentialStore{creds: map[string]*Credential{}, queue: newKeyedLock()}
}

// Read returns a copy of the stored credential, or (nil, nil) when none.
func (s *InMemoryCredentialStore) Read(ctx context.Context, providerID string) (*Credential, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.creds[providerID]
	if c == nil {
		return nil, nil
	}
	clone := *c
	return &clone, nil
}

// List returns stored credential metadata without exposing secrets (pi
// InMemoryCredentialStore.list). pi enumerates in Map insertion order; Go maps
// are unordered, so entries are sorted by provider id for determinism.
func (s *InMemoryCredentialStore) List(ctx context.Context) ([]CredentialInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]CredentialInfo, 0, len(s.creds))
	for providerID, credential := range s.creds {
		out = append(out, CredentialInfo{ProviderID: providerID, Type: credential.Type})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ProviderID < out[j].ProviderID })
	return out, nil
}

// Modify is the only write path: a serialized read-modify-write per provider
// id. fn sees a copy of the current credential (nil when none) and returns the
// new credential, or nil to leave the entry unchanged. Returns the post-write
// credential. An error from fn propagates and leaves the entry unchanged.
//
// Cancellation is checked before fn runs and again before the write commits, so
// a mutation cancelled while its callback was in flight leaves the stored
// credential exactly as it was (pi's throwIfAborted around the task body).
func (s *InMemoryCredentialStore) Modify(
	ctx context.Context,
	providerID string,
	fn func(current *Credential) (*Credential, error),
) (*Credential, error) {
	if err := s.queue.lock(ctx, providerID); err != nil {
		return nil, err
	}
	defer s.queue.unlock(providerID)
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.Lock()
	current := s.creds[providerID]
	var currentCopy *Credential
	if current != nil {
		c := *current
		currentCopy = &c
	}
	s.mu.Unlock()

	next, err := fn(currentCopy)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if next != nil {
		stored := *next
		s.creds[providerID] = &stored
		ret := stored
		return &ret, nil
	}
	// Unchanged: return the still-current credential (pi's `next ?? current`).
	if cur := s.creds[providerID]; cur != nil {
		ret := *cur
		return &ret, nil
	}
	return nil, nil
}

// Delete removes a credential (logout), serialized against Modify.
func (s *InMemoryCredentialStore) Delete(ctx context.Context, providerID string) error {
	if err := s.queue.lock(ctx, providerID); err != nil {
		return err
	}
	defer s.queue.unlock(providerID)
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.creds, providerID)
	return nil
}
