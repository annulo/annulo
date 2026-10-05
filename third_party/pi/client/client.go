package client

import (
	"context"
	"fmt"
	"strconv"
	"sync"

	"github.com/sky-valley/pi/protocol"
)

// Options configures a Client. TransportFactory is required.
type Options struct {
	// TransportFactory produces a fresh transport per connection attempt, so a
	// Client can reconnect without its owner rebuilding anything.
	TransportFactory TransportFactory
	// MaxFrameLength bounds one framed message in both directions. A nil
	// pointer takes protocol.DefaultMaxFrameLength.
	MaxFrameLength *int
	// OnListenerError reports subscriber failures. Subscribers run inside the
	// client's own goroutines, so a panicking one is contained and reported
	// here rather than being allowed to corrupt client state.
	OnListenerError func(error)
}

// CreateSessionOptions are the optional properties of a new session. Every
// field is a pointer because pi omits unset properties from the wire message
// entirely, and an omitted `cwd` is not the same request as an empty one.
type CreateSessionOptions struct {
	Cwd           *string
	Name          *string
	Model         *protocol.ModelRef
	ThinkingLevel *protocol.ThinkingLevel
}

// Client is a connected view of a pi server: the connection, the cached state,
// the in-flight requests, and the session leases this process holds. It is the
// port of pi's PiClient (packages/client/src/client.ts).
//
// All exported methods are safe for concurrent use. Listeners registered
// through Subscribe, OnEvent, OnConnectionStateChange, or a SessionHandle run
// on whichever goroutine produced the notification — usually the transport's
// reading goroutine — so a listener must not block, and must start a new
// goroutine if it wants to issue a request or reconnect.
//
// Lock ordering inside the package is sessionLease.mu, then Client.mu, then
// Connection.mu, then State.mu. No lock is held while a callback runs, with one
// documented exception: ConnectionOptions.OnReset, which exists precisely to be
// atomic with the Connection claiming an attempt.
type Client struct {
	conn       *Connection
	state      *State
	frameLimit int

	mu                       sync.Mutex
	disposed                 bool
	requestSequence          uint64
	pendingRequests          map[string]*pendingRequest
	leaseCounts              map[string]int
	exclusiveLeases          map[string]*leaseToken
	leaseGenerations         map[string]uint64
	attachments              map[string]*sharedOp
	detachments              map[string]*sharedOp
	reconciliations          map[string]*sharedOp
	cleanupRequired          map[string]bool
	connectionStateListeners *listenerSet[ConnectionStateChange]

	// closed is closed when teardown has finished, so a caller that lost the
	// race to dispose still learns when the client actually is disposed.
	closed chan struct{}
}

// pendingRequest is one request waiting for its response. It settles once,
// either from the reader goroutine correlating a response or from the
// disconnect path rejecting everything in flight.
type pendingRequest struct {
	command protocol.Command
	done    chan struct{}
	once    sync.Once
	result  protocol.CommandResult
	err     error
}

func (p *pendingRequest) settle(result protocol.CommandResult, err error) {
	p.once.Do(func() {
		p.result = result
		p.err = err
		close(p.done)
	})
}

// sharedOp is one in-flight operation several callers wait on together — pi
// stores a shared promise in a Map for exactly this, so that a second acquirer
// joins the attach already in flight instead of issuing a duplicate.
//
// DIVERGENCE (deliberate): the caller that creates the op also drives it, on
// its own goroutine and with its own context, rather than the work running
// detached. That keeps cancellation attached to a real caller; the cost is that
// cancelling the first caller's context fails the shared op for everyone
// waiting on it, which pi cannot express because it has no cancellation at all.
type sharedOp struct {
	done chan struct{}
	once sync.Once
	err  error
}

func newSharedOp() *sharedOp { return &sharedOp{done: make(chan struct{})} }

func (o *sharedOp) settle(err error) {
	o.once.Do(func() {
		o.err = err
		close(o.done)
	})
}

// wait blocks until the op settles, returning its error.
func (o *sharedOp) wait(ctx context.Context) error {
	select {
	case <-o.done:
		return o.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// waitSettled blocks until the op settles, discarding its error. pi spells this
// `await detachment.catch(() => {})`: a failed detach must serialize a fresh
// acquisition behind it, not veto it.
func (o *sharedOp) waitSettled(ctx context.Context) error {
	select {
	case <-o.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// New builds an unconnected Client.
func New(opts Options) (*Client, error) {
	c := &Client{
		state:                    NewState(opts.OnListenerError),
		pendingRequests:          map[string]*pendingRequest{},
		leaseCounts:              map[string]int{},
		exclusiveLeases:          map[string]*leaseToken{},
		leaseGenerations:         map[string]uint64{},
		attachments:              map[string]*sharedOp{},
		detachments:              map[string]*sharedOp{},
		reconciliations:          map[string]*sharedOp{},
		cleanupRequired:          map[string]bool{},
		connectionStateListeners: newListenerSet[ConnectionStateChange](),
		closed:                   make(chan struct{}),
	}
	conn, err := NewConnection(ConnectionOptions{
		TransportFactory: opts.TransportFactory,
		MaxFrameLength:   opts.MaxFrameLength,
		OnReset:          c.state.Reset,
		OnHandshake:      c.state.ApplyServerSnapshot,
		OnMessage:        c.handleMessage,
		OnStateChange:    c.handleConnectionStateChange,
	})
	if err != nil {
		return nil, err
	}
	c.conn = conn
	c.frameLimit = conn.MaxFrameLength()
	return c, nil
}

// Connect builds a Client and completes its handshake, disposing of it if the
// handshake fails so a caller never has to clean up a client it never got.
func Connect(ctx context.Context, opts Options) (*Client, error) {
	c, err := New(opts)
	if err != nil {
		return nil, err
	}
	if _, err := c.Connect(ctx); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

// Disposed reports whether Close has been called.
func (c *Client) Disposed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.disposed
}

// ConnectionState reports where the underlying connection sits.
func (c *Client) ConnectionState() ConnectionState { return c.conn.State() }

// Connected reports whether the handshake has completed and not yet ended.
func (c *Client) Connected() bool { return c.conn.State() == Connected }

// Snapshot returns the latest server snapshot, or nil before the handshake.
//
// The snapshot is the cached one, not a copy, and the same pointer is handed to
// every caller and every subscriber. Treat it as read-only: mutating it in place
// changes what everyone else is looking at. New state always arrives as a fresh
// snapshot replacing this one, so a caller that keeps the pointer keeps a
// consistent view of one moment rather than a value that shifts underneath it.
func (c *Client) Snapshot() *protocol.ServerSnapshot { return c.state.Snapshot() }

// Connect performs the handshake and returns the server's snapshot. Reconnects
// start from a clean cache: a new server, or a restarted one, must not be
// described by the previous connection's leftovers.
//
// DIVERGENCE (deliberate): pi tests the connection's state here and resets the
// cache before calling connect(). Two steps, and in Go two goroutines can both
// pass the test — after which the one that loses the race for the attempt wipes
// the cache the winner has already filled, leaving a connected client with no
// snapshot. The reset is handed to the Connection instead, which performs it in
// the same critical section that claims the attempt.
func (c *Client) Connect(ctx context.Context) (*protocol.ServerSnapshot, error) {
	if err := c.assertNotDisposed(); err != nil {
		return nil, err
	}
	return c.conn.Connect(ctx)
}

// Reconnect is Connect under pi's other name for it, kept so ported call sites
// read unchanged.
func (c *Client) Reconnect(ctx context.Context) (*protocol.ServerSnapshot, error) {
	return c.Connect(ctx)
}

// Disconnect ends the current connection. An empty reason takes pi's default.
func (c *Client) Disconnect(reason string) {
	if reason == "" {
		reason = defaultConnectionDisconnectReason
	}
	c.conn.Disconnect(&DisconnectedError{Message: reason})
}

// Subscribe registers a listener for every server snapshot.
func (c *Client) Subscribe(listener func(*protocol.ServerSnapshot)) (Unsubscribe, error) {
	if err := c.assertNotDisposed(); err != nil {
		return nil, err
	}
	return c.state.Subscribe(listener), nil
}

// OnEvent registers a listener for every server event.
func (c *Client) OnEvent(listener func(protocol.ServerEvent)) (Unsubscribe, error) {
	if err := c.assertNotDisposed(); err != nil {
		return nil, err
	}
	return c.state.OnEvent(listener), nil
}

// OnConnectionStateChange registers a listener for connection lifecycle
// transitions.
func (c *Client) OnConnectionStateChange(listener func(ConnectionStateChange)) (Unsubscribe, error) {
	if err := c.assertNotDisposed(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	listeners := c.connectionStateListeners
	id := listeners.add(listener)
	var done sync.Once
	return func() {
		done.Do(func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			listeners.remove(id)
		})
	}, nil
}

// ListSessions asks the server for every session it knows about.
func (c *Client) ListSessions(ctx context.Context) ([]protocol.SessionMetadata, error) {
	result, err := c.request(ctx, protocol.NewListCommand())
	if err != nil {
		return nil, err
	}
	list, ok := result.(*protocol.ListResult)
	if !ok {
		return nil, unexpectedResult("list", result)
	}
	return list.Sessions, nil
}

// CreateSession starts a new session and returns an exclusive lease on it. The
// creator owns what it created, so the lease is exclusive without being asked.
func (c *Client) CreateSession(ctx context.Context, opts CreateSessionOptions) (*SessionHandle, error) {
	result, err := c.request(ctx, &protocol.CreateCommand{
		Command:       "create",
		Cwd:           opts.Cwd,
		Name:          opts.Name,
		Model:         opts.Model,
		ThinkingLevel: opts.ThinkingLevel,
	})
	if err != nil {
		return nil, err
	}
	session, ok := result.(*protocol.SessionResult)
	if !ok {
		return nil, unexpectedResult("create", result)
	}
	token, err := c.reserveSessionLease(session.Session.ID, LeaseExclusive)
	if err != nil {
		return nil, err
	}
	return c.newLease(session.Session.ID, token), nil
}

// AttachSession takes a shared lease on an existing session.
func (c *Client) AttachSession(ctx context.Context, sessionID string) (*SessionHandle, error) {
	return c.AcquireSession(ctx, sessionID, LeaseShared)
}

// AcquireSession takes a lease on an existing session, attaching to it first if
// this client is not attached already.
//
// Acquisition serializes behind any detach still in flight for the same
// session, and behind the cleanup detach owed by a lease that was closed after
// its own detach failed — otherwise a reattach could race the server's teardown
// of the previous attachment and leave the session attached to nobody.
func (c *Client) AcquireSession(
	ctx context.Context,
	sessionID string,
	mode SessionLeaseMode,
) (*SessionHandle, error) {
	if err := c.assertNotDisposed(); err != nil {
		return nil, err
	}
	token, err := c.reserveSessionLease(sessionID, mode)
	if err != nil {
		return nil, err
	}
	handle, err := c.acquire(ctx, sessionID, token)
	if err != nil {
		c.releaseSessionLease(sessionID, token)
		return nil, err
	}
	return handle, nil
}

func (c *Client) acquire(ctx context.Context, sessionID string, token *leaseToken) (*SessionHandle, error) {
	c.mu.Lock()
	detachment := c.detachments[sessionID]
	c.mu.Unlock()

	if detachment != nil {
		if err := detachment.waitSettled(ctx); err != nil {
			return nil, err
		}
	}

	// Read after the wait, exactly where pi reads it. The detach this
	// acquisition just waited on may be the one that failed, and a failed
	// detach is what records the cleanup owed. Reading it before the wait
	// answers a question that had not been decided yet, and the lease minted
	// on that answer holds a session the server still has attached to a
	// connection nobody is using.
	c.mu.Lock()
	needsCleanup := c.cleanupRequired[sessionID]
	c.mu.Unlock()

	reconciled := false
	if needsCleanup {
		var err error
		reconciled, err = c.reconcileSessionCleanup(ctx, sessionID)
		if err != nil {
			return nil, err
		}
	}

	// A reconciled session was just detached on the server, so the cached
	// "attached" bit cannot be trusted and the attach is reissued regardless.
	if reconciled || !c.state.IsSessionAttached(sessionID) {
		if err := c.ensureAttached(ctx, sessionID); err != nil {
			return nil, err
		}
	}
	return c.newLease(sessionID, token), nil
}

// ensureAttached issues the attach, or joins the one already in flight.
func (c *Client) ensureAttached(ctx context.Context, sessionID string) error {
	c.mu.Lock()
	if existing := c.attachments[sessionID]; existing != nil {
		c.mu.Unlock()
		return existing.wait(ctx)
	}
	op := newSharedOp()
	c.attachments[sessionID] = op
	c.mu.Unlock()

	err := c.attachSession(ctx, sessionID)

	c.mu.Lock()
	if c.attachments[sessionID] == op {
		delete(c.attachments, sessionID)
	}
	c.mu.Unlock()
	op.settle(err)
	return err
}

// attachSession drops the cached snapshot before attaching and puts it back if
// the attach fails.
//
// The server answers an attach with a snapshot whose revision counts from the
// new attachment, which can be lower than the one already cached. Forgetting
// first is what lets that lower revision be accepted instead of discarded by
// State's staleness guard.
func (c *Client) attachSession(ctx context.Context, sessionID string) error {
	previous := c.state.ForgetSessionSnapshot(sessionID)
	if _, err := c.request(ctx, protocol.NewAttachCommand(sessionID)); err != nil {
		if previous != nil {
			c.state.RestoreSessionSnapshot(previous)
		}
		return err
	}
	return nil
}

// reconcileSessionCleanup pays off the detach owed by a lease that was closed
// after its own detach failed. It reports whether a reconciliation ran, so the
// caller knows to reattach.
func (c *Client) reconcileSessionCleanup(ctx context.Context, sessionID string) (bool, error) {
	c.mu.Lock()
	if !c.cleanupRequired[sessionID] {
		c.mu.Unlock()
		return false, nil
	}
	if existing := c.reconciliations[sessionID]; existing != nil {
		c.mu.Unlock()
		return true, existing.wait(ctx)
	}
	op := newSharedOp()
	c.reconciliations[sessionID] = op
	c.mu.Unlock()

	_, err := c.request(ctx, protocol.NewDetachCommand(sessionID))

	c.mu.Lock()
	if err == nil {
		delete(c.cleanupRequired, sessionID)
	}
	if c.reconciliations[sessionID] == op {
		delete(c.reconciliations, sessionID)
	}
	c.mu.Unlock()
	op.settle(err)
	return true, err
}

// request issues one command and waits for its response.
//
// A cancelled context abandons the wait but leaves the request registered, so
// the response the server eventually sends is still correlated and discarded.
// Forgetting it instead would make that response look unsolicited, and an
// unsolicited response tears the connection down.
func (c *Client) request(ctx context.Context, command protocol.Command) (protocol.CommandResult, error) {
	if err := c.assertNotDisposed(); err != nil {
		return nil, err
	}
	if !c.Connected() {
		return nil, &DisconnectedError{}
	}

	c.mu.Lock()
	c.requestSequence++
	id := "request-" + strconv.FormatUint(c.requestSequence, 10)
	pending := &pendingRequest{command: command, done: make(chan struct{})}
	c.pendingRequests[id] = pending
	c.mu.Unlock()

	frame, err := protocol.EncodeClientMessage(
		protocol.NewRequest(id, command),
		&protocol.FrameOptions{MaxFrameLength: &c.frameLimit},
	)
	if err == nil {
		err = c.conn.Send(frame)
	}
	// A send that fails terminally has already rejected this request through
	// the disconnect path, in which case the take below finds nothing and the
	// wait returns that rejection instead.
	if err != nil {
		if taken := c.takePendingRequest(id); taken != nil {
			return nil, err
		}
	}

	select {
	case <-pending.done:
		return pending.result, pending.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// handleMessage dispatches one post-handshake server message.
func (c *Client) handleMessage(message protocol.ServerMessage) {
	switch typed := message.(type) {
	case *protocol.EventEnvelope:
		// Leases are invalidated before the event is applied, so a subscriber
		// woken by it already sees the session as gone.
		if removed, ok := typed.Event.(*protocol.SessionRemovedEvent); ok {
			c.invalidateSessionLeases(removed.SessionID)
		}
		c.state.ApplyEvent(typed.Event)
	case *protocol.ResponseEnvelope:
		c.handleResponse(typed)
	}
}

func (c *Client) handleResponse(response *protocol.ResponseEnvelope) {
	pending := c.takePendingRequest(response.ID)
	if pending == nil {
		// The server answered something we never asked. The request/response
		// correlation is the only thing keeping results attached to callers, so
		// a violation of it is not recoverable.
		c.conn.Disconnect(&protocol.ValidationError{Msg: "Response has no matching request"})
		return
	}
	if !response.OK {
		pending.settle(nil, newServerError(response.Error))
		return
	}
	if response.Result.ResultCommandName() != pending.command.CommandName() {
		err := &protocol.ValidationError{Msg: fmt.Sprintf(
			"Response command %s does not match %s",
			response.Result.ResultCommandName(),
			pending.command.CommandName(),
		)}
		pending.settle(nil, err)
		c.conn.Disconnect(err)
		return
	}
	c.state.ApplyResult(response.Result)
	pending.settle(response.Result, nil)
}

func (c *Client) handleConnectionStateChange(change ConnectionStateChange) {
	if change.State == Disconnected {
		c.state.ClearAttachments()
		c.invalidateAllSessionLeases()
		err := change.Err
		if err == nil {
			err = &DisconnectedError{}
		}
		c.rejectPendingRequests(err)
	}
	c.notifyConnectionStateListeners(change)
}

func (c *Client) takePendingRequest(id string) *pendingRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	pending, ok := c.pendingRequests[id]
	if !ok {
		return nil
	}
	delete(c.pendingRequests, id)
	return pending
}

func (c *Client) rejectPendingRequests(err error) {
	c.mu.Lock()
	pending := c.pendingRequests
	c.pendingRequests = map[string]*pendingRequest{}
	c.mu.Unlock()
	for _, request := range pending {
		request.settle(nil, err)
	}
}

// Close disposes of the client: it stops accepting work, rejects everything in
// flight, drops the connection, and invalidates every lease. It is pi's
// dispose(), named for Go's io.Closer convention, and is idempotent. A caller
// that arrives second waits for the first caller's teardown, so Close returning
// always means the client is fully disposed — for every caller, not just the
// one that won the race.
//
// That guarantee is pi's, not an embellishment. pi's dispose() memoises an
// already-resolved promise and then tears down synchronously, so run-to-
// completion hands even a re-entrant caller's continuation a fully torn-down
// client. Go has to build with a channel what JavaScript gets from its event
// loop.
//
// It is also re-entrant, which is why it is not a sync.Once: disposal drops the
// connection, and dropping the connection notifies connection-state listeners.
// "Tear the client down when the connection dies" is a listener a reader would
// think is allowed, and the Close it issues must not wait for the teardown it
// is itself part of. That is why disposal publishes completion BEFORE it
// notifies: by the time any listener runs, c.closed is already closed, so a
// nested Close finds a finished teardown and returns rather than deadlocking.
// No flag distinguishes the nested caller from any other — nothing has to,
// which is the point.
//
// DIVERGENCE (deliberate): pi runs #state.dispose() AFTER #connection.disconnect,
// so a pi connection-state listener still sees live session subscribers; here
// State is disposed before the notification, so such a listener sees them
// already cleared. The observable difference is one statement wide even in pi —
// a subscription registered from that listener is destroyed by the very next
// line of pi's dispose() — and it buys the guarantee above, which pi gets for
// free and Go cannot get any other way: the only alternatives are to leave a
// concurrent caller able to observe an undisposed State, or to identify the
// tearing-down goroutine, which Go does not support. Same reason State.Dispose
// itself already diverges from pi: single-threaded assumptions do not survive
// the port.
//
// It always reports nil: disposal is local teardown with nothing left to fail,
// and the error result exists only so Client satisfies io.Closer.
func (c *Client) Close() error {
	c.mu.Lock()
	if c.disposed {
		c.mu.Unlock()
		<-c.closed
		return nil
	}
	c.disposed = true
	c.mu.Unlock()

	err := &DisposedError{}
	// Rejecting first means the disconnect below finds nothing in flight,
	// so callers learn the client was disposed rather than merely dropped.
	c.rejectPendingRequests(err)

	// Tear the connection down but hold its notification: State() already
	// reports Disconnected from here on, and the callback is delivered only
	// once this client has finished disposing itself.
	notify := c.conn.DisconnectDeferred(err)

	// The work handleConnectionStateChange would have done for this transition,
	// done here instead because the notification is deferred. Idempotent, so
	// the deferred callback re-running it below is a no-op.
	c.state.ClearAttachments()
	c.invalidateAllSessionLeases()
	c.rejectPendingRequests(err)
	c.state.Dispose()

	// Published before any listener runs, and before a panic in one could
	// escape: a caller waiting here must never be stranded by someone else's
	// callback, and a nested Close must find this already closed.
	close(c.closed)

	if notify != nil {
		notify()
	}

	// Cleared last, so subscribers still see the disconnect that disposal
	// caused. Cleared in place rather than replaced: the ids a live
	// unsubscribe closure holds must not be handed out again.
	c.mu.Lock()
	c.connectionStateListeners.clear()
	c.mu.Unlock()
	return nil
}

func (c *Client) assertNotDisposed() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.disposed {
		return &DisposedError{}
	}
	return nil
}

func (c *Client) notifyConnectionStateListeners(change ConnectionStateChange) {
	// Walks the live set by id, as State's own notifications do and as pi's
	// #notifyConnectionStateListeners does over its Set: a listener registered
	// during this notification is reached by it, one cancelled during it is not,
	// and no lock is held while a listener runs.
	for last := uint64(0); ; {
		c.mu.Lock()
		entry, ok := c.connectionStateListeners.next(last)
		c.mu.Unlock()
		if !ok {
			return
		}
		last = entry.id
		// Reuses State's containment so a panicking connection-state listener
		// is reported through the same handler as a panicking subscriber, and
		// cannot stop the listeners behind it.
		c.state.call(func() { entry.fn(change) })
	}
}

// unexpectedResult reports a result whose type does not match the command that
// asked for it. The response correlator already rejects a mismatched command
// name, so reaching this is a bug in this package rather than a hostile peer.
func unexpectedResult(command string, result protocol.CommandResult) error {
	name := "nothing"
	if result != nil {
		name = result.ResultCommandName()
	}
	return &protocol.ValidationError{Msg: fmt.Sprintf("Command %s answered with %s", command, name)}
}

// leaseToken identifies one lease. Its identity, not its contents, is what
// distinguishes the exclusive holder from a later lease on the same session.
type leaseToken struct{ mode SessionLeaseMode }

func (c *Client) reserveSessionLease(sessionID string, mode SessionLeaseMode) (*leaseToken, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	count := c.leaseCounts[sessionID]
	if mode == LeaseExclusive && count > 0 {
		return nil, &SessionOwnershipError{
			SessionID: sessionID,
			Message:   "Session " + sessionID + " already has an active lease",
		}
	}
	if mode == LeaseShared {
		if _, exclusive := c.exclusiveLeases[sessionID]; exclusive {
			return nil, &SessionOwnershipError{
				SessionID: sessionID,
				Message:   "Session " + sessionID + " has an exclusive lease",
			}
		}
	}
	token := &leaseToken{mode: mode}
	c.leaseCounts[sessionID] = count + 1
	if mode == LeaseExclusive {
		c.exclusiveLeases[sessionID] = token
	}
	return token, nil
}

func (c *Client) releaseSessionLease(sessionID string, token *leaseToken) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if count := c.leaseCounts[sessionID]; count <= 1 {
		delete(c.leaseCounts, sessionID)
	} else {
		c.leaseCounts[sessionID] = count - 1
	}
	if c.exclusiveLeases[sessionID] == token {
		delete(c.exclusiveLeases, sessionID)
	}
}

func (c *Client) leaseCount(sessionID string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.leaseCounts[sessionID]
}

func (c *Client) leaseGeneration(sessionID string) uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.leaseGenerations[sessionID]
}

// invalidateSessionLeases retires every lease on a session by bumping its
// generation. Leases notice lazily, which is what lets an invalidated lease be
// closed without issuing a detach for a session that no longer exists.
func (c *Client) invalidateSessionLeases(sessionID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.invalidateSessionLeasesLocked(sessionID)
}

func (c *Client) invalidateSessionLeasesLocked(sessionID string) {
	delete(c.leaseCounts, sessionID)
	delete(c.exclusiveLeases, sessionID)
	delete(c.cleanupRequired, sessionID)
	c.leaseGenerations[sessionID]++
}

func (c *Client) invalidateAllSessionLeases() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for sessionID := range c.leaseCounts {
		c.invalidateSessionLeasesLocked(sessionID)
	}
	clear(c.cleanupRequired)
}

func (c *Client) markCleanupRequired(sessionID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cleanupRequired[sessionID] = true
}

func (c *Client) setDetachment(sessionID string, op *sharedOp) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.detachments[sessionID] = op
}

func (c *Client) clearDetachment(sessionID string, op *sharedOp) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.detachments[sessionID] == op {
		delete(c.detachments, sessionID)
	}
}

// leaseState is where one lease sits in its own lifecycle.
type leaseState int

const (
	leaseActive leaseState = iota
	leaseReleasing
	leaseReleased
	leaseInvalidated
)

// sessionLease is the client-side half of a SessionHandle: the lease's own
// lifecycle plus the bookkeeping that decides whether releasing it should
// detach the session on the server.
//
// It is pi's closure over `state`, `releasePromise`, and `generation` inside
// PiClient#createSessionLease, made explicit because Go closures over mutable
// state shared between goroutines need a lock to hold anyway.
type sessionLease struct {
	client     *Client
	sessionID  string
	token      *leaseToken
	generation uint64

	mu        sync.Mutex
	state     leaseState
	releasing *sharedOp
}

func (c *Client) newLease(sessionID string, token *leaseToken) *SessionHandle {
	// leaseGenerations is written by invalidation and never deleted, not even
	// when the last lease on a session goes away: deleting would take the
	// session back to generation 0, and a lease minted before an invalidation
	// would then match again and consider itself live.
	c.mu.Lock()
	generation := c.leaseGenerations[sessionID]
	c.mu.Unlock()
	lease := &sessionLease{
		client:     c,
		sessionID:  sessionID,
		token:      token,
		generation: generation,
		state:      leaseActive,
	}
	return newSessionHandle(sessionID, lease)
}

// refreshLocked retires the lease if its session's generation has moved on.
// A lease that is already released or invalidated stays that way.
func (l *sessionLease) refreshLocked() {
	if l.state != leaseActive && l.state != leaseReleasing {
		return
	}
	if l.client.leaseGeneration(l.sessionID) != l.generation {
		l.state = leaseInvalidated
	}
}

func (l *sessionLease) isActive() bool {
	l.mu.Lock()
	l.refreshLocked()
	active := l.state == leaseActive
	l.mu.Unlock()
	return active && l.client.state.IsSessionAttached(l.sessionID)
}

// assertActiveLocked is pi's assertActive. l.mu must be held; it deliberately
// does not call isActive, which would deadlock on that lock.
func (l *sessionLease) assertActiveLocked() error {
	if err := l.client.assertNotDisposed(); err != nil {
		return err
	}
	if !l.client.Connected() {
		return &DisconnectedError{}
	}
	if l.state != leaseActive || !l.client.state.IsSessionAttached(l.sessionID) {
		return &SessionDetachedError{SessionID: l.sessionID}
	}
	return nil
}

func (l *sessionLease) assertActive() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.refreshLocked()
	return l.assertActiveLocked()
}

func (l *sessionLease) sessionSnapshot() *protocol.SessionSnapshot {
	if !l.isActive() {
		return nil
	}
	return l.client.state.SessionSnapshot(l.sessionID)
}

func (l *sessionLease) subscribe(listener func(*protocol.SessionSnapshot)) (Unsubscribe, error) {
	if err := l.assertActive(); err != nil {
		return nil, err
	}
	// Re-checked at delivery time as well: the lease can end between
	// subscribing and the next snapshot, and a retired lease must go quiet.
	return l.client.state.SubscribeSession(l.sessionID, func(snapshot *protocol.SessionSnapshot) {
		if l.isActive() {
			listener(snapshot)
		}
	}), nil
}

func (l *sessionLease) onEvent(listener func(protocol.ServerEvent)) (Unsubscribe, error) {
	if err := l.assertActive(); err != nil {
		return nil, err
	}
	return l.client.state.OnSessionEvent(l.sessionID, func(event protocol.ServerEvent) {
		// Removal is the one event delivered to a retired lease: it is the
		// reason the lease retired, and a subscriber needs to hear it.
		_, removed := event.(*protocol.SessionRemovedEvent)
		if l.isActive() || removed {
			listener(event)
		}
	}), nil
}

func (l *sessionLease) request(ctx context.Context, command protocol.Command) (protocol.CommandResult, error) {
	if err := l.assertActive(); err != nil {
		return nil, err
	}
	return l.client.request(ctx, command)
}

func (l *sessionLease) detach(ctx context.Context) error { return l.release(ctx, false) }

func (l *sessionLease) close(ctx context.Context) error { return l.release(ctx, true) }

// release gives the lease up. relinquishOnFailure decides what a failed detach
// means: Close gives the lease up anyway and records that the session still
// owes the server a detach, while Detach keeps the lease usable so the caller
// can retry.
func (l *sessionLease) release(ctx context.Context, relinquishOnFailure bool) error {
	l.mu.Lock()
	l.refreshLocked()
	switch l.state {
	case leaseReleased, leaseInvalidated:
		// Nothing to give up, and nothing to tell the server: an invalidated
		// lease's session is already gone.
		l.mu.Unlock()
		return nil
	}
	if l.releasing != nil {
		op := l.releasing
		l.mu.Unlock()
		return op.wait(ctx)
	}
	if err := l.assertActiveLocked(); err != nil {
		l.mu.Unlock()
		return err
	}
	l.state = leaseReleasing
	op := newSharedOp()
	l.releasing = op
	l.mu.Unlock()

	detachment, detachErr := l.doRelease(ctx)
	err := detachErr

	l.mu.Lock()
	switch {
	case err == nil:
		l.state = leaseReleased
	default:
		l.refreshLocked()
		switch {
		case l.state == leaseInvalidated:
			// The session disappeared while we were detaching from it, which
			// makes the detach's failure moot.
			err = nil
		case relinquishOnFailure:
			l.client.releaseSessionLease(l.sessionID, l.token)
			l.client.markCleanupRequired(l.sessionID)
			l.state = leaseReleased
		default:
			l.state = leaseActive
			l.releasing = nil
		}
	}
	l.mu.Unlock()

	// The detachment stays published and unsettled until the bookkeeping above
	// is done, so an acquisition parked on it wakes to a decided world: either
	// the lease was given up and the session owes a detach, or the lease is
	// still held. pi gets this from microtask ordering — the rejection handler
	// that records the cleanup runs before the acquirer's continuation — which
	// is ordering Go has to arrange rather than inherit.
	if detachment != nil {
		l.client.clearDetachment(l.sessionID, detachment)
		// The detachment carries the detach's own outcome, not the lease's:
		// waiters only care that it is over, and Close reports success for a
		// lease invalidated mid-detach.
		detachment.settle(detachErr)
	}
	op.settle(err)
	return err
}

// doRelease drops this lease's claim, detaching on the server only when it was
// the last claim on the session. It returns the detachment other acquisitions
// serialize behind, unsettled, for release to finish once its own bookkeeping
// is done — nil when no detach was needed.
func (l *sessionLease) doRelease(ctx context.Context) (*sharedOp, error) {
	client := l.client
	if client.leaseCount(l.sessionID) > 1 {
		client.releaseSessionLease(l.sessionID, l.token)
		return nil, nil
	}
	// Published before the request so a concurrent acquisition serializes
	// behind this detach instead of racing it.
	op := newSharedOp()
	client.setDetachment(l.sessionID, op)
	_, err := client.request(ctx, protocol.NewDetachCommand(l.sessionID))
	if err == nil {
		client.releaseSessionLease(l.sessionID, l.token)
	}
	return op, err
}
