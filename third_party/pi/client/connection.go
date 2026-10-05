package client

import (
	"context"
	"fmt"
	"sync"

	"github.com/sky-valley/pi/protocol"
)

// maxUint32 is the largest frame length the wire's 32-bit prefix can express.
// It is uint64 because it does not fit in an int on a 32-bit build.
const maxUint32 uint64 = 0xffff_ffff

// defaultConnectionDisconnectReason is pi's Connection#disconnect default.
const defaultConnectionDisconnectReason = "Client disconnected"

// ConnectionOptions configures a Connection. Every callback except OnReset is
// required.
//
// OnHandshake, OnMessage and OnStateChange are invoked with no Connection lock
// held, on whichever goroutine produced the event: the first two on the
// transport's reading goroutine, OnStateChange on whichever goroutine caused
// the transition. Such a callback may therefore call back into the Connection —
// pi's listeners do exactly that, disconnecting and reconnecting from inside a
// handshake notification — and the lifecycle is re-checked after every callback
// so a re-entrant change is not undone.
//
// OnReset is the exception, and runs under the lock; see its own comment.
type ConnectionOptions struct {
	// TransportFactory produces a fresh transport per connection attempt.
	TransportFactory TransportFactory
	// MaxFrameLength bounds one framed message in both directions. A nil
	// pointer takes protocol.DefaultMaxFrameLength; pi spells this `??`, so a
	// zero must stay distinguishable from "unset" and is rejected.
	MaxFrameLength *int
	// OnReset is called when a Connect claims the connection for a fresh
	// attempt, before anything else happens on it. It exists so an owner's
	// cache can be dropped in the same step that claims the attempt: pi does
	// the equivalent from its client, which is safe there only because nothing
	// else can run in between. It may be nil.
	//
	// Unlike every other callback here it runs with the Connection's lock held,
	// which is the whole point — atomicity with the claim is what it buys. It
	// must therefore not call back into the Connection. The lock it may take is
	// State's, extending the package's ordering to Connection.mu then State.mu;
	// State never reaches back the other way.
	OnReset func()
	// OnHandshake receives the snapshot carried by the server hello, before
	// the connection is reported as connected.
	OnHandshake func(snapshot *protocol.ServerSnapshot)
	// OnMessage receives every post-handshake server message. Handshake
	// messages never reach it.
	OnMessage func(message protocol.ServerMessage)
	// OnStateChange reports every lifecycle transition.
	OnStateChange func(change ConnectionStateChange)
}

// Connection is one client's protocol-level link to a pi server: it owns the
// handshake, the inbound decoder, and the lifecycle state machine. It is the
// port of pi's Connection (packages/client/src/connection.ts).
//
// A Connection is reusable: after it disconnects, Connect starts a fresh
// attempt with a fresh transport and a fresh decoder. Every attempt carries a
// monotonic id so a late callback from a superseded transport is ignored
// instead of corrupting the current attempt.
//
// All exported methods are safe for concurrent use.
type Connection struct {
	opts           ConnectionOptions
	maxFrameLength int

	mu sync.Mutex
	// lifecycle is nil exactly when the state is Disconnected. Its identity —
	// not just its contents — is what the re-entrancy checks compare, standing
	// in for pi's object-identity comparisons against `connected`.
	lifecycle *connectionLifecycle
	sequence  uint64
}

// connectionLifecycle is the mutable half of one connection attempt.
type connectionLifecycle struct {
	state   ConnectionState
	id      uint64
	decoder *protocol.ServerMessageDecoder
	// transport is nil between the start of an attempt and the moment the
	// factory returns. Server bytes arriving in that window are a protocol
	// violation: we cannot have sent the hello yet.
	transport Transport
	// handshake is cleared once the attempt has been reported as connected, so
	// a later failure does not try to settle it again.
	handshake *handshake
}

// handshake carries the result of one connection attempt to whoever called
// Connect. It settles exactly once.
//
// DIVERGENCE (deliberate): pi threads a PromiseResolvers through the lifecycle
// (packages/client/src/promise.ts). Go has no promises; a closed channel plus a
// sync.Once is the direct equivalent and needs no separate file.
type handshake struct {
	done     chan struct{}
	once     sync.Once
	snapshot *protocol.ServerSnapshot
	err      error
}

func newHandshake() *handshake { return &handshake{done: make(chan struct{})} }

func (h *handshake) settle(snapshot *protocol.ServerSnapshot, err error) {
	h.once.Do(func() {
		h.snapshot = snapshot
		h.err = err
		close(h.done)
	})
}

// NewConnection validates the options and returns an idle Connection.
//
// pi throws a TypeError from the constructor for an out-of-range frame limit;
// Go reports it as an error so a caller cannot build an unusable Connection.
func NewConnection(opts ConnectionOptions) (*Connection, error) {
	maxFrameLength := protocol.DefaultMaxFrameLength
	if opts.MaxFrameLength != nil {
		maxFrameLength = *opts.MaxFrameLength
	}
	if maxFrameLength <= 0 || uint64(maxFrameLength) > maxUint32 {
		return nil, fmt.Errorf("PiClient maxFrameLength must be an integer between 1 and %d", maxUint32)
	}
	return &Connection{opts: opts, maxFrameLength: maxFrameLength}, nil
}

// State reports where the connection currently sits in its lifecycle.
func (c *Connection) State() ConnectionState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stateLocked()
}

func (c *Connection) stateLocked() ConnectionState {
	if c.lifecycle == nil {
		return Disconnected
	}
	return c.lifecycle.state
}

// MaxFrameLength is the resolved per-frame limit, in bytes.
func (c *Connection) MaxFrameLength() int { return c.maxFrameLength }

// Connect opens a transport, performs the handshake, and returns the server
// snapshot the server hello carried. It fails if the connection is not idle.
//
// DIVERGENCE (deliberate): pi's connect() returns a promise and opens the
// transport in the background. Connect blocks until the handshake settles,
// which is what a Go caller wants, and takes a context so a caller can give up
// on a server that never answers — cancelling the context tears the attempt
// down rather than leaving it dangling. A callback that wants to reconnect
// (pi's listeners do this) must therefore call Connect from a new goroutine
// unless its transport delivers data synchronously.
//
// ctx covers the whole attempt, transport establishment included: it is handed
// to the TransportFactory, which is expected to abandon a dial that is still in
// progress when ctx is done. A factory that ignores it holds Connect for as
// long as it takes to return.
func (c *Connection) Connect(ctx context.Context) (*protocol.ServerSnapshot, error) {
	decoder, err := protocol.NewServerMessageDecoder(&protocol.FrameOptions{MaxFrameLength: &c.maxFrameLength})
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	if c.lifecycle != nil {
		state := c.lifecycle.state
		c.mu.Unlock()
		return nil, &DisconnectedError{Message: "PiClient is already " + string(state)}
	}
	if c.opts.OnReset != nil {
		c.opts.OnReset()
	}
	c.sequence++
	id := c.sequence
	hs := newHandshake()
	c.lifecycle = &connectionLifecycle{state: Connecting, id: id, decoder: decoder, handshake: hs}
	c.mu.Unlock()

	c.opts.OnStateChange(ConnectionStateChange{State: Connecting})

	c.openTransport(ctx, id)

	select {
	case <-hs.done:
	case <-ctx.Done():
		// Tear the attempt down so the transport is closed and the state
		// machine agrees with the caller that this attempt is over. The
		// handshake is settled by failAndClose, so the receive below is ready.
		c.failAndClose(asDisconnectedError(ctx.Err()))
		<-hs.done
	}
	return hs.snapshot, hs.err
}

// Disconnect ends the current attempt. reason nil takes pi's default message.
// It is a no-op when the connection is already idle.
//
// DIVERGENCE (deliberate): pi has both disconnect(string|Error) and
// fail(Error). Go has no union type and the two differ only in that default,
// so one method taking an error covers both.
func (c *Connection) Disconnect(reason error) {
	if reason == nil {
		reason = &DisconnectedError{Message: defaultConnectionDisconnectReason}
	}
	c.failAndClose(reason)
}

// Send writes one already-encoded frame.
//
// It returns a DisconnectedError if the connection is not connected. A
// transport failure is not returned: like pi, it is terminal for the whole
// connection and is reported by moving to Disconnected, which rejects every
// request in flight. Reporting it twice would let a caller believe only its own
// send had failed.
func (c *Connection) Send(frame []byte) error {
	c.mu.Lock()
	lifecycle := c.lifecycle
	if lifecycle == nil || lifecycle.state != Connected {
		c.mu.Unlock()
		return &DisconnectedError{}
	}
	transport := lifecycle.transport
	c.mu.Unlock()

	if err := transport.Send(frame); err != nil {
		// Guard on transport identity: by the time a slow send fails, the
		// Connection may already be on a newer attempt that must not be torn
		// down for a dead one's failure.
		c.failTransport(transport, asDisconnectedError(err))
	}
	return nil
}

// openTransport builds the transport for attempt id and sends the client hello.
func (c *Connection) openTransport(ctx context.Context, id uint64) {
	handlers := TransportHandlers{
		OnData:  func(chunk []byte) { c.handleData(id, chunk) },
		OnClose: func() { c.handleClose(id) },
		OnError: func(err error) { c.handleError(id, err) },
	}

	// The factory may deliver data synchronously, before it has returned a
	// transport — pi's tests cover exactly that — so no lock is held here.
	transport, err := c.opts.TransportFactory(ctx, handlers)
	if err != nil {
		// pi calls #fail, not #failAndClose: there is no transport to close.
		c.failIfCurrent(id, asDisconnectedError(err))
		return
	}
	if transport == nil {
		c.failIfCurrent(id, &DisconnectedError{Message: "Transport factory returned no transport"})
		return
	}

	c.mu.Lock()
	lifecycle := c.lifecycle
	if lifecycle == nil || lifecycle.state != Connecting || lifecycle.id != id {
		c.mu.Unlock()
		// The attempt was superseded or torn down while the factory ran; the
		// transport it produced belongs to nobody.
		_ = transport.Close()
		return
	}
	lifecycle.transport = transport
	c.mu.Unlock()

	// pi evaluates this encode inside the try that wraps transport.send, so a
	// hello that cannot be encoded fails the connection the same way a hello
	// that cannot be sent does. The distinction it declines to draw is the right
	// one: either way the connection never said hello.
	hello, err := protocol.EncodeClientMessage(
		protocol.NewClientHello(),
		&protocol.FrameOptions{MaxFrameLength: &c.maxFrameLength},
	)
	if err == nil {
		err = transport.Send(hello)
	}
	if err != nil {
		c.failAndCloseIfCurrent(id, asDisconnectedError(err))
	}
}

// handleData feeds one inbound chunk through the decoder and dispatches
// whatever messages it completed.
func (c *Connection) handleData(id uint64, chunk []byte) {
	c.mu.Lock()
	lifecycle := c.lifecycle
	if lifecycle == nil || lifecycle.id != id {
		c.mu.Unlock()
		return
	}
	if lifecycle.state == Connecting && lifecycle.transport == nil {
		c.mu.Unlock()
		c.failAndClose(&protocol.ValidationError{Msg: "Received server data before the client hello was sent"})
		return
	}
	// The decoder is not safe for concurrent use, so it is driven under the
	// lock. It never calls back out, so nothing can re-enter here.
	messages, err := lifecycle.decoder.Push(chunk)
	c.mu.Unlock()
	if err != nil {
		c.failAndCloseIfCurrent(id, err)
		return
	}

	for _, message := range messages {
		// DIVERGENCE (deliberate): pi's loop guard only asks whether the
		// connection is disconnected, not whether it is still on this attempt.
		// A listener that replaces the connection mid-chunk — the pattern
		// ConnectionOptions documents — therefore leaves pi applying the rest of
		// a dead transport's chunk to the new one. Go's blocking Connect makes
		// that strictly worse: by the time the loop resumes the replacement has
		// finished its handshake, so the leftovers are accepted as its own
		// traffic rather than rejected as a missing hello. These bytes belong to
		// the attempt that is over, so the attempt id decides, as it does on the
		// way in.
		if !c.isCurrent(id) {
			return
		}
		c.handleMessage(id, message)
	}
}

// handleMessage applies one decoded server message to attempt id.
//
// Every tear-down here is conditional on the attempt: the lock is dropped
// before failing, so between the decision and the failure another goroutine —
// or a re-entrant listener — may already have replaced the attempt, and a
// stale message must not take the replacement down with it. pi re-reads its
// single lifecycle field instead, which is the same guard in a language with
// one thread.
func (c *Connection) handleMessage(id uint64, message protocol.ServerMessage) {
	c.mu.Lock()
	lifecycle := c.lifecycle
	if lifecycle == nil || lifecycle.id != id {
		c.mu.Unlock()
		return
	}

	if lifecycle.state == Connecting {
		switch typed := message.(type) {
		case *protocol.ServerHelloError:
			c.mu.Unlock()
			protocolError := typed.Error
			c.failAndCloseIfCurrent(id, newServerError(&protocolError))
		case *protocol.ServerHello:
			if lifecycle.transport == nil {
				c.mu.Unlock()
				c.failAndCloseIfCurrent(id, &protocol.ValidationError{
					Msg: "Received server hello before the client hello was sent",
				})
				return
			}
			c.mu.Unlock()
			c.completeHandshake(lifecycle, typed)
		default:
			c.mu.Unlock()
			c.failAndCloseIfCurrent(id, &protocol.ValidationError{Msg: "Expected server hello as first message"})
		}
		return
	}

	c.mu.Unlock()
	switch message.(type) {
	case *protocol.ServerHello, *protocol.ServerHelloError:
		c.failAndCloseIfCurrent(id, &protocol.ValidationError{Msg: "Unexpected handshake message"})
		return
	}
	c.opts.OnMessage(message)
}

// completeHandshake promotes a connecting attempt to connected.
//
// Each callback is made with the lock released and the lifecycle re-checked
// afterwards, because pi's listeners are allowed to disconnect or reconnect
// from inside them. The identity comparison against `lifecycle` is pi's
// `this.#lifecycle !== connected` check: an attempt that was replaced in the
// meantime must not be resurrected.
func (c *Connection) completeHandshake(lifecycle *connectionLifecycle, hello *protocol.ServerHello) {
	c.mu.Lock()
	if c.lifecycle != lifecycle {
		// The attempt was replaced while this message was on its way here.
		// Reporting its handshake would revive a connection that is over and
		// hand its snapshot to whoever replaced it.
		c.mu.Unlock()
		return
	}
	lifecycle.state = Connected
	c.mu.Unlock()

	// pi wraps onHandshake in try/catch and fails the connection if it throws.
	// The Client's own handler cannot: State contains subscriber panics (see
	// State.call) before they reach here. ConnectionOptions is exported
	// though, so a handler this package did not write can still panic, and pi's
	// answer to that is to fail the connection rather than unwind through the
	// transport's reading goroutine.
	snapshot := hello.Snapshot
	if err := c.callHandshake(&snapshot); err != nil {
		if c.isLifecycle(lifecycle) {
			c.failAndClose(err)
		}
		return
	}
	if !c.isLifecycle(lifecycle) {
		return
	}

	c.opts.OnStateChange(ConnectionStateChange{State: Connected})
	if !c.isLifecycle(lifecycle) {
		return
	}

	// Clear the handshake before settling it: once the attempt is reported as
	// connected, a later failure must not try to settle it a second time.
	c.mu.Lock()
	hs := lifecycle.handshake
	lifecycle.handshake = nil
	c.mu.Unlock()
	if hs != nil {
		hs.settle(&snapshot, nil)
	}
}

// callHandshake runs the handshake callback and turns a panic into the error
// pi's catch clause would have produced.
func (c *Connection) callHandshake(snapshot *protocol.ServerSnapshot) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			panicked, ok := recovered.(error)
			if !ok {
				panicked = fmt.Errorf("%v", recovered)
			}
			err = panicked
		}
	}()
	c.opts.OnHandshake(snapshot)
	return nil
}

// isLifecycle reports whether the given attempt is still the current one.
func (c *Connection) isLifecycle(lifecycle *connectionLifecycle) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lifecycle == lifecycle
}

// handleClose reports the transport's orderly end. A stream that stopped
// mid-frame is a protocol error, not a clean close, so the decoder gets the
// last word.
func (c *Connection) handleClose(id uint64) {
	c.mu.Lock()
	lifecycle := c.lifecycle
	if lifecycle == nil || lifecycle.id != id {
		c.mu.Unlock()
		return
	}
	var err error = &DisconnectedError{Message: "Byte transport closed"}
	if decoderErr := lifecycle.decoder.End(); decoderErr != nil {
		err = decoderErr
	}
	c.mu.Unlock()
	// Conditional on the attempt for the same reason handleMessage's failures
	// are: the lock is dropped first, so the attempt this close belongs to may
	// already have been replaced.
	c.failIfCurrent(id, err)
}

func (c *Connection) handleError(id uint64, err error) {
	c.failAndCloseIfCurrent(id, asDisconnectedError(err))
}

// failAndClose moves to Disconnected and then closes whatever transport the
// dying attempt owned. pi closes after notifying, so a listener that starts a
// new attempt does so before the old transport's close callback can fire — and
// that callback is then ignored by the attempt-id guard.
func (c *Connection) failAndClose(err error) {
	c.mu.Lock()
	var transport Transport
	if c.lifecycle != nil {
		transport = c.lifecycle.transport
	}
	c.mu.Unlock()

	c.fail(err)
	if transport != nil {
		// The close error is dropped on purpose: the connection has already
		// failed for a reason the caller has been told, and pi's
		// transport?.close() returns nothing at all. Reporting a second,
		// later failure would only replace the one that explains what happened.
		_ = transport.Close()
	}
}

func (c *Connection) failAndCloseIfCurrent(id uint64, err error) {
	if !c.isCurrent(id) {
		return
	}
	c.failAndClose(err)
}

func (c *Connection) failIfCurrent(id uint64, err error) {
	if !c.isCurrent(id) {
		return
	}
	c.fail(err)
}

// failTransport tears the connection down only if it is still running on the
// given transport.
func (c *Connection) failTransport(transport Transport, err error) {
	c.mu.Lock()
	current := c.lifecycle != nil && c.lifecycle.transport == transport
	c.mu.Unlock()
	if !current {
		return
	}
	c.failAndClose(err)
}

// fail moves to Disconnected, settles any unsettled handshake, and notifies.
func (c *Connection) fail(err error) {
	if notify := c.failDeferred(err); notify != nil {
		notify()
	}
}

// failDeferred tears the connection down and hands back the notification
// instead of delivering it, or nil when there was nothing live to tear down.
// Once it returns, State() already reports Disconnected and the handshake is
// already settled — only the callback is outstanding.
//
// It exists for Client.Close, which must finish disposing itself BEFORE any
// connection-state listener runs: a listener is allowed to close the client,
// and that nested Close has to find a client that is already fully torn down
// rather than one still mid-teardown. Splitting the notification out is what
// lets disposal publish completion first and notify second. Everything else
// goes through fail, which delivers immediately, exactly as pi's #fail does.
func (c *Connection) failDeferred(err error) func() {
	c.mu.Lock()
	lifecycle := c.lifecycle
	if lifecycle == nil {
		c.mu.Unlock()
		return nil
	}
	c.lifecycle = nil
	hs := lifecycle.handshake
	c.mu.Unlock()

	if hs != nil {
		hs.settle(nil, err)
	}
	change := ConnectionStateChange{State: Disconnected, Err: err}
	return func() { c.opts.OnStateChange(change) }
}

// DisconnectDeferred is Disconnect with the state-change notification handed
// back to the caller rather than delivered. See failDeferred; Client.Close is
// the only caller.
func (c *Connection) DisconnectDeferred(reason error) func() {
	if reason == nil {
		reason = &DisconnectedError{Message: defaultConnectionDisconnectReason}
	}
	c.mu.Lock()
	var transport Transport
	if c.lifecycle != nil {
		transport = c.lifecycle.transport
	}
	c.mu.Unlock()

	notify := c.failDeferred(reason)
	if transport != nil {
		// Dropped for the same reason failAndClose drops it.
		_ = transport.Close()
	}
	return notify
}

func (c *Connection) isCurrent(id uint64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lifecycle != nil && c.lifecycle.id == id
}
