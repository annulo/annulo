package ai

import (
	"context"
	"sync"
)

// keyedLock serializes work per string key while honoring cancellation of a
// waiting caller. It is the Go expression of pi's per-key promise chains
// (InMemoryCredentialStore.enqueue, ModelsImpl.publicationChains, upstream
// fed6009c): a queued caller stops waiting as soon as its context is done,
// while the holder runs to completion — the chain is never released before the
// active work settles. Entries are reference-counted so the map does not grow
// with every key ever locked (pi's chain cleanup).
type keyedLock struct {
	mu      sync.Mutex
	entries map[string]*keyedLockEntry
}

type keyedLockEntry struct {
	// slot is a one-token semaphore: holding the token is holding the lock.
	slot chan struct{}
	// refs counts callers between acquireEntry and releaseEntry.
	refs int
}

func newKeyedLock() *keyedLock {
	return &keyedLock{entries: map[string]*keyedLockEntry{}}
}

// lock acquires key's lock, returning ctx.Err() if ctx is done first. The lock
// is not held when a non-nil error is returned.
func (k *keyedLock) lock(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	entry := k.acquireEntry(key)
	select {
	case entry.slot <- struct{}{}:
		return nil
	case <-ctx.Done():
		k.releaseEntry(key)
		return ctx.Err()
	}
}

// unlock releases key's lock. It must be called exactly once per successful
// lock.
func (k *keyedLock) unlock(key string) {
	k.mu.Lock()
	entry := k.entries[key]
	k.mu.Unlock()
	if entry == nil {
		return
	}
	<-entry.slot
	k.releaseEntry(key)
}

func (k *keyedLock) acquireEntry(key string) *keyedLockEntry {
	k.mu.Lock()
	defer k.mu.Unlock()
	entry := k.entries[key]
	if entry == nil {
		entry = &keyedLockEntry{slot: make(chan struct{}, 1)}
		k.entries[key] = entry
	}
	entry.refs++
	return entry
}

func (k *keyedLock) releaseEntry(key string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	entry := k.entries[key]
	if entry == nil {
		return
	}
	entry.refs--
	if entry.refs <= 0 {
		delete(k.entries, key)
	}
}
