package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"runtime"
	"sync"

	"github.com/sky-valley/pi/protocol"
)

// unixReadBufferSize is the size of one read from the socket. It is a chunking
// choice only: the frame decoder reassembles whatever arrives.
const unixReadBufferSize = 64 * 1024

// maxUnixSocketPathBytes is the kernel's sun_path limit, which differs per
// platform. pi computes the same two numbers from process.platform.
func maxUnixSocketPathBytes() int {
	if runtime.GOOS == "linux" {
		return 107
	}
	return 103
}

// UnixTransportOptions configures a Unix-domain socket transport factory.
type UnixTransportOptions struct {
	// Path is the socket to connect to.
	Path string
	// MaxPendingBytes bounds the bytes queued for writing but not yet written.
	// A nil pointer takes four times protocol.DefaultMaxFrameLength; pi spells
	// this `??`, so a zero must stay distinguishable from "unset" and is
	// rejected.
	MaxPendingBytes *int
}

// NewUnixTransportFactory validates its options and returns a factory that
// creates a fresh Unix-domain socket transport per connection attempt. It is
// the port of pi's createUnixTransportFactory (packages/client/src/unix.ts).
//
// The options are validated once, here, rather than on each connection attempt,
// so a misconfigured path fails at construction instead of at reconnect time.
func NewUnixTransportFactory(opts UnixTransportOptions) (TransportFactory, error) {
	if opts.Path == "" {
		return nil, errors.New("Unix transport path must not be empty")
	}
	// len is UTF-8 bytes in Go, which is what the kernel counts and what pi
	// measures with Buffer.byteLength.
	if len(opts.Path) > maxUnixSocketPathBytes() {
		return nil, fmt.Errorf("Unix transport path is too long; maximum is %d UTF-8 bytes", maxUnixSocketPathBytes())
	}
	maxPendingBytes := protocol.DefaultMaxFrameLength * 4
	if opts.MaxPendingBytes != nil {
		maxPendingBytes = *opts.MaxPendingBytes
	}
	if maxPendingBytes <= 0 {
		return nil, errors.New("Unix transport maxPendingBytes must be a positive safe integer")
	}
	if runtime.GOOS == "windows" {
		return nil, errors.New("Unix transport is not supported on Windows")
	}
	path := opts.Path
	return func(ctx context.Context, handlers TransportHandlers) (Transport, error) {
		return dialUnix(ctx, path, maxPendingBytes, handlers)
	}, nil
}

// dialUnix connects and starts the transport's two goroutines.
//
// The dial is bounded by ctx: a socket whose accept backlog is full leaves
// connect(2) blocked indefinitely, and a caller that gave up on the attempt
// must not be held by it. ctx stops mattering the moment the connection is
// established, which is the TransportFactory contract.
//
// DIVERGENCE (deliberate): pi's dial has no timeout or cancellation at all.
func dialUnix(ctx context.Context, path string, maxPendingBytes int, handlers TransportHandlers) (Transport, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		// pi rejects the factory promise with the socket error; the caller
		// turns it into a DisconnectedError.
		return nil, err
	}
	t := &unixTransport{conn: conn, maxPendingBytes: maxPendingBytes, handlers: handlers}
	t.cond = sync.NewCond(&t.mu)
	go t.readLoop()
	go t.writeLoop()
	return t, nil
}

// unixTransport carries frames over one connected Unix-domain socket.
//
// DIVERGENCE (deliberate): pi's send() returns a promise that settles when the
// write reaches the kernel, and Connection turns a rejected one into a
// connection failure. Send here returns as soon as the chunk is queued, and a
// write failure is reported through OnError — which is the same terminal path
// pi's socket "error" event takes for the very same failure, so the observable
// outcome is identical without a promise per write.
//
// A dedicated writer goroutine, rather than writing on the caller's goroutine,
// is what preserves the Transport contract that chunks are delivered in
// invocation order, and is what gives the pending-byte bound something to
// bound.
type unixTransport struct {
	conn            net.Conn
	maxPendingBytes int
	handlers        TransportHandlers

	mu   sync.Mutex
	cond *sync.Cond
	// queue holds copies of the chunks still to be written, oldest first.
	queue   [][]byte
	pending int
	closed  bool
	// terminal records that the transport is finished, either because it was
	// closed locally or because exactly one terminal handler has been called.
	// pi keeps the same flag to guarantee "exactly one terminal handler".
	terminal bool
}

// Send queues one chunk for writing.
//
// The chunk is copied, so the caller may reuse its buffer immediately. Queuing
// more than the configured pending-byte budget fails rather than growing
// without bound: a peer that stops reading must not be able to exhaust this
// process's memory.
func (t *unixTransport) Send(chunk []byte) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed || t.terminal {
		return errors.New("Unix transport is closed")
	}
	if t.pending+len(chunk) > t.maxPendingBytes {
		return errors.New("Unix transport exceeded its pending byte limit")
	}
	t.pending += len(chunk)
	t.queue = append(t.queue, append([]byte(nil), chunk...))
	t.cond.Signal()
	return nil
}

// Close shuts the socket down. Repeated calls are harmless, and no terminal
// handler fires for a close this side asked for.
func (t *unixTransport) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	t.terminal = true
	t.cond.Broadcast()
	t.mu.Unlock()
	return t.conn.Close()
}

// readLoop delivers inbound bytes until the socket ends.
//
// The chunk handed to OnData aliases the read buffer and is only valid for the
// duration of the call. Every consumer in this package copies what it keeps.
func (t *unixTransport) readLoop() {
	buffer := make([]byte, unixReadBufferSize)
	for {
		n, err := t.conn.Read(buffer)
		if n > 0 && !t.isTerminal() {
			t.handlers.OnData(buffer[:n])
		}
		if err == nil {
			continue
		}
		// A local Close already claimed terminal, so a read error caused by
		// our own shutdown reports nothing.
		if !t.claimTerminal() {
			return
		}
		// The read error below is the one that explains this transport's end;
		// a close error on a socket that has already failed adds nothing and
		// would displace it.
		_ = t.conn.Close()
		if errors.Is(err, io.EOF) {
			t.handlers.OnClose()
		} else {
			t.handlers.OnError(err)
		}
		return
	}
}

// writeLoop drains the queue in order.
func (t *unixTransport) writeLoop() {
	for {
		t.mu.Lock()
		for len(t.queue) == 0 && !t.closed && !t.terminal {
			t.cond.Wait()
		}
		if t.closed || t.terminal {
			t.mu.Unlock()
			return
		}
		chunk := t.queue[0]
		t.queue = t.queue[1:]
		t.mu.Unlock()

		_, err := t.conn.Write(chunk)

		t.mu.Lock()
		t.pending -= len(chunk)
		t.mu.Unlock()

		if err == nil {
			continue
		}
		if !t.claimTerminal() {
			return
		}
		// Same as in readLoop: the write error is what OnError reports, and a
		// close error on an already-broken socket would only hide it.
		_ = t.conn.Close()
		t.handlers.OnError(err)
		return
	}
}

// claimTerminal reports whether this caller is the one that gets to deliver the
// single terminal handler call.
func (t *unixTransport) claimTerminal() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.terminal {
		return false
	}
	t.terminal = true
	t.cond.Broadcast()
	return true
}

func (t *unixTransport) isTerminal() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.terminal
}
