package server

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
	"time"

	"github.com/sky-valley/pi/protocol"
)

// DefaultHandshakeTimeout is how long a connection has to complete its
// handshake before the server closes it.
const DefaultHandshakeTimeout = 5 * time.Second

// maxHandshakeTimeout is Node's maximum timer delay.
//
// This bound is a Node artifact — Go timers have no such limit — but it is kept
// so a configuration a Node pi server rejects is rejected here too. A
// deployment that shares its configuration between the two implementations
// should get the same answer from both.
const maxHandshakeTimeout = 2147483647 * time.Millisecond

// maxUint32 is uint64 because it does not fit in an int on a 32-bit build.
const maxUint32 uint64 = 0xffff_ffff

// Options configures a Server.
type Options struct {
	// Listeners are the transports to serve on. It must not be nil; an empty
	// non-nil slice is a server with no transport, which is useful in tests.
	Listeners []Listener
	// MaxFrameLength bounds one encoded protocol message. nil takes
	// protocol.DefaultMaxFrameLength.
	MaxFrameLength *int
	// HandshakeTimeout bounds the handshake. Zero takes
	// DefaultHandshakeTimeout.
	HandshakeTimeout time.Duration
	// ServerID is the stable identity this server reports in its snapshots.
	//
	// DIVERGENCE (deliberate): pi distinguishes an absent serverId (generate
	// one) from an empty string (a TypeError). Go's zero value cannot carry
	// that distinction, so empty means "generate one" and there is nothing left
	// to reject.
	ServerID string
	// OnError observes failures that have nowhere else to go: service errors
	// hidden from clients, transport failures, dispose failures. It may be nil,
	// may be called from any goroutine, and its own panics are swallowed.
	OnError func(error)
}

// Server serves pi's session protocol over any set of Listeners.
//
// DIVERGENCE (deliberate): pi wires its three collaborators together with
// option bags full of closures back into PiServer. Here the session manager and
// the snapshot publisher hold a *Server directly. The indirection bought pi
// nothing that a Go package boundary does not already give, and the closures
// obscured the lock order these three now share: a caller never holds the
// Server's mutex while calling into the session manager.
type Server struct {
	id               string
	listeners        []Listener
	maxFrameLength   int
	handshakeTimeout time.Duration
	onError          func(error)

	// ctx bounds service and runtime calls. It is cancelled once Close has
	// finished, so a service can use it to retire whatever it started; it is
	// deliberately still live while sessions are being disposed.
	ctx    context.Context
	cancel context.CancelFunc

	sessions  *sessionManager
	snapshots *snapshotPublisher

	mu          sync.Mutex
	connections map[*connState]struct{}
	closing     bool
	started     bool
	startDone   chan struct{}
	closeDone   chan struct{}
	closeErr    error
}

// New validates options and builds a Server. It does not bind anything; call
// Start for that.
func New(service Service, options Options) (*Server, error) {
	if service == nil {
		return nil, errors.New("server service must not be nil")
	}
	if options.Listeners == nil {
		return nil, errors.New("server listeners must not be nil; pass an empty slice for a server with no transport")
	}
	maxFrameLength := protocol.DefaultMaxFrameLength
	if options.MaxFrameLength != nil {
		maxFrameLength = *options.MaxFrameLength
	}
	if maxFrameLength <= 0 || uint64(maxFrameLength) > maxUint32 {
		return nil, fmt.Errorf("server maxFrameLength must be an integer between 1 and %d", maxUint32)
	}
	handshakeTimeout := options.HandshakeTimeout
	if handshakeTimeout == 0 {
		handshakeTimeout = DefaultHandshakeTimeout
	}
	// Node's timer takes whole milliseconds, so a duration it cannot express is
	// rejected here too rather than being silently rounded into a different
	// configuration than a Node pi server would run.
	if handshakeTimeout < time.Millisecond ||
		handshakeTimeout > maxHandshakeTimeout ||
		handshakeTimeout%time.Millisecond != 0 {
		return nil, fmt.Errorf(
			"server handshakeTimeout must be a whole number of milliseconds between 1ms and %s, or zero for the %s default",
			maxHandshakeTimeout, DefaultHandshakeTimeout)
	}

	id := options.ServerID
	if id == "" {
		id = newUUID()
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{
		id:               id,
		listeners:        options.Listeners,
		maxFrameLength:   maxFrameLength,
		handshakeTimeout: handshakeTimeout,
		onError:          options.OnError,
		ctx:              ctx,
		cancel:           cancel,
		connections:      map[*connState]struct{}{},
	}
	s.sessions = newSessionManager(s, service)
	s.snapshots = newSnapshotPublisher(s, service)
	return s, nil
}

// ID is the server's stable identity, as reported in its snapshots.
func (s *Server) ID() string { return s.id }

// Addresses lists the bound address of every listener that has one.
func (s *Server) Addresses() []string {
	addresses := make([]string, 0, len(s.listeners))
	for _, listener := range s.listeners {
		if address := listener.Address(); address != "" {
			addresses = append(addresses, address)
		}
	}
	return addresses
}

// Start binds every listener. A listener that fails to start closes the ones
// already started, so a failed Start leaves nothing bound.
func (s *Server) Start(ctx context.Context) error {
	s.mu.Lock()
	switch {
	case s.started:
		s.mu.Unlock()
		return errors.New("server is already started")
	case s.startDone != nil:
		s.mu.Unlock()
		return errors.New("server is already starting")
	case s.closing:
		s.mu.Unlock()
		return errors.New("server is closing or closed")
	}
	done := make(chan struct{})
	s.startDone = done
	s.mu.Unlock()

	err := s.startListeners(ctx)

	s.mu.Lock()
	s.startDone = nil
	if err == nil {
		s.started = true
	}
	s.mu.Unlock()
	close(done)
	return err
}

func (s *Server) startListeners(ctx context.Context) error {
	started := make([]Listener, 0, len(s.listeners))
	for _, listener := range s.listeners {
		if err := listener.Start(ctx, s.Accept); err != nil {
			s.mu.Lock()
			s.closing = true
			s.mu.Unlock()
			for _, opened := range started {
				if closeErr := opened.Close(ctx); closeErr != nil {
					s.reportError(closeErr)
				}
			}
			s.closeServerState(ctx)
			return err
		}
		started = append(started, listener)
	}
	return nil
}

// Accept adopts one transport connection and returns the handler the transport
// must drive. It is the Acceptor a Listener is given, and may also be called
// directly to serve a connection the caller already has.
func (s *Server) Accept(conn ByteConn) ConnHandler {
	maxFrameLength := s.maxFrameLength
	decoder, err := protocol.NewClientMessageDecoder(&protocol.FrameOptions{MaxFrameLength: &maxFrameLength})
	if err != nil {
		// Unreachable: New already validated maxFrameLength against the same
		// bound the decoder enforces.
		s.reportError(err)
		s.closeConnection(conn, nil)
		return ConnHandler{OnData: func([]byte) {}, OnClose: func() {}, OnError: func(error) {}}
	}

	state := &connState{
		id:         newUUID(),
		conn:       conn,
		decoder:    decoder,
		sessionIDs: map[string]struct{}{},
		stage:      stageAwaitingHello,
	}

	// Deciding whether the server is still open and adopting the connection is
	// one step. Split in two, a connection arriving between them is adopted by
	// a server that has already collected the connections it is about to close
	// — and then nothing ever closes it: the socket, its goroutines and its
	// running handshake timer outlive the server that owns them.
	s.mu.Lock()
	adopted := !s.closing
	if adopted {
		s.connections[state] = struct{}{}
	}
	s.mu.Unlock()
	if !adopted {
		s.closeConnection(conn, nil)
		return ConnHandler{
			OnData:  func([]byte) {},
			OnClose: func() {},
			OnError: func(err error) { s.reportError(err) },
		}
	}

	// The timer is armed only once the connection has been adopted — a
	// connection the server refused has nothing to time — and under the
	// connection's own lock, so the callback cannot observe the field before it
	// has been assigned.
	state.mu.Lock()
	state.handshakeTimer = time.AfterFunc(s.handshakeTimeout, func() {
		s.failProtocol(state, &protocol.ProtocolError{
			Code:    protocol.ErrorInvalidRequest,
			Message: "Handshake timeout",
		})
	})
	state.mu.Unlock()

	return ConnHandler{
		OnData:  func(chunk []byte) { s.receive(state, chunk) },
		OnClose: func() { s.transportClosed(state) },
		OnError: func(err error) {
			s.reportError(err)
			s.closeConnection(conn, nil)
			s.disconnect(s.opContext(), state)
		},
	}
}

// Close stops every listener, closes every connection, and disposes every live
// session. It is idempotent: concurrent and repeated calls all wait for the
// first one and return its result.
//
// It is bounded by ctx. A Service that never answers must not be able to wedge
// a caller that asked for a bounded shutdown, so every wait gives up when ctx
// does and Close returns ctx.Err(). An abandoned shutdown is not resumed: the
// server stays closing and accepts nothing new, and ctx is cancelled so a
// service bounded by it can unwind — but whatever had not been released by then
// is not released later, and a later Close reports the same failure.
func (s *Server) Close(ctx context.Context) error {
	s.mu.Lock()
	if s.closeDone != nil {
		done := s.closeDone
		s.mu.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.closeErr
	}
	s.closing = true
	done := make(chan struct{})
	s.closeDone = done
	starting := s.startDone
	s.mu.Unlock()

	err := s.closeEverything(ctx, starting)

	s.mu.Lock()
	s.started = false
	s.closeErr = err
	s.mu.Unlock()
	close(done)
	s.cancel()
	return err
}

func (s *Server) closeEverything(ctx context.Context, starting chan struct{}) error {
	if starting != nil {
		select {
		case <-starting:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	var firstErr error
	for _, listener := range s.listeners {
		if err := listener.Close(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if err := s.closeServerState(ctx); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// receive decodes one inbound chunk and acts on every message in it.
//
// DIVERGENCE (deliberate): pi runs the handshake as a promise and chains any
// message that arrives during it onto that promise. Here every message in the
// chunk is judged first and the handshake is completed afterwards, on the same
// goroutine. That is the same ordering without the promise — a second hello
// still ends the connection before the first one's answer goes out, and a
// request that arrives during the handshake still waits for it — and a request
// on a ready connection still gets a goroutine each, so a slow command cannot
// hold up a fast one behind it.
func (s *Server) receive(state *connState, chunk []byte) {
	if state.terminal() {
		return
	}
	messages, err := state.decoder.Push(chunk)
	if err != nil {
		s.failProtocol(state, s.toProtocolError(err))
		return
	}

	var handshaking bool
	var deferred []*protocol.RequestEnvelope
	for _, message := range messages {
		if state.terminal() {
			return
		}
		switch msg := message.(type) {
		case *protocol.ClientHello:
			if !s.beginHandshake(state, msg) {
				return
			}
			handshaking = true

		case *protocol.RequestEnvelope:
			switch state.currentStage() {
			case stageAwaitingHello:
				s.failProtocol(state, &protocol.ProtocolError{
					Code:    protocol.ErrorInvalidRequest,
					Message: "The first client message must be hello",
				})
				return
			case stageHandshaking:
				deferred = append(deferred, msg)
			case stageReady:
				go s.handleRequest(state, msg)
			}
		}
	}

	if handshaking && !s.finishHandshake(state) {
		return
	}
	for _, request := range deferred {
		if !state.ready() {
			return
		}
		go s.handleRequest(state, request)
	}
}

// beginHandshake opens the handshake and runs everything pi runs synchronously
// before it awaits the server snapshot. It reports whether the connection is
// still worth answering.
func (s *Server) beginHandshake(state *connState, hello *protocol.ClientHello) bool {
	state.mu.Lock()
	first := state.stage == stageAwaitingHello
	if first {
		state.stage = stageHandshaking
	}
	state.mu.Unlock()
	if !first {
		s.failProtocol(state, &protocol.ProtocolError{
			Code:    protocol.ErrorInvalidRequest,
			Message: "hello may only be sent as the first message",
		})
		return false
	}

	if !protocol.IsSupportedVersion(hello.Version) {
		s.failProtocol(state, &protocol.ProtocolError{
			Code: protocol.ErrorVersion,
			Message: fmt.Sprintf("Unsupported protocol version %d; expected %d",
				hello.Version, protocol.ProtocolVersion),
		})
		return false
	}
	return true
}

// finishHandshake builds the snapshot the hello carries, sends it and promotes
// the connection. It reports whether the connection reached ready.
func (s *Server) finishHandshake(state *connState) bool {
	ctx := s.opContext()
	snapshot, err := s.serverSnapshot(ctx)
	if err != nil {
		s.failProtocol(state, s.toProtocolError(err))
		return false
	}

	// The stage is re-read after the snapshot, because anything the peer sent
	// while it was being built has already been judged against the handshaking
	// stage — a second hello among it has ended the connection, and answering
	// it now would put a hello on the wire behind the hello_error that
	// replaced it.
	state.mu.Lock()
	abandon := state.disconnected || state.stage != stageHandshaking
	state.mu.Unlock()
	if s.isClosing() || abandon || state.conn.Closed() {
		return false
	}

	sent := s.sendMessage(state, &protocol.ServerHello{
		Type:         "hello",
		Version:      protocol.ProtocolVersion,
		ConnectionID: state.id,
		Snapshot:     *snapshot,
	})
	if !sent {
		return false
	}

	state.mu.Lock()
	promoted := !state.disconnected && state.stage == stageHandshaking
	if promoted {
		state.handshakeComplete = true
		state.stage = stageReady
		state.stopHandshakeTimerLocked()
	}
	state.mu.Unlock()
	if !promoted {
		return false
	}

	// A change that landed while the snapshot was being built would otherwise
	// be invisible to this client until the next broadcast, because the
	// broadcast that carried it went out before the connection was ready.
	if snapshot.Revision == s.snapshots.currentRevision() {
		return true
	}
	current, err := s.serverSnapshot(ctx)
	if err != nil {
		s.failProtocol(state, s.toProtocolError(err))
		return false
	}
	s.sendMessage(state, &protocol.EventEnvelope{
		Type:  "event",
		Event: &protocol.ServerSnapshotEvent{Type: "server_snapshot", Snapshot: *current},
	})
	return true
}

func (s *Server) handleRequest(state *connState, envelope *protocol.RequestEnvelope) {
	result, err := s.executeCommand(state, envelope)
	if err != nil {
		s.sendMessage(state, &protocol.ResponseEnvelope{
			Type:  "response",
			ID:    envelope.ID,
			OK:    false,
			Error: s.toProtocolError(err),
		})
		return
	}
	s.sendMessage(state, &protocol.ResponseEnvelope{
		Type:   "response",
		ID:     envelope.ID,
		OK:     true,
		Result: result,
	})
}

// executeCommand runs one command behind a panic barrier. A panicking Service
// or SessionRuntime fails the request it was called from; the error it produces
// is not an *Error, so the peer is told nothing beyond "internal server error".
func (s *Server) executeCommand(
	state *connState,
	envelope *protocol.RequestEnvelope,
) (result protocol.CommandResult, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			result = nil
			err = panicError(recovered, "executing the "+envelope.Request.CommandName()+" command")
		}
	}()
	return s.sessions.executeCommand(s.opContext(), state, envelope.Request)
}

// serverSnapshot builds the server snapshot behind the same barrier: the
// handshake reads the service from the connection's read goroutine, where a
// panic would take the process down rather than the connection.
func (s *Server) serverSnapshot(ctx context.Context) (snapshot *protocol.ServerSnapshot, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			snapshot = nil
			err = panicError(recovered, "building the server snapshot")
		}
	}()
	return s.snapshots.get(ctx, nil)
}

// panicError turns a panic that crossed an extension-point boundary into an
// error. Service and SessionRuntime are implemented outside this package, and
// they are called from goroutines their implementer never sees; a panic there
// must fail the work in hand, not the process.
func panicError(recovered any, what string) error {
	return fmt.Errorf(
		"panic while %s; a Service or SessionRuntime must report failures as errors rather than panic: %v\n%s",
		what, recovered, debug.Stack())
}

func (s *Server) transportClosed(state *connState) {
	state.mu.Lock()
	drain := !state.disconnected && state.stage != stageClosing
	state.mu.Unlock()
	if drain {
		// End reports a stream that stopped mid-frame. The connection is going
		// away either way, so this only records the truncation.
		if err := state.decoder.End(); err != nil {
			s.reportError(err)
		}
	}
	s.disconnect(s.opContext(), state)
}

func (s *Server) disconnect(ctx context.Context, state *connState) {
	state.mu.Lock()
	if state.disconnected {
		state.mu.Unlock()
		return
	}
	handshakeComplete := state.handshakeComplete
	state.disconnected = true
	state.stage = stageClosed
	state.stopHandshakeTimerLocked()
	state.mu.Unlock()

	s.mu.Lock()
	delete(s.connections, state)
	s.mu.Unlock()

	s.sessions.disconnect(ctx, state)
	if !s.isClosing() && handshakeComplete {
		s.snapshots.broadcast()
	}
}

// sendMessage encodes and queues one server message, reporting whether it was
// accepted. A message that cannot be encoded or queued is terminal for the
// connection: the peer's view of the stream is already incomplete.
func (s *Server) sendMessage(state *connState, message protocol.ServerMessage) bool {
	state.mu.Lock()
	disconnected := state.disconnected
	state.mu.Unlock()
	if disconnected || state.conn.Closed() {
		return false
	}

	maxFrameLength := s.maxFrameLength
	frame, err := protocol.EncodeServerMessage(message, &protocol.FrameOptions{MaxFrameLength: &maxFrameLength})
	if err == nil {
		err = state.conn.Send(frame)
	}
	if err != nil {
		s.reportError(err)
		s.closeConnection(state.conn, nil)
		s.disconnect(s.opContext(), state)
		return false
	}
	return true
}

// failProtocol reports a protocol violation and closes the connection, writing
// the hello_error as the connection's final bytes so the peer learns why.
func (s *Server) failProtocol(state *connState, protocolError *protocol.ProtocolError) {
	state.mu.Lock()
	if state.terminalLocked() {
		state.mu.Unlock()
		return
	}
	state.stage = stageClosing
	state.stopHandshakeTimerLocked()
	state.mu.Unlock()

	maxFrameLength := s.maxFrameLength
	frame, err := protocol.EncodeServerMessage(
		&protocol.ServerHelloError{Type: "hello_error", Error: *protocolError},
		&protocol.FrameOptions{MaxFrameLength: &maxFrameLength},
	)
	if err != nil {
		s.reportError(err)
		frame = nil
	}
	s.closeConnection(state.conn, frame)
	s.disconnect(s.opContext(), state)
}

func (s *Server) closeServerState(ctx context.Context) error {
	s.mu.Lock()
	connections := make([]*connState, 0, len(s.connections))
	for state := range s.connections {
		connections = append(connections, state)
	}
	s.mu.Unlock()

	for _, state := range connections {
		state.mu.Lock()
		state.stage = stageClosing
		state.stopHandshakeTimerLocked()
		state.mu.Unlock()
	}
	for _, state := range connections {
		s.closeConnection(state.conn, nil)
	}
	for _, state := range connections {
		s.disconnect(ctx, state)
	}

	// Broadcasts are already no-ops once closing is set; waiting only makes
	// sure none is still touching the service when Close returns.
	if err := s.snapshots.wait(ctx); err != nil {
		return err
	}
	err := s.sessions.close(ctx)

	s.mu.Lock()
	clear(s.connections)
	s.mu.Unlock()
	return err
}

func (s *Server) closeConnection(conn ByteConn, finalChunk []byte) {
	if err := conn.Close(finalChunk); err != nil {
		s.reportError(err)
	}
}

// toProtocolError decides what a failure is allowed to tell the peer.
func (s *Server) toProtocolError(err error) *protocol.ProtocolError {
	// Checked before *Error, because an InternalError may wrap one: its whole
	// purpose is that whatever it holds stays off the wire.
	var internalError *InternalError
	if errors.As(err, &internalError) {
		s.reportError(internalError.Cause)
		return &protocol.ProtocolError{Code: protocol.ErrorInternal, Message: InternalErrorMessage}
	}
	var serverError *Error
	if errors.As(err, &serverError) && serverError.crossesProtocolBoundary() {
		// A missing operation says only that it is missing. The error's own
		// message and details are discarded rather than risk naming internals.
		if serverError.Code == protocol.ErrorNotImplemented {
			return &protocol.ProtocolError{
				Code:    protocol.ErrorNotImplemented,
				Message: NotImplementedMessage,
			}
		}
		protoErr := &protocol.ProtocolError{Code: serverError.Code, Message: serverError.Message}
		if serverError.Details != nil {
			details := serverError.Details
			protoErr.Details = &details
		}
		return protoErr
	}
	var validationError *protocol.ValidationError
	if errors.As(err, &validationError) {
		return &protocol.ProtocolError{Code: protocol.ErrorInvalidRequest, Message: validationError.Error()}
	}
	s.reportError(err)
	return &protocol.ProtocolError{Code: protocol.ErrorInternal, Message: InternalErrorMessage}
}

// reportError hands a failure to the error observer. An observer that panics
// cannot affect server state.
func (s *Server) reportError(err error) {
	if s.onError == nil || err == nil {
		return
	}
	defer func() { _ = recover() }()
	s.onError(err)
}

func (s *Server) isClosing() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closing
}

// opContext is the context service and runtime calls run under.
func (s *Server) opContext() context.Context { return s.ctx }

// readyConnections snapshots the connections a broadcast may reach.
//
// DIVERGENCE (deliberate): pi iterates a Set, so it visits connections in the
// order they were accepted; a Go map does not. The order is not observable —
// every connection in one pass carries the same revision, and the protocol
// makes no promise about how two different connections are interleaved — and
// keeping an insertion-ordered list would add a removal scan to every
// disconnect for nothing.
func (s *Server) readyConnections() []*connState {
	s.mu.Lock()
	states := make([]*connState, 0, len(s.connections))
	for state := range s.connections {
		states = append(states, state)
	}
	s.mu.Unlock()

	ready := states[:0]
	for _, state := range states {
		if state.ready() {
			ready = append(ready, state)
		}
	}
	return ready
}
