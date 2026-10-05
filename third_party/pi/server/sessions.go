package server

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/sky-valley/pi/protocol"
)

// liveSession is one acquired runtime plus the connections attached to it.
//
// Every field is guarded by sessionManager.mu except queue, which is
// self-synchronising.
type liveSession struct {
	id          string
	runtime     SessionRuntime
	connections map[*connState]struct{}
	unsubscribe Unsubscribe

	operationCount int
	ready          bool
	terminal       bool

	// disposing is non-nil once disposal has begun and is closed when it has
	// finished; disposeErr then holds its result. Together they are the
	// disposing promise pi keeps on the session.
	disposing  chan struct{}
	disposeErr error

	// queue serialises the work a runtime event schedules, off the runtime's
	// own goroutine. Reading a snapshot or disposing from inside a subscriber
	// callback would re-enter a runtime that is mid-emit.
	queue serialQueue
}

// openingSession is an in-flight acquisition other callers can join.
type openingSession struct {
	done chan struct{}
	live *liveSession
	err  error
}

// sessionManager owns every live session: which runtimes are acquired, which
// connections hold them, and when a runtime is released.
//
// A session is a singleton per ID. Two connections that attach to the same
// session share one runtime and both control it; the runtime is disposed once
// the last connection detaches and the session goes idle.
type sessionManager struct {
	srv     *Server
	service Service

	mu sync.Mutex
	// live and liveOrder are one map: liveOrder keeps insertion order, which
	// pi gets for free from a JS Map and which decides the order unstored live
	// sessions appear in a listing.
	live      map[string]*liveSession
	liveOrder []string
	opening   map[string]*openingSession
}

func newSessionManager(srv *Server, service Service) *sessionManager {
	return &sessionManager{
		srv:     srv,
		service: service,
		live:    map[string]*liveSession{},
		opening: map[string]*openingSession{},
	}
}

// executeCommand runs one client command on behalf of conn.
func (m *sessionManager) executeCommand(
	ctx context.Context,
	conn *connState,
	command protocol.Command,
) (protocol.CommandResult, error) {
	switch cmd := command.(type) {
	case *protocol.ListCommand:
		sessions, err := m.listMetadata(ctx)
		if err != nil {
			return nil, err
		}
		return &protocol.ListResult{Command: "list", Sessions: sessions}, nil

	case *protocol.CreateCommand:
		id := newUUID()
		options := CreateSessionOptions{
			ID:            id,
			Cwd:           cmd.Cwd,
			Name:          cmd.Name,
			Model:         cmd.Model,
			ThinkingLevel: cmd.ThinkingLevel,
		}
		live, err := m.acquire(ctx, id, func(ctx context.Context) (SessionRuntime, error) {
			return m.service.CreateSession(ctx, options)
		})
		if err != nil {
			return nil, err
		}
		session, err := m.attachAndSnapshot(ctx, conn, live)
		if err != nil {
			return nil, err
		}
		m.srv.snapshots.broadcast()
		return &protocol.SessionResult{Command: "create", Session: *session}, nil

	case *protocol.AttachCommand:
		live, err := m.acquire(ctx, cmd.SessionID, func(ctx context.Context) (SessionRuntime, error) {
			return m.service.OpenSession(ctx, cmd.SessionID)
		})
		if err != nil {
			return nil, err
		}
		session, err := m.attachAndSnapshot(ctx, conn, live)
		if err != nil {
			return nil, err
		}
		m.srv.snapshots.broadcast()
		return &protocol.SessionResult{Command: "attach", Session: *session}, nil

	case *protocol.DetachCommand:
		// Detaching is a command like any other: it has to say so when the
		// connection does not hold the session, or when the session is no
		// longer live. Only the disconnect path detaches silently.
		live, err := m.requireAttached(conn, cmd.SessionID)
		if err != nil {
			return nil, err
		}
		if err := m.detach(ctx, conn, live); err != nil {
			return nil, err
		}
		return &protocol.DetachResult{Command: "detach", SessionID: cmd.SessionID}, nil

	case *protocol.PromptCommand:
		return m.sessionOperation(ctx, conn, cmd.SessionID, "prompt", func(r SessionRuntime) error {
			return r.Prompt(ctx, PromptInput{Text: cmd.Text})
		})

	case *protocol.SteerCommand:
		return m.sessionOperation(ctx, conn, cmd.SessionID, "steer", func(r SessionRuntime) error {
			return r.Steer(ctx, SteerInput{Text: cmd.Text})
		})

	case *protocol.AbortCommand:
		return m.sessionOperation(ctx, conn, cmd.SessionID, "abort", func(r SessionRuntime) error {
			return r.Abort(ctx)
		})

	case *protocol.SetModelCommand:
		return m.sessionOperation(ctx, conn, cmd.SessionID, "set_model", func(r SessionRuntime) error {
			return r.SetModel(ctx, cmd.Model)
		})

	case *protocol.SetThinkingCommand:
		return m.sessionOperation(ctx, conn, cmd.SessionID, "set_thinking", func(r SessionRuntime) error {
			return r.SetThinking(ctx, cmd.ThinkingLevel)
		})

	default:
		// Unreachable: the decoder rejects any command this switch does not
		// name, so reaching here means the protocol package grew an arm the
		// server does not implement.
		return nil, NewError(protocol.ErrorInvalidRequest,
			fmt.Sprintf("Unsupported command %s", command.CommandName()), nil)
	}
}

func (m *sessionManager) attachAndSnapshot(
	ctx context.Context,
	conn *connState,
	live *liveSession,
) (*protocol.SessionSnapshot, error) {
	if err := m.attach(ctx, conn, live); err != nil {
		return nil, err
	}
	snapshot, err := m.broadcastSnapshot(ctx, live)
	if err != nil {
		return nil, err
	}
	return forConnection(snapshot, conn), nil
}

func (m *sessionManager) sessionOperation(
	ctx context.Context,
	conn *connState,
	sessionID string,
	name string,
	operation func(SessionRuntime) error,
) (protocol.CommandResult, error) {
	live, err := m.requireAttached(conn, sessionID)
	if err != nil {
		return nil, err
	}
	snapshot, err := m.runOperation(ctx, conn, live, operation)
	if err != nil {
		return nil, err
	}
	return &protocol.SessionResult{Command: name, Session: *snapshot}, nil
}

// detach releases one session from one connection. The caller has already
// established, through requireAttached, that the connection holds it.
func (m *sessionManager) detach(ctx context.Context, conn *connState, live *liveSession) error {
	// Both halves of the attachment go under both locks, for the reason
	// attach documents: a disconnect that sees only one of them undone leaves
	// the other behind forever.
	m.mu.Lock()
	conn.mu.Lock()
	delete(conn.sessionIDs, live.id)
	delete(live.connections, conn)
	// DIVERGENCE (deliberate): pi broadcasts whenever a connection is left.
	// The terminal and disposing terms are Go's alone: a disposal that began
	// concurrently would otherwise have this read a snapshot from a runtime
	// that is already being released. pi is single-threaded and cannot get
	// there.
	broadcastSession := len(live.connections) > 0 && !live.terminal && live.disposing == nil
	conn.mu.Unlock()
	m.mu.Unlock()

	if broadcastSession {
		if _, err := m.broadcastSnapshot(ctx, live); err != nil {
			return err
		}
	}
	if err := m.maybeDispose(ctx, live); err != nil {
		return err
	}
	m.srv.snapshots.broadcast()
	return nil
}

// disconnect releases everything conn held. It is called once per connection,
// from Server.disconnect.
func (m *sessionManager) disconnect(ctx context.Context, conn *connState) {
	conn.mu.Lock()
	ids := make([]string, 0, len(conn.sessionIDs))
	for id := range conn.sessionIDs {
		ids = append(ids, id)
	}
	clear(conn.sessionIDs)
	conn.mu.Unlock()

	m.mu.Lock()
	sessions := make([]*liveSession, 0, len(ids))
	for _, id := range ids {
		if live := m.live[id]; live != nil {
			delete(live.connections, conn)
			sessions = append(sessions, live)
		}
	}
	m.mu.Unlock()

	for _, live := range sessions {
		if err := m.maybeDispose(ctx, live); err != nil {
			m.srv.reportError(err)
		}
	}
}

// listMetadata merges what the service has stored with what is live now. A
// live session always wins: its snapshot is authoritative, and a live session
// the service has not stored yet is appended.
//
// It takes no connection: since 6189e53b3 the listed shape is durable metadata
// with no per-connection `attached` field, which is what lets one snapshot be
// built once and broadcast to every peer.
func (m *sessionManager) listMetadata(ctx context.Context) ([]protocol.SessionMetadata, error) {
	stored, err := m.service.ListSessions(ctx)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	candidates := make([]*liveSession, 0, len(m.liveOrder))
	for _, id := range m.liveOrder {
		if live := m.live[id]; live != nil && live.disposing == nil {
			candidates = append(candidates, live)
		}
	}
	m.mu.Unlock()

	liveIDs := make([]string, 0, len(candidates))
	byID := make(map[string]*protocol.SessionSnapshot, len(candidates))
	for _, live := range candidates {
		snapshot, err := m.normalizedSnapshot(ctx, live)
		if err != nil {
			return nil, err
		}
		liveIDs = append(liveIDs, live.id)
		byID[live.id] = snapshot
	}

	metadata := make([]protocol.SessionMetadata, 0, len(stored)+len(liveIDs))
	for _, item := range stored {
		snapshot, ok := byID[item.ID]
		if !ok {
			metadata = append(metadata, item)
			continue
		}
		delete(byID, item.ID)
		metadata = append(metadata, overlayLiveMetadata(item, snapshot.Metadata()))
	}
	for _, id := range liveIDs {
		snapshot, ok := byID[id]
		if !ok {
			continue
		}
		metadata = append(metadata, snapshot.Metadata())
	}
	return metadata, nil
}

// overlayLiveMetadata lays a live snapshot's projection over the stored record,
// reproducing pi's `{ ...item, ...toMetadata(snapshot) }`. It is not a merge:
// the live side wins outright, including where it has nothing to say.
//
// Every field the projection produces wins, including when it is absent --
// a live session whose snapshot has no name clears a stored sessionName,
// because in pi the spread writes the key with `undefined`. ParentSessionID
// is the one field toMetadata never produces, so it survives from storage.
func overlayLiveMetadata(stored, live protocol.SessionMetadata) protocol.SessionMetadata {
	merged := live
	merged.ParentSessionID = stored.ParentSessionID
	return merged
}

// attachedTo answers the attachment question for a possibly-nil connection; a
// nil connection holds nothing.
func (c *connState) attachedTo(sessionID string) bool {
	if c == nil {
		return false
	}
	return c.attached(sessionID)
}

// close disposes every live session. The Server has already stopped accepting
// and disconnected its connections.
func (m *sessionManager) close(ctx context.Context) error {
	m.mu.Lock()
	opening := make([]*openingSession, 0, len(m.opening))
	for _, op := range m.opening {
		opening = append(opening, op)
	}
	m.mu.Unlock()
	for _, op := range opening {
		// Every wait here is bounded by ctx: an acquisition the service never
		// answers must not hold a bounded shutdown open forever.
		select {
		case <-op.done:
			if op.err != nil {
				m.srv.reportError(op.err)
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	m.mu.Lock()
	sessions := make([]*liveSession, 0, len(m.liveOrder))
	for _, id := range m.liveOrder {
		if live := m.live[id]; live != nil {
			sessions = append(sessions, live)
		}
	}
	m.live = map[string]*liveSession{}
	m.liveOrder = nil
	m.mu.Unlock()

	var firstErr error
	for _, live := range sessions {
		if err := live.queue.WaitContext(ctx); err != nil {
			return err
		}

		m.mu.Lock()
		disposing := live.disposing
		unsubscribe := live.unsubscribe
		m.mu.Unlock()

		if disposing != nil {
			select {
			case <-disposing:
				if live.disposeErr != nil && firstErr == nil {
					firstErr = live.disposeErr
				}
			case <-ctx.Done():
				return ctx.Err()
			}
			continue
		}
		unsubscribe()
		if err := m.disposeSafely(ctx, live.runtime); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// disposeSafely releases a runtime behind a panic barrier. Shutdown is the
// worst place to let a third-party implementation take the process down: every
// runtime after this one would go unreleased, and so would its durable lock.
func (m *sessionManager) disposeSafely(ctx context.Context, runtime SessionRuntime) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = panicError(recovered, "disposing a session runtime")
		}
	}()
	return runtime.Dispose(ctx)
}

// goSafely queues one job on the session's serial queue behind a panic barrier.
// The job calls into a Service or a SessionRuntime from a goroutine their
// implementer never handed us, so a panic there has nowhere to surface but the
// error observer.
func (m *sessionManager) goSafely(live *liveSession, what string, job func() error) {
	live.queue.Go(func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				m.srv.reportError(panicError(recovered, what))
			}
		}()
		if err := job(); err != nil {
			m.srv.reportError(err)
		}
	})
}

func (m *sessionManager) runOperation(
	ctx context.Context,
	conn *connState,
	live *liveSession,
	operation func(SessionRuntime) error,
) (*protocol.SessionSnapshot, error) {
	// DIVERGENCE (deliberate): pi checks liveness in requireAttached and then
	// counts the operation, with nothing able to run in between. Here a
	// disposal can begin in that gap, so the count is taken under the same
	// lock acquisition that re-reads it — otherwise the operation would run
	// against a runtime that is already being released.
	m.mu.Lock()
	if live.terminal || live.disposing != nil {
		m.mu.Unlock()
		return nil, NewError(protocol.ErrorNotFound, "Session is not live: "+live.id, nil)
	}
	live.operationCount++
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		live.operationCount--
		m.mu.Unlock()
		m.scheduleMaybeDispose(live)
	}()

	if err := operation(live.runtime); err != nil {
		return nil, err
	}
	snapshot, err := m.broadcastSnapshot(ctx, live)
	if err != nil {
		return nil, err
	}
	return forConnection(snapshot, conn), nil
}

// acquire returns the live session for id, creating it when no one holds it
// yet and joining an in-flight acquisition when someone does.
func (m *sessionManager) acquire(
	ctx context.Context,
	id string,
	acquireRuntime func(context.Context) (SessionRuntime, error),
) (*liveSession, error) {
	for {
		m.mu.Lock()
		if existing := m.live[id]; existing != nil {
			if existing.terminal {
				m.mu.Unlock()
				return nil, NewError(protocol.ErrorSessionLocked,
					"Session runtime is terminating: "+id, nil)
			}
			if existing.disposing != nil {
				disposing := existing.disposing
				m.mu.Unlock()
				<-disposing
				continue
			}
			m.mu.Unlock()
			return existing, nil
		}
		if op := m.opening[id]; op != nil {
			m.mu.Unlock()
			<-op.done
			return op.live, op.err
		}
		op := &openingSession{done: make(chan struct{})}
		m.opening[id] = op
		m.mu.Unlock()

		op.live, op.err = m.create(ctx, id, acquireRuntime)
		m.mu.Lock()
		if m.opening[id] == op {
			delete(m.opening, id)
		}
		m.mu.Unlock()
		close(op.done)
		return op.live, op.err
	}
}

func (m *sessionManager) create(
	ctx context.Context,
	id string,
	acquireRuntime func(context.Context) (SessionRuntime, error),
) (*liveSession, error) {
	runtime, err := acquireRuntime(ctx)
	if err != nil {
		return nil, err
	}
	if m.srv.isClosing() {
		m.disposeQuietly(ctx, runtime)
		return nil, errors.New("PiServer closed while acquiring a session runtime")
	}

	snapshot, err := runtime.Snapshot(ctx)
	if err != nil {
		m.disposeQuietly(ctx, runtime)
		return nil, err
	}
	if snapshot.ID != id {
		m.disposeQuietly(ctx, runtime)
		return nil, NewError(protocol.ErrorInvalidRequest,
			fmt.Sprintf("Service returned session %s for server-assigned session %s", snapshot.ID, id), nil)
	}

	live := &liveSession{
		id:          id,
		runtime:     runtime,
		connections: map[*connState]struct{}{},
		unsubscribe: func() {},
	}
	// A runtime may report a terminal failure from inside Subscribe, before it
	// has handed back the canceller. That reaches the manager on the session's
	// queue, which takes the lock — so the canceller is recorded under the same
	// lock, as liveSession promises for every field but the queue. The
	// placeholder covers the ordering the lock cannot: a teardown that got here
	// first cancelled nothing, and this caller cancels for it.
	unsubscribe := runtime.Subscribe(func(event RuntimeEvent) {
		m.handleRuntimeEvent(live, event)
	})

	m.mu.Lock()
	live.unsubscribe = unsubscribe
	m.live[id] = live
	m.liveOrder = append(m.liveOrder, id)
	live.ready = true
	terminated := live.terminal
	m.mu.Unlock()
	if terminated {
		unsubscribe()
	}
	return live, nil
}

func (m *sessionManager) disposeQuietly(ctx context.Context, runtime SessionRuntime) {
	if err := runtime.Dispose(ctx); err != nil {
		m.srv.reportError(err)
	}
}

// handleRuntimeEvent is the subscriber the manager registers on every runtime.
//
// The arms are split the way pi's are, and the split is observable. pi sends
// progress synchronously from inside the emit, while its snapshot arm suspends
// on `await this.normalizedSnapshot(live)` before it sends anything — so a
// runtime that emits a snapshot and then progress in one call stack puts the
// progress on the wire first. Running both through the queue instead reverses
// that pair, and a client that applies deltas onto the last snapshot it saw
// then applies the same delta twice: once from the delta, once from the
// snapshot that already contained it.
//
// DIVERGENCE (deliberate): pi gets the snapshot and error arms off the emitting
// call stack by dropping a promise on the floor. The session's serial queue is
// the same guarantee stated directly — a burst still reaches the peer in
// emission order, and the work that calls back into the runtime (reading a
// snapshot, disposing) never re-enters a runtime that is mid-emit.
func (m *sessionManager) handleRuntimeEvent(live *liveSession, event RuntimeEvent) {
	ctx := m.srv.opContext()
	switch event.Type {
	case RuntimeErrorEvent:
		m.goSafely(live, "terminating a session after a runtime failure", func() error {
			return m.terminate(ctx, live, event.Err)
		})
	case RuntimeProgressEvent:
		envelope := &protocol.EventEnvelope{
			Type: "event",
			Event: &protocol.SessionProgressEvent{
				Type:      "session_progress",
				SessionID: live.id,
				Progress:  event.Progress,
			},
		}
		// Sent on the emitting goroutine, as pi does, which also settles who
		// receives it: the connections attached at the moment of the emit, not
		// whichever set exists by the time a queued job gets to run.
		for _, conn := range m.sessionConnections(live) {
			m.srv.sendMessage(conn, envelope)
		}
		m.scheduleMaybeDispose(live)
	default:
		m.goSafely(live, "rebroadcasting a session snapshot", func() error {
			_, err := m.broadcastSnapshot(ctx, live)
			return err
		})
		m.scheduleMaybeDispose(live)
	}
}

// terminate tears a session down after its runtime reported a terminal error:
// every attached connection is closed, then the runtime is released.
func (m *sessionManager) terminate(ctx context.Context, live *liveSession, cause *Error) error {
	m.mu.Lock()
	if live.terminal {
		m.mu.Unlock()
		return nil
	}
	live.terminal = true
	unsubscribe := live.unsubscribe
	connections := make([]*connState, 0, len(live.connections))
	for conn := range live.connections {
		connections = append(connections, conn)
	}
	m.mu.Unlock()

	m.srv.reportError(cause)
	unsubscribe()
	for _, conn := range connections {
		m.srv.closeConnection(conn.conn, nil)
	}
	for _, conn := range connections {
		m.srv.disconnect(ctx, conn)
	}
	return m.maybeDispose(ctx, live)
}

// normalizedSnapshot re-reads the runtime and overrides the three fields the
// server owns rather than the service: the live phase, whether anyone is
// attached, and that the session is locked because the server holds it.
func (m *sessionManager) normalizedSnapshot(ctx context.Context, live *liveSession) (*protocol.SessionSnapshot, error) {
	snapshot, err := live.runtime.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	if snapshot.ID != live.id {
		return nil, NewError(protocol.ErrorInvalidRequest,
			fmt.Sprintf("Runtime session ID changed from %s to %s", live.id, snapshot.ID), nil)
	}
	normalized := *snapshot
	normalized.Phase = live.runtime.Phase()
	m.mu.Lock()
	normalized.Attached = len(live.connections) > 0
	m.mu.Unlock()
	normalized.Locked = true
	return &normalized, nil
}

// forConnection re-answers "attached" from one connection's point of view: a
// session is attached for the connection that holds it, whatever other
// connections are doing.
func forConnection(snapshot *protocol.SessionSnapshot, conn *connState) *protocol.SessionSnapshot {
	result := *snapshot
	result.Attached = conn.attachedTo(snapshot.ID)
	return &result
}

func (m *sessionManager) sessionConnections(live *liveSession) []*connState {
	m.mu.Lock()
	defer m.mu.Unlock()
	connections := make([]*connState, 0, len(live.connections))
	for conn := range live.connections {
		connections = append(connections, conn)
	}
	return connections
}

func (m *sessionManager) broadcastSnapshot(ctx context.Context, live *liveSession) (*protocol.SessionSnapshot, error) {
	snapshot, err := m.normalizedSnapshot(ctx, live)
	if err != nil {
		return nil, err
	}
	envelope := &protocol.EventEnvelope{
		Type:  "event",
		Event: &protocol.SessionSnapshotEvent{Type: "session_snapshot", Snapshot: *snapshot},
	}
	for _, conn := range m.sessionConnections(live) {
		m.srv.sendMessage(conn, envelope)
	}
	return snapshot, nil
}

// attach records one connection as a holder of one live session.
//
// The two halves of an attachment — the session on the connection and the
// connection on the session — are written under both locks at once, so a
// concurrent disconnect sees either a whole attachment or none of it. Written
// separately, a disconnect landing between them clears the connection's half
// and finds nothing to remove from the session's; attach then registers a dead
// connection that nothing will ever remove, and the session stays acquired —
// and so does the service's lock on it — for the life of the process.
//
// The transport is asked whether it is closed before either lock is taken:
// ByteConn is an interface a transport implements, and calling into it under
// the manager's lock would invite a lock cycle through code this package does
// not own. pi reads the flag inline; a flag that flips in the gap only means
// the transport's own close path disconnects the connection a moment later,
// which now unwinds the attachment cleanly.
func (m *sessionManager) attach(ctx context.Context, conn *connState, live *liveSession) error {
	closed := conn.conn.Closed()

	m.mu.Lock()
	conn.mu.Lock()
	usable := !conn.disconnected && conn.stage == stageReady && !closed
	if usable {
		conn.sessionIDs[live.id] = struct{}{}
		live.connections[conn] = struct{}{}
	}
	conn.mu.Unlock()
	m.mu.Unlock()

	if !usable {
		if err := m.maybeDispose(ctx, live); err != nil {
			m.srv.reportError(err)
		}
		return NewError(protocol.ErrorInvalidRequest, "Connection closed while attaching to a session", nil)
	}
	return nil
}

func (m *sessionManager) requireAttached(conn *connState, sessionID string) (*liveSession, error) {
	if !conn.attached(sessionID) {
		return nil, NewError(protocol.ErrorInvalidRequest,
			"Connection is not attached to session "+sessionID, nil)
	}
	m.mu.Lock()
	live := m.live[sessionID]
	missing := live == nil || live.terminal || live.disposing != nil
	m.mu.Unlock()
	if missing {
		return nil, NewError(protocol.ErrorNotFound, "Session is not live: "+sessionID, nil)
	}
	return live, nil
}

func (m *sessionManager) scheduleMaybeDispose(live *liveSession) {
	m.goSafely(live, "releasing an idle session runtime", func() error {
		return m.maybeDispose(m.srv.opContext(), live)
	})
}

// maybeDispose releases the runtime when nothing is holding it: no attached
// connection, no operation in flight, and either the session is idle or its
// runtime already failed.
func (m *sessionManager) maybeDispose(ctx context.Context, live *liveSession) error {
	held, disposing := m.disposalBlocked(live)
	if disposing != nil {
		<-disposing
		return live.disposeErr
	}
	if held {
		return nil
	}
	// Phase is a runtime call, so it happens outside the lock: a runtime is
	// free to take its own lock there, and may already hold one when it calls
	// back into the manager.
	m.mu.Lock()
	terminal := live.terminal
	m.mu.Unlock()
	if !terminal && live.runtime.Phase() != protocol.PhaseIdle {
		return nil
	}

	m.mu.Lock()
	if live.disposing != nil {
		disposing := live.disposing
		m.mu.Unlock()
		<-disposing
		return live.disposeErr
	}
	if m.srv.isClosing() || !live.ready || len(live.connections) > 0 || live.operationCount > 0 {
		m.mu.Unlock()
		return nil
	}
	done := make(chan struct{})
	live.disposing = done
	unsubscribe := live.unsubscribe
	m.mu.Unlock()

	unsubscribe()
	err := live.runtime.Dispose(ctx)
	m.mu.Lock()
	if m.live[live.id] == live {
		m.removeLiveLocked(live.id)
	}
	m.mu.Unlock()
	live.disposeErr = err
	close(done)

	if !m.srv.isClosing() {
		m.srv.snapshots.broadcast()
	}
	return err
}

// disposalBlocked reports whether anything currently prevents disposal, and
// hands back the in-flight disposal to wait on when there is one.
func (m *sessionManager) disposalBlocked(live *liveSession) (held bool, disposing chan struct{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if live.disposing != nil {
		return true, live.disposing
	}
	blocked := m.srv.isClosing() ||
		!live.ready ||
		len(live.connections) > 0 ||
		live.operationCount > 0
	return blocked, nil
}

func (m *sessionManager) removeLiveLocked(id string) {
	delete(m.live, id)
	for i, existing := range m.liveOrder {
		if existing == id {
			m.liveOrder = append(m.liveOrder[:i], m.liveOrder[i+1:]...)
			return
		}
	}
}
