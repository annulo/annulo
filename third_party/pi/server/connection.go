package server

import (
	"sync"
	"time"

	"github.com/sky-valley/pi/protocol"
)

// ByteConn is an established, authorized ordered byte connection handed to the
// Server by a transport.
//
// Send must be safe to call from several goroutines and must preserve
// invocation order: two chunks sent one after another arrive in that order. It
// should enqueue rather than block on the peer — the Server calls it from the
// goroutine that produced the event, and a transport that blocks there stalls
// the session that produced it.
type ByteConn interface {
	// Closed reports whether the connection has finished closing.
	Closed() bool
	// Send queues one chunk. A returned error is terminal for the connection.
	Send(chunk []byte) error
	// Close shuts the connection down, writing finalChunk first when it is not
	// nil. Repeated calls must be harmless.
	Close(finalChunk []byte) error
}

// ConnHandler receives everything a transport observes on one connection.
//
// DIVERGENCE (deliberate): pi declares this as an interface a transport
// implements. A struct of funcs matches client.TransportHandlers and lets a
// transport close over its socket without declaring a type.
type ConnHandler struct {
	// OnData delivers an inbound chunk. Calls must be serialised: the Server
	// drives a stateful frame decoder from them.
	OnData func(chunk []byte)
	// OnClose reports an orderly terminal close.
	OnClose func()
	// OnError reports a terminal transport failure.
	OnError func(err error)
}

// Acceptor is what a Listener calls for each accepted connection.
type Acceptor func(conn ByteConn) ConnHandler

// connStage is where a connection sits in its handshake lifecycle.
type connStage int

const (
	stageAwaitingHello connStage = iota
	// stageHandshaking is the window pi opens synchronously when it accepts a
	// hello and closes when the handshake resolves. Everything a peer sends in
	// the meantime is judged against this stage: a second hello ends the
	// connection there and then, and a request waits.
	stageHandshaking
	stageReady
	stageClosing
	stageClosed
)

// connState is one accepted connection.
type connState struct {
	id      string
	conn    ByteConn
	decoder *protocol.ClientMessageDecoder

	// mu guards everything below. The decoder is not guarded: it is touched
	// only from the read goroutine, which is exactly the contract it declares.
	mu                sync.Mutex
	sessionIDs        map[string]struct{}
	stage             connStage
	disconnected      bool
	handshakeComplete bool
	// handshakeTimer is nil until the connection has been adopted, and stays
	// nil for one the server refused. Stop it through stopHandshakeTimerLocked.
	handshakeTimer *time.Timer
}

// stopHandshakeTimerLocked stops the handshake timer when there is one. A
// connection the server declined to adopt never had one armed.
func (c *connState) stopHandshakeTimerLocked() {
	if c.handshakeTimer != nil {
		c.handshakeTimer.Stop()
	}
}

// currentStage reports where the connection sits right now.
func (c *connState) currentStage() connStage {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stage
}

// terminal reports whether the connection can no longer process messages.
func (c *connState) terminal() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.terminalLocked()
}

func (c *connState) terminalLocked() bool {
	return c.disconnected || c.stage == stageClosing || c.stage == stageClosed
}

// ready reports whether the connection completed its handshake and is still
// usable.
func (c *connState) ready() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stage == stageReady && !c.disconnected
}

// attached reports whether this connection holds sessionID.
func (c *connState) attached(sessionID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.sessionIDs[sessionID]
	return ok
}
