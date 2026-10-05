package client

import (
	"fmt"
	"sync"

	"github.com/sky-valley/pi/protocol"
)

// State is the client's view of the server: the latest server snapshot, the
// per-session snapshots, which sessions this connection has attached, and the
// subscribers watching all of it.
//
// pi's ClientState is single-threaded. Here mutations arrive from the
// connection's reader goroutine while reads come from caller goroutines, so
// every field is guarded. Listeners are invoked after the lock is released, so
// a subscriber may call back into State without deadlocking; ordering still
// holds because all mutation comes from the one reader goroutine.
type State struct {
	mu sync.Mutex
	// disposed latches: nothing is cached again once it is set. Dispose cannot
	// be ordered against the connection feeding this State — the handshake
	// callback runs with no lock held — so "stop caching" has to be a property
	// of the State rather than of when Dispose was called.
	disposed         bool
	snapshot         *protocol.ServerSnapshot
	sessionSnapshots map[string]*protocol.SessionSnapshot
	attached         map[string]bool

	snapshotListeners        *listenerSet[*protocol.ServerSnapshot]
	eventListeners           *listenerSet[protocol.ServerEvent]
	sessionSnapshotListeners map[string]*listenerSet[*protocol.SessionSnapshot]
	sessionEventListeners    map[string]*listenerSet[protocol.ServerEvent]

	onListenerError func(error)
}

// NewState returns an empty State. onListenerError, if set, receives panics
// raised by subscribers; it may be nil.
func NewState(onListenerError func(error)) *State {
	return &State{
		sessionSnapshots:         map[string]*protocol.SessionSnapshot{},
		attached:                 map[string]bool{},
		snapshotListeners:        newListenerSet[*protocol.ServerSnapshot](),
		eventListeners:           newListenerSet[protocol.ServerEvent](),
		sessionSnapshotListeners: map[string]*listenerSet[*protocol.SessionSnapshot]{},
		sessionEventListeners:    map[string]*listenerSet[protocol.ServerEvent]{},
		onListenerError:          onListenerError,
	}
}

// listenerSet holds callbacks in registration order with stable identity.
//
// It is a slice rather than a map for two reasons: Go funcs are not
// comparable, so removal needs an explicit id, and pi stores listeners in a Set
// — which iterates in insertion order. Notifying in map order instead would
// make delivery order vary run to run, which is both a divergence and a source
// of tests that pass by luck. Subscriber counts are small, so the linear
// removal scan is cheaper than keeping a parallel index.
type listenerSet[T any] struct {
	sequence uint64
	entries  []listenerEntry[T]
}

type listenerEntry[T any] struct {
	id uint64
	fn func(T)
}

func newListenerSet[T any]() *listenerSet[T] {
	return &listenerSet[T]{}
}

func (s *listenerSet[T]) add(fn func(T)) uint64 {
	s.sequence++
	s.entries = append(s.entries, listenerEntry[T]{id: s.sequence, fn: fn})
	return s.sequence
}

func (s *listenerSet[T]) remove(id uint64) {
	for i, entry := range s.entries {
		if entry.id == id {
			s.entries = append(s.entries[:i], s.entries[i+1:]...)
			return
		}
	}
}

func (s *listenerSet[T]) len() int { return len(s.entries) }

// clear drops every listener but keeps the id counter, so an unsubscribe still
// held from before the clear cannot name a listener registered after it.
func (s *listenerSet[T]) clear() { s.entries = nil }

// next is the first listener registered after id. Ids only increase, so walking
// them visits every subscriber once, in registration order, without depending on
// positions a concurrent add or remove would shift.
func (s *listenerSet[T]) next(after uint64) (listenerEntry[T], bool) {
	if s == nil {
		return listenerEntry[T]{}, false
	}
	for _, entry := range s.entries {
		if entry.id > after {
			return entry, true
		}
	}
	return listenerEntry[T]{}, false
}

// Snapshot returns the latest server snapshot, or nil before the handshake.
//
// The cached snapshot is returned as-is, not copied, and the same pointer goes
// to every caller and every subscriber. It must be treated as read-only:
// nothing in this package ever mutates a cached snapshot in place — new state
// arrives as a fresh snapshot that replaces the old one — so a caller that
// holds on to the pointer holds a stable view of one moment.
func (s *State) Snapshot() *protocol.ServerSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshot
}

// Reset drops all server-derived state, keeping subscribers.
func (s *State) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resetLocked()
}

func (s *State) resetLocked() {
	s.snapshot = nil
	clear(s.sessionSnapshots)
	clear(s.attached)
}

// ClearAttachments forgets which sessions were attached, keeping snapshots. A
// reconnect loses the attachments but the cached transcripts are still worth
// showing.
func (s *State) ClearAttachments() {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.attached)
}

// Dispose drops all state and all subscribers, permanently: a State that has
// been disposed of ignores everything applied to it afterwards.
//
// DIVERGENCE (deliberate): pi's dispose() only clears, because a single thread
// cannot have a snapshot in flight while it runs. Here the reader goroutine can
// be midway through delivering one, so clearing alone would leave a disposed
// client describing a server it has already let go of.
func (s *State) Dispose() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.disposed = true
	s.resetLocked()
	// Cleared in place, not replaced: a listener id must never be handed out
	// twice, or an unsubscribe held from before disposal cancels a stranger.
	s.snapshotListeners.clear()
	s.eventListeners.clear()
	clear(s.sessionSnapshotListeners)
	clear(s.sessionEventListeners)
}

// SessionSnapshot returns the cached snapshot for a session, if any. Like
// Snapshot it hands back the cached pointer rather than a copy, and it must be
// treated as read-only for the same reason.
func (s *State) SessionSnapshot(sessionID string) *protocol.SessionSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessionSnapshots[sessionID]
}

// IsSessionAttached reports whether this connection has the session attached.
func (s *State) IsSessionAttached(sessionID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attached[sessionID]
}

// ForgetSessionSnapshot removes and returns a cached session snapshot.
func (s *State) ForgetSessionSnapshot(sessionID string) *protocol.SessionSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := s.sessionSnapshots[sessionID]
	delete(s.sessionSnapshots, sessionID)
	return previous
}

// RestoreSessionSnapshot puts a snapshot back only if none is cached. Restore
// exists to undo a failed detach, and a fresher snapshot may have arrived in
// the meantime — that one wins.
func (s *State) RestoreSessionSnapshot(snapshot *protocol.SessionSnapshot) {
	if snapshot == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.sessionSnapshots[snapshot.ID]; !exists {
		s.sessionSnapshots[snapshot.ID] = snapshot
	}
}

// Subscribe registers a server-snapshot listener.
func (s *State) Subscribe(listener func(*protocol.ServerSnapshot)) Unsubscribe {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.snapshotListeners.add(listener)
	return s.once(func() { s.snapshotListeners.remove(id) })
}

// OnEvent registers a listener for every server event.
func (s *State) OnEvent(listener func(protocol.ServerEvent)) Unsubscribe {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.eventListeners.add(listener)
	return s.once(func() { s.eventListeners.remove(id) })
}

// SubscribeSession registers a snapshot listener scoped to one session.
func (s *State) SubscribeSession(sessionID string, listener func(*protocol.SessionSnapshot)) Unsubscribe {
	s.mu.Lock()
	defer s.mu.Unlock()
	return addScoped(s, s.sessionSnapshotListeners, sessionID, listener)
}

// OnSessionEvent registers an event listener scoped to one session.
func (s *State) OnSessionEvent(sessionID string, listener func(protocol.ServerEvent)) Unsubscribe {
	s.mu.Lock()
	defer s.mu.Unlock()
	return addScoped(s, s.sessionEventListeners, sessionID, listener)
}

// addScoped registers a per-session listener, creating the bucket on demand and
// dropping it when its last listener leaves — otherwise a long-lived client
// accumulates one map entry per session it ever watched.
func addScoped[T any](s *State, buckets map[string]*listenerSet[T], sessionID string, listener func(T)) Unsubscribe {
	set, exists := buckets[sessionID]
	if !exists {
		set = newListenerSet[T]()
		buckets[sessionID] = set
	}
	id := set.add(listener)
	return s.once(func() {
		set.remove(id)
		// Deleted by set identity, not by session id: Dispose drops the buckets
		// wholesale, so an unsubscribe held from before it names a set the map
		// no longer has — and must not take its replacement with it.
		if set.len() == 0 && buckets[sessionID] == set {
			delete(buckets, sessionID)
		}
	})
}

// once wraps an unsubscribe so repeated calls are harmless — callers hold these
// and may run them from a defer as well as explicitly.
func (s *State) once(remove func()) Unsubscribe {
	var done sync.Once
	return func() {
		done.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			remove()
		})
	}
}

// ApplyResult folds a command result into the cached state.
func (s *State) ApplyResult(result protocol.CommandResult) {
	s.mu.Lock()
	var pending notification
	switch typed := result.(type) {
	case *protocol.ListResult:
		// A list result carries summaries, not snapshots; pi leaves cached
		// state untouched.
	case *protocol.DetachResult:
		delete(s.attached, typed.SessionID)
		if current := s.sessionSnapshots[typed.SessionID]; current != nil {
			// A detach result carries no new snapshot, so the cached one is
			// force-updated: its revision has not moved and the normal guard
			// would discard the change.
			detached := *current
			detached.Attached = false
			pending = s.applySessionSnapshotLocked(&detached, true)
		}
	case *protocol.SessionResult:
		session := typed.Session
		pending = s.applySessionSnapshotLocked(&session, false)
	}
	s.mu.Unlock()
	if pending != nil {
		pending()
	}
}

// ApplyEvent folds a server event into the cached state and notifies listeners.
func (s *State) ApplyEvent(event protocol.ServerEvent) {
	s.mu.Lock()
	if s.disposed {
		s.mu.Unlock()
		return
	}
	var pending []notification
	switch typed := event.(type) {
	case *protocol.ServerSnapshotEvent:
		snapshot := typed.Snapshot
		if applied := s.applyServerSnapshotLocked(&snapshot); applied != nil {
			pending = append(pending, applied)
		}
	case *protocol.SessionSnapshotEvent:
		snapshot := typed.Snapshot
		if applied := s.applySessionSnapshotLocked(&snapshot, false); applied != nil {
			pending = append(pending, applied)
		}
	case *protocol.SessionRemovedEvent:
		delete(s.sessionSnapshots, typed.SessionID)
		delete(s.attached, typed.SessionID)
	}
	pending = append(pending, queue(s, func() *listenerSet[protocol.ServerEvent] {
		return s.eventListeners
	}, event))
	if sessionID := eventSessionID(event); sessionID != "" {
		pending = append(pending, queue(s, func() *listenerSet[protocol.ServerEvent] {
			return s.sessionEventListeners[sessionID]
		}, event))
	}
	s.mu.Unlock()
	s.deliver(pending)
}

// ApplyServerSnapshot replaces the server snapshot unless it is stale.
func (s *State) ApplyServerSnapshot(snapshot *protocol.ServerSnapshot) {
	s.mu.Lock()
	pending := s.applyServerSnapshotLocked(snapshot)
	s.mu.Unlock()
	if pending != nil {
		pending()
	}
}

func (s *State) applyServerSnapshotLocked(snapshot *protocol.ServerSnapshot) notification {
	if snapshot == nil || s.disposed {
		return nil
	}
	// Snapshots can arrive out of order behind a reconnect; an older one must
	// not clobber a newer view.
	if s.snapshot != nil && snapshot.Revision < s.snapshot.Revision {
		return nil
	}
	s.snapshot = snapshot
	// 6189e53b3 dropped `attached` from the listed session shape, so a server
	// snapshot no longer says anything about this connection's attachments and
	// cannot rebuild them. They are now maintained solely by the attach and
	// detach paths, exactly as pi's ClientState does after the same change.
	return queue(s, func() *listenerSet[*protocol.ServerSnapshot] { return s.snapshotListeners }, snapshot)
}

func (s *State) applySessionSnapshotLocked(snapshot *protocol.SessionSnapshot, force bool) notification {
	if snapshot == nil || s.disposed {
		return nil
	}
	current := s.sessionSnapshots[snapshot.ID]
	if !force && current != nil && snapshot.Revision < current.Revision {
		return nil
	}
	s.sessionSnapshots[snapshot.ID] = snapshot
	if snapshot.Attached {
		s.attached[snapshot.ID] = true
	} else {
		delete(s.attached, snapshot.ID)
	}
	sessionID := snapshot.ID
	return queue(s, func() *listenerSet[*protocol.SessionSnapshot] {
		return s.sessionSnapshotListeners[sessionID]
	}, snapshot)
}

// notification is one listener set's delivery, deferred until the state lock is
// released.
type notification func()

// queue defers a value's delivery to one listener set. resolve is re-run under
// the lock at every step rather than captured once, because Dispose replaces
// what the per-session maps hold.
func queue[T any](s *State, resolve func() *listenerSet[T], value T) notification {
	return func() {
		// Walk the live set: a listener registered during this notification is
		// reached by it, and one unsubscribed during it is not. pi iterates a
		// Set directly and a JS Set iterator sees both edits, so snapshotting
		// the list up front would diverge in two directions at once — and the
		// second direction delivers to a subscriber that has already said stop.
		//
		// The walk is by id, not by position, so an unsubscribe that shifts the
		// slice cannot make the walk skip or repeat a listener. Nothing is held
		// while a listener runs: taking a delivery lock under s.mu would
		// deadlock the moment a listener read the State.
		for last := uint64(0); ; {
			s.mu.Lock()
			entry, ok := resolve().next(last)
			s.mu.Unlock()
			if !ok {
				return
			}
			last = entry.id
			s.call(func() { entry.fn(value) })
		}
	}
}

// deliver runs queued notifications outside the lock.
func (s *State) deliver(pending []notification) {
	for _, notify := range pending {
		notify()
	}
}

// call contains one listener's failure. pi wraps every callback in try/catch so
// a subscriber cannot corrupt client state or stop the subscribers after it;
// recover is the Go equivalent and is justified for exactly that reason.
func (s *State) call(notify func()) {
	defer func() {
		if recovered := recover(); recovered != nil {
			s.reportListenerError(recovered)
		}
	}()
	notify()
}

func (s *State) reportListenerError(recovered any) {
	if s.onListenerError == nil {
		return
	}
	// Diagnostics cannot affect client state, so a broken error handler must
	// not escape either.
	defer func() { _ = recover() }()
	err, ok := recovered.(error)
	if !ok {
		err = fmt.Errorf("%v", recovered)
	}
	s.onListenerError(err)
}

// eventSessionID reports which session an event belongs to, if any.
func eventSessionID(event protocol.ServerEvent) string {
	switch typed := event.(type) {
	case *protocol.SessionSnapshotEvent:
		return typed.Snapshot.ID
	case *protocol.SessionProgressEvent:
		return typed.SessionID
	case *protocol.SessionRemovedEvent:
		return typed.SessionID
	default:
		return ""
	}
}
