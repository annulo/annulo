package servertest

import (
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/sky-valley/pi/protocol"
)

// Timeout bounds every wait a test makes on a client.
const Timeout = 5 * time.Second

// Client is a raw protocol peer: it speaks framed CBOR straight at a server
// without going through the client package, so a server test observes exactly
// what a Node peer would see on the wire.
type Client struct {
	conn net.Conn

	mu       sync.Mutex
	messages []protocol.ServerMessage
	waiters  []*waiter
	readErr  error

	closed    chan struct{}
	closeOnce sync.Once
}

type waiter struct {
	from   int
	match  func(protocol.ServerMessage) bool
	result chan protocol.ServerMessage
}

// Dial connects to a Unix socket and starts reading.
func Dial(path string) (*Client, error) {
	conn, err := net.DialTimeout("unix", path, Timeout)
	if err != nil {
		return nil, err
	}
	client := &Client{conn: conn, closed: make(chan struct{})}
	go client.readLoop()
	return client, nil
}

func (c *Client) readLoop() {
	decoder, err := protocol.NewServerMessageDecoder(nil)
	if err != nil {
		c.finish(err)
		return
	}
	buffer := make([]byte, 64*1024)
	for {
		n, readErr := c.conn.Read(buffer)
		if n > 0 {
			messages, decodeErr := decoder.Push(buffer[:n])
			if decodeErr != nil {
				c.finish(decodeErr)
				return
			}
			c.deliver(messages)
		}
		if readErr != nil {
			c.finish(readErr)
			return
		}
	}
}

func (c *Client) deliver(messages []protocol.ServerMessage) {
	c.mu.Lock()
	type ready struct {
		w       *waiter
		message protocol.ServerMessage
	}
	var resolved []ready
	for _, message := range messages {
		index := len(c.messages)
		c.messages = append(c.messages, message)
		remaining := c.waiters[:0]
		for _, w := range c.waiters {
			if index >= w.from && w.match(message) {
				resolved = append(resolved, ready{w: w, message: message})
				continue
			}
			remaining = append(remaining, w)
		}
		c.waiters = remaining
	}
	c.mu.Unlock()
	for _, entry := range resolved {
		entry.w.result <- entry.message
	}
}

func (c *Client) finish(err error) {
	c.mu.Lock()
	if c.readErr == nil {
		c.readErr = err
	}
	waiters := c.waiters
	c.waiters = nil
	c.mu.Unlock()
	for _, w := range waiters {
		close(w.result)
	}
	c.closeOnce.Do(func() { close(c.closed) })
}

// Messages is every message received so far.
func (c *Client) Messages() []protocol.ServerMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]protocol.ServerMessage(nil), c.messages...)
}

// Count is how many messages have been received so far.
func (c *Client) Count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.messages)
}

// Next waits for the next message matching a predicate, ignoring everything
// received before the call.
func (c *Client) Next(match func(protocol.ServerMessage) bool) (protocol.ServerMessage, error) {
	return c.NextFrom(c.Count(), match)
}

// NextFrom waits for a matching message at or after index from, which lets a
// test anchor a wait before the traffic it is waiting on is provoked.
func (c *Client) NextFrom(from int, match func(protocol.ServerMessage) bool) (protocol.ServerMessage, error) {
	c.mu.Lock()
	for i := from; i < len(c.messages); i++ {
		if match(c.messages[i]) {
			message := c.messages[i]
			c.mu.Unlock()
			return message, nil
		}
	}
	if c.readErr != nil {
		err := c.readErr
		c.mu.Unlock()
		return nil, fmt.Errorf("connection ended before a matching message arrived: %w", err)
	}
	w := &waiter{from: from, match: match, result: make(chan protocol.ServerMessage, 1)}
	c.waiters = append(c.waiters, w)
	c.mu.Unlock()

	select {
	case message, ok := <-w.result:
		if !ok {
			return nil, errors.New("connection ended before a matching message arrived")
		}
		return message, nil
	case <-time.After(Timeout):
		return nil, errors.New("timed out waiting for a matching message")
	}
}

// WaitForClose blocks until the connection has ended.
func (c *Client) WaitForClose() error {
	select {
	case <-c.closed:
		return nil
	case <-time.After(Timeout):
		return errors.New("timed out waiting for the connection to close")
	}
}

// Close hangs up.
func (c *Client) Close() error {
	err := c.conn.Close()
	<-c.closed
	return err
}

// SendBytes writes raw bytes, bypassing framing.
func (c *Client) SendBytes(chunk []byte) error {
	_, err := c.conn.Write(chunk)
	return err
}

// SendMessage encodes and writes one client message.
func (c *Client) SendMessage(message protocol.ClientMessage) error {
	frame, err := protocol.EncodeClientMessage(message, nil)
	if err != nil {
		return err
	}
	return c.SendBytes(frame)
}

// SendFragmented writes one client message split across parts writes, so a
// server is forced to reassemble it.
func (c *Client) SendFragmented(message protocol.ClientMessage, parts int) error {
	frame, err := protocol.EncodeClientMessage(message, nil)
	if err != nil {
		return err
	}
	if parts < 1 {
		parts = 1
	}
	size := (len(frame) + parts - 1) / parts
	for offset := 0; offset < len(frame); offset += size {
		end := min(offset+size, len(frame))
		if err := c.SendBytes(frame[offset:end]); err != nil {
			return err
		}
	}
	return nil
}

// Hello performs the handshake and returns the server's answer, which may be a
// hello_error.
//
// The read position is captured before the request goes out, not after: a
// server can answer before the caller gets back to waiting, and a wait anchored
// afterwards would step straight over the answer it was waiting for.
func (c *Client) Hello(version int64) (protocol.ServerMessage, error) {
	from := c.Count()
	if err := c.SendMessage(&protocol.ClientHello{Type: "hello", Version: version}); err != nil {
		return nil, err
	}
	return c.NextFrom(from, func(message protocol.ServerMessage) bool {
		switch message.(type) {
		case *protocol.ServerHello, *protocol.ServerHelloError:
			return true
		}
		return false
	})
}

// Request sends one command and waits for its response.
func (c *Client) Request(id string, command protocol.Command) (*protocol.ResponseEnvelope, error) {
	from := c.Count()
	if err := c.SendMessage(protocol.NewRequest(id, command)); err != nil {
		return nil, err
	}
	message, err := c.NextFrom(from, func(message protocol.ServerMessage) bool {
		response, ok := message.(*protocol.ResponseEnvelope)
		return ok && response.ID == id
	})
	if err != nil {
		return nil, err
	}
	return message.(*protocol.ResponseEnvelope), nil
}
