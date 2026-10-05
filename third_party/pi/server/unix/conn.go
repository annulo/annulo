//go:build unix

package unix

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

// errConnClosed is what a caller gets for writing to a connection that is
// closing or closed.
var errConnClosed = errors.New("Unix connection is closed")

// errPendingLimit is what a caller gets when a peer is too slow to keep up.
var errPendingLimit = errors.New("Unix connection exceeded its pending byte limit")

// conn is one accepted socket presented as a server.ByteConn.
//
// Writes go through a single goroutine, so chunks reach the peer in the order
// Send was called and no caller ever blocks on the socket. A peer that stops
// reading is bounded by maxPendingBytes rather than by memory.
type conn struct {
	socket               net.Conn
	gracefulCloseTimeout time.Duration
	maxPendingBytes      int
	// reportError is the listener's error observer.
	//
	// DIVERGENCE (deliberate): pi's send returns a promise, so a write that
	// fails rejects in the server's own sendMessage, which reports it. Send
	// here only queues — the write happens later on the write goroutine, and
	// there is no caller left to hand the failure to — so it goes straight to
	// the observer instead of being swallowed.
	reportError func(error)

	notify   chan struct{}
	closedCh chan struct{}

	mu           sync.Mutex
	queue        [][]byte
	pendingBytes int
	closing      bool
	closed       bool
	finalChunk   []byte
	closeTimer   *time.Timer
	writeFailed  bool
}

func newConn(
	socket net.Conn,
	gracefulCloseTimeout time.Duration,
	maxPendingBytes int,
	reportError func(error),
) *conn {
	c := &conn{
		socket:               socket,
		gracefulCloseTimeout: gracefulCloseTimeout,
		maxPendingBytes:      maxPendingBytes,
		reportError:          reportError,
		notify:               make(chan struct{}, 1),
		closedCh:             make(chan struct{}),
	}
	go c.writeLoop()
	return c
}

// Closed reports whether the socket has finished closing. A connection that is
// draining its final bytes is not yet closed, which is what stops the server
// from queuing more onto it.
func (c *conn) Closed() bool {
	select {
	case <-c.closedCh:
		return true
	default:
		return false
	}
}

func (c *conn) Send(chunk []byte) error {
	c.mu.Lock()
	if c.closed || c.closing {
		c.mu.Unlock()
		return errConnClosed
	}
	if c.pendingBytes+len(chunk) > c.maxPendingBytes {
		c.mu.Unlock()
		return errPendingLimit
	}
	c.pendingBytes += len(chunk)
	c.queue = append(c.queue, bytes.Clone(chunk))
	c.mu.Unlock()
	c.wake()
	return nil
}

// Close flushes whatever is queued, writes finalChunk last, and shuts the
// socket down.
//
// DIVERGENCE (deliberate): pi's close resolves only once the socket has fully
// closed. Waiting for that here would deadlock: the server closes a connection
// from the goroutine that is reading it, and that goroutine is the one that
// observes the close. So Close returns once the shutdown is committed — the
// final chunk is still written before the socket goes away, and the graceful
// timeout still tears down a peer that will not finish.
func (c *conn) Close(finalChunk []byte) error {
	c.mu.Lock()
	if c.closed || c.closing {
		c.mu.Unlock()
		return nil
	}
	c.closing = true
	c.finalChunk = bytes.Clone(finalChunk)
	c.closeTimer = time.AfterFunc(c.gracefulCloseTimeout, func() { _ = c.socket.Close() })
	c.mu.Unlock()
	c.wake()
	return nil
}

// markClosed records that the socket is gone. The listener's read loop calls it
// when the socket ends, whichever side ended it.
func (c *conn) markClosed() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.closing = true
	timer := c.closeTimer
	c.closeTimer = nil
	c.mu.Unlock()
	if timer != nil {
		timer.Stop()
	}
	close(c.closedCh)
	c.wake()
}

func (c *conn) wake() {
	select {
	case c.notify <- struct{}{}:
	default:
	}
}

func (c *conn) writeLoop() {
	for {
		c.mu.Lock()
		switch {
		case c.closed:
			c.mu.Unlock()
			return
		case len(c.queue) > 0:
			chunk := c.queue[0]
			c.queue = c.queue[1:]
			c.mu.Unlock()
			if _, err := c.socket.Write(chunk); err != nil {
				c.recordWriteError(err)
				return
			}
			c.mu.Lock()
			c.pendingBytes -= len(chunk)
			c.mu.Unlock()
			continue
		case c.closing:
			final := c.finalChunk
			c.finalChunk = nil
			c.mu.Unlock()
			if len(final) > 0 {
				if _, err := c.socket.Write(final); err != nil {
					c.recordWriteError(err)
					return
				}
			}
			c.halfClose()
			return
		}
		c.mu.Unlock()
		<-c.notify
	}
}

// halfClose sends FIN and leaves the read side open, so a peer still reading
// receives everything that was written before the socket disappears. The
// graceful timer set by Close tears the socket down if the peer never answers.
func (c *conn) halfClose() {
	if half, ok := c.socket.(interface{ CloseWrite() error }); ok {
		if err := half.CloseWrite(); err == nil {
			return
		}
	}
	_ = c.socket.Close()
}

// recordWriteError reports the first write failure on this socket and tears it
// down. Later failures are the same failure, so only the first is reported.
func (c *conn) recordWriteError(err error) {
	c.mu.Lock()
	first := !c.writeFailed
	c.writeFailed = true
	closing := c.closing
	c.mu.Unlock()
	// A write that fails while the connection is already closing is the peer
	// hanging up on its own final bytes, which is not a failure worth telling
	// anyone about.
	if first && !closing && c.reportError != nil {
		c.reportError(fmt.Errorf(
			"Unix connection write failed and the connection was dropped; the peer stopped reading: %w", err))
	}
	_ = c.socket.Close()
}
