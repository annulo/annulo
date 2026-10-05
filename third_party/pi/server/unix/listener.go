//go:build unix

package unix

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/sky-valley/pi/protocol"
	"github.com/sky-valley/pi/server"
)

const (
	defaultSocketMode          = 0o600
	defaultGracefulCloseTimout = 5 * time.Second
	socketProbeTimeout         = 1 * time.Second
	// maxUint32 is uint64 because it does not fit in an int on a 32-bit build.
	maxUint32      uint64 = 0xffff_ffff
	readBufferSize        = 64 * 1024

	// maxGracefulCloseTimeout is Node's maximum timer delay, kept for the same
	// reason server.maxHandshakeTimeout is: a configuration a Node pi server
	// rejects is rejected here too.
	maxGracefulCloseTimeout = 2147483647 * time.Millisecond

	// minAcceptRetryDelay and maxAcceptRetryDelay are net/http.Server.Serve's
	// backoff schedule for transient accept failures.
	minAcceptRetryDelay = 5 * time.Millisecond
	maxAcceptRetryDelay = 1 * time.Second
)

// maxSocketPathBytes is what fits in sockaddr_un.sun_path, which differs by
// platform and is checked before binding so an over-long path fails with an
// explanation rather than a truncated socket.
func maxSocketPathBytes() int {
	if runtime.GOOS == "linux" {
		return 107
	}
	return 103
}

// ValidateSocketPath rejects a path a Unix socket cannot be bound at.
// description names the path in the error, since a listener validates both the
// configured path and the private one it derives from it.
func ValidateSocketPath(path, description string) error {
	if description == "" {
		description = "Unix socket path"
	}
	if path == "" {
		return fmt.Errorf("%s must not be empty", description)
	}
	if len(path) > maxSocketPathBytes() {
		return fmt.Errorf("%s is too long; maximum is %d UTF-8 bytes", description, maxSocketPathBytes())
	}
	return nil
}

// fileIdentity is the device and inode pair that says a path still refers to
// the same file it did earlier.
type fileIdentity struct {
	dev uint64
	ino uint64
}

type resolvedOptions struct {
	path                 string
	mode                 os.FileMode
	gracefulCloseTimeout time.Duration
	maxPendingBytes      int
	onError              func(error)
}

func resolveOptions(options ListenerOptions) (resolvedOptions, error) {
	if err := ValidateSocketPath(options.Path, "PiServer Unix socket path"); err != nil {
		return resolvedOptions{}, err
	}
	mode := os.FileMode(defaultSocketMode)
	if options.Mode != 0 {
		if options.Mode > 0o777 {
			return resolvedOptions{}, errors.New("PiServer Unix socket mode must be an integer between 0 and 0o777")
		}
		mode = os.FileMode(options.Mode)
	}
	maxFrameLength := protocol.DefaultMaxFrameLength
	if options.MaxFrameLength != nil {
		maxFrameLength = *options.MaxFrameLength
	}
	if maxFrameLength <= 0 || uint64(maxFrameLength) > maxUint32 {
		return resolvedOptions{}, fmt.Errorf("PiServer maxFrameLength must be an integer between 1 and %d", maxUint32)
	}
	maxPendingBytes := options.MaxPendingBytes
	if maxPendingBytes == 0 {
		maxPendingBytes = maxFrameLength * 4
	}
	if maxPendingBytes < maxFrameLength+4 {
		return resolvedOptions{}, errors.New("PiServer maxPendingBytes must be at least maxFrameLength + 4")
	}
	gracefulCloseTimeout := options.GracefulCloseTimeout
	if gracefulCloseTimeout == 0 {
		gracefulCloseTimeout = defaultGracefulCloseTimout
	}
	if gracefulCloseTimeout < time.Millisecond ||
		gracefulCloseTimeout > maxGracefulCloseTimeout ||
		gracefulCloseTimeout%time.Millisecond != 0 {
		return resolvedOptions{}, fmt.Errorf(
			"PiServer gracefulCloseTimeout must be a whole number of milliseconds between 1ms and %s, or zero for the %s default",
			maxGracefulCloseTimeout, defaultGracefulCloseTimout)
	}
	return resolvedOptions{
		path:                 options.Path,
		mode:                 mode,
		gracefulCloseTimeout: gracefulCloseTimeout,
		maxPendingBytes:      maxPendingBytes,
		onError:              options.OnError,
	}, nil
}

// Listener binds one Unix-domain socket and serves the connections it accepts.
type Listener struct {
	options resolvedOptions

	mu             sync.Mutex
	netListener    *net.UnixListener
	connections    map[*conn]struct{}
	socketIdentity *fileIdentity
	ownedBindPath  string
	boundPath      string
	closing        bool
	closed         chan struct{}
	accept         server.Acceptor
	acceptDone     chan struct{}
	// stopAccept is closed as soon as Close begins, so an accept loop that is
	// backing off after a transient failure gives up immediately instead of
	// making Close wait out its delay.
	stopAccept chan struct{}
}

var _ server.Listener = (*Listener)(nil)

// NewListener validates options and returns an unbound listener.
func NewListener(options ListenerOptions) (*Listener, error) {
	resolved, err := resolveOptions(options)
	if err != nil {
		return nil, err
	}
	return &Listener{options: resolved, connections: map[*conn]struct{}{}}, nil
}

// Address is the bound socket path, or empty before Start and after Close.
func (l *Listener) Address() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.boundPath
}

// Start binds the socket and begins accepting.
//
// The socket is bound at a private derived path and then hard-linked to the
// configured one. Binding directly would leave a window where the configured
// path exists but is not yet permission-restricted; linking publishes an
// already-bound, already-owned socket in one atomic step.
func (l *Listener) Start(ctx context.Context, accept server.Acceptor) error {
	l.mu.Lock()
	switch {
	case l.netListener != nil:
		l.mu.Unlock()
		return errors.New("Unix listener is already started")
	case l.closing:
		l.mu.Unlock()
		return errors.New("Unix listener is closing or closed")
	}
	l.accept = accept
	l.mu.Unlock()

	path := l.options.path
	ownedBindPath := derivedBindPath(path)
	if err := ValidateSocketPath(ownedBindPath, "PiServer private Unix bind path"); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := removeStaleSocket(path); err != nil {
		return err
	}
	if err := removeStaleSocket(ownedBindPath); err != nil {
		return err
	}

	netListener, err := net.ListenUnix("unix", &net.UnixAddr{Name: ownedBindPath, Net: "unix"})
	if err != nil {
		return err
	}
	// The listener does not unlink anything on its own: every removal here is
	// gated on an inode identity check, and Go's would not be.
	netListener.SetUnlinkOnClose(false)

	l.mu.Lock()
	l.netListener = netListener
	l.ownedBindPath = ownedBindPath
	l.closed = make(chan struct{})
	l.mu.Unlock()

	if err := l.publish(ctx, ownedBindPath, path); err != nil {
		l.closeListenerAndCleanup(netListener)
		l.mu.Lock()
		l.netListener = nil
		l.mu.Unlock()
		return err
	}

	l.mu.Lock()
	l.boundPath = path
	l.acceptDone = make(chan struct{})
	l.stopAccept = make(chan struct{})
	acceptDone := l.acceptDone
	l.mu.Unlock()
	go l.acceptLoop(netListener, acceptDone)
	return nil
}

func (l *Listener) publish(_ context.Context, ownedBindPath, path string) error {
	identity, isSocket, err := statIdentity(ownedBindPath)
	if err != nil {
		return err
	}
	if !isSocket {
		return fmt.Errorf("Unix listener path is not a socket after binding: %s", ownedBindPath)
	}
	l.mu.Lock()
	l.socketIdentity = &identity
	l.mu.Unlock()

	if err := os.Link(ownedBindPath, path); err != nil {
		return err
	}
	return setSocketMode(path, l.options.mode)
}

// acceptLoop serves the listener until it is closed.
//
// A transient accept failure — the process is out of descriptors, the peer
// vanished between the connect and the accept — must not retire the transport:
// Address would keep reporting a bound socket that answers nobody, for the life
// of the process. The retry schedule is net/http.Server.Serve's, and the
// backoff is abandoned as soon as Close asks it to stop.
func (l *Listener) acceptLoop(netListener net.Listener, done chan struct{}) {
	defer close(done)
	delay := time.Duration(0)
	for {
		socket, err := netListener.Accept()
		if err != nil {
			l.mu.Lock()
			closing := l.closing
			stop := l.stopAccept
			l.mu.Unlock()
			if closing || errors.Is(err, net.ErrClosed) {
				return
			}
			if !isTemporaryAcceptError(err) {
				l.reportError(err)
				return
			}
			delay = min(max(2*delay, minAcceptRetryDelay), maxAcceptRetryDelay)
			l.reportError(fmt.Errorf(
				"Unix listener accept failed, retrying in %s; the process may be out of file descriptors: %w",
				delay, err))
			timer := time.NewTimer(delay)
			select {
			case <-timer.C:
			case <-stop:
				timer.Stop()
				return
			}
			continue
		}
		delay = 0
		l.serve(socket)
	}
}

// isTemporaryAcceptError reports the accept failures that say nothing about
// whether the next accept will work.
func isTemporaryAcceptError(err error) bool {
	for _, code := range []syscall.Errno{
		syscall.EMFILE, syscall.ENFILE, syscall.ENOBUFS, syscall.ENOMEM,
		syscall.ECONNABORTED, syscall.EINTR, syscall.EPERM, syscall.EPROTO,
	} {
		if errors.Is(err, code) {
			return true
		}
	}
	return false
}

func (l *Listener) serve(socket net.Conn) {
	l.mu.Lock()
	closing := l.closing
	accept := l.accept
	l.mu.Unlock()
	if closing || accept == nil {
		_ = socket.Close()
		return
	}

	c := newConn(socket, l.options.gracefulCloseTimeout, l.options.maxPendingBytes, l.reportError)
	l.mu.Lock()
	l.connections[c] = struct{}{}
	l.mu.Unlock()

	handler := accept(c)
	go l.readLoop(c, handler)
}

// readLoop owns the socket's read side. The buffer is reused between reads: a
// handler may inspect the chunk but must not retain it, which is the contract
// server.ConnHandler.OnData states and the protocol decoder honours.
func (l *Listener) readLoop(c *conn, handler server.ConnHandler) {
	buffer := make([]byte, readBufferSize)
	for {
		n, err := c.socket.Read(buffer)
		if n > 0 {
			handler.OnData(buffer[:n])
		}
		if err != nil {
			if !errors.Is(err, io.EOF) && !isClosedConnError(err) {
				handler.OnError(err)
			}
			break
		}
	}
	// Nothing downstream closes the socket: conn.Close only half-closes, so a
	// peer still reading gets the final frame, and markClosed merely records
	// that the socket is gone. Closing it here is what releases the descriptor
	// and what sends FIN to a peer that only closed its own write side.
	_ = c.socket.Close()
	c.markClosed()
	l.mu.Lock()
	delete(l.connections, c)
	l.mu.Unlock()
	handler.OnClose()
}

// Close unbinds the socket, closes every connection, and removes the paths this
// listener created — and only those.
func (l *Listener) Close(ctx context.Context) error {
	l.mu.Lock()
	if l.closing {
		closed := l.closed
		l.mu.Unlock()
		if closed == nil {
			return nil
		}
		select {
		case <-closed:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	l.closing = true
	l.boundPath = ""
	netListener := l.netListener
	connections := make([]*conn, 0, len(l.connections))
	for c := range l.connections {
		connections = append(connections, c)
	}
	acceptDone := l.acceptDone
	closed := l.closed
	// Closed, not cleared: the accept loop reads it under this lock and a nil
	// there would leave it waiting out a backoff it has already been told to
	// abandon. The closing guard above is what keeps this to one close.
	if l.stopAccept != nil {
		close(l.stopAccept)
	}
	l.mu.Unlock()

	var err error
	if netListener != nil {
		err = l.closeListenerAndCleanup(netListener)
		if acceptDone != nil {
			select {
			case <-acceptDone:
			case <-ctx.Done():
				err = ctx.Err()
			}
		}
	} else {
		err = l.cleanupOwnedSocket()
	}

	for _, c := range connections {
		_ = c.Close(nil)
	}

	l.mu.Lock()
	ownedBindPath := l.ownedBindPath
	l.ownedBindPath = ""
	clear(l.connections)
	l.netListener = nil
	l.mu.Unlock()
	if ownedBindPath != "" {
		if removeErr := removePath(ownedBindPath); removeErr != nil && err == nil {
			err = removeErr
		}
	}
	if closed != nil {
		close(closed)
	}
	return err
}

func (l *Listener) closeListenerAndCleanup(netListener *net.UnixListener) error {
	closeErr := netListener.Close()
	cleanupErr := l.cleanupOwnedSocket()

	l.mu.Lock()
	ownedBindPath := l.ownedBindPath
	l.ownedBindPath = ""
	l.mu.Unlock()
	if ownedBindPath != "" {
		if err := removePath(ownedBindPath); err != nil && cleanupErr == nil {
			cleanupErr = err
		}
	}
	if cleanupErr != nil {
		return cleanupErr
	}
	if closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
		return closeErr
	}
	return nil
}

// cleanupOwnedSocket removes the published path, but only while it still refers
// to the inode this listener bound. Anything else at that path belongs to
// somebody else and is left alone.
func (l *Listener) cleanupOwnedSocket() error {
	l.mu.Lock()
	identity := l.socketIdentity
	l.socketIdentity = nil
	path := l.options.path
	l.mu.Unlock()
	if identity == nil {
		return nil
	}

	current, isSocket, err := statIdentity(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if !isSocket || current != *identity {
		return nil
	}

	// Rename before unlinking so the removal is committed to one inode: if the
	// path is swapped in the gap, the swap is what stays behind.
	preserved := filepath.Join(filepath.Dir(path), ".c-"+randomSuffix())
	if err := os.Rename(path, preserved); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	moved, movedIsSocket, err := statIdentity(preserved)
	if err != nil {
		return err
	}
	if movedIsSocket && moved == *identity {
		return removePath(preserved)
	}
	if _, err := os.Lstat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			if renameErr := os.Rename(preserved, path); renameErr != nil {
				return renameErr
			}
		} else {
			return err
		}
	}
	return fmt.Errorf("Unix listener path changed during cleanup; preserved replacement at %s", preserved)
}

func (l *Listener) reportError(err error) {
	if l.options.onError == nil || err == nil {
		return
	}
	defer func() { _ = recover() }()
	l.options.onError(err)
}

// derivedBindPath is the private path a listener binds before publishing. It is
// derived from the configured path so two listeners for different sockets in
// one directory never collide.
func derivedBindPath(path string) string {
	digest := sha256.Sum256([]byte(path))
	return filepath.Join(filepath.Dir(path), ".p-"+hex.EncodeToString(digest[:])[:8])
}

// removeStaleSocket clears a socket left behind by a dead process, and refuses
// to touch anything else: a regular file is an error, and a socket somebody is
// still listening on means this listener is a duplicate.
func removeStaleSocket(path string) error {
	original, isSocket, err := statIdentity(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if !isSocket {
		return fmt.Errorf("Refusing to remove non-socket Unix listener path: %s", path)
	}
	live, err := isSocketLive(path)
	if err != nil {
		return err
	}
	if live {
		return fmt.Errorf("Unix listener is already running: %s", path)
	}

	preserved := filepath.Join(filepath.Dir(path), ".s-"+randomSuffix())
	if err := os.Rename(path, preserved); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	current, currentIsSocket, err := statIdentity(preserved)
	if err != nil {
		return err
	}
	if !currentIsSocket || current != original {
		if _, statErr := os.Lstat(path); statErr != nil {
			if errors.Is(statErr, fs.ErrNotExist) {
				if renameErr := os.Rename(preserved, path); renameErr != nil {
					return renameErr
				}
			} else {
				return statErr
			}
		}
		return fmt.Errorf("Unix listener path changed while checking for a stale socket: %s", path)
	}
	return removePath(preserved)
}

func removePath(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// isSocketLive reports whether anything is listening at path. A probe that
// times out counts as live: refusing to bind is the safe answer when the
// question cannot be settled.
func isSocketLive(path string) (bool, error) {
	socket, err := net.DialTimeout("unix", path, socketProbeTimeout)
	if err == nil {
		_ = socket.Close()
		return true, nil
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true, nil
	}
	for _, code := range []syscall.Errno{
		syscall.ECONNREFUSED, syscall.ENOENT, syscall.EPIPE, syscall.ECONNRESET,
	} {
		if errors.Is(err, code) {
			return false, nil
		}
	}
	return false, err
}

func setSocketMode(path string, mode os.FileMode) error {
	if err := os.Chmod(path, mode); err != nil {
		if errors.Is(err, syscall.ENOSYS) || errors.Is(err, syscall.ENOTSUP) {
			return nil
		}
		return err
	}
	return nil
}

func statIdentity(path string) (fileIdentity, bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return fileIdentity{}, false, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fileIdentity{}, info.Mode()&fs.ModeSocket != 0, nil
	}
	identity := fileIdentity{dev: uint64(stat.Dev), ino: uint64(stat.Ino)}
	return identity, info.Mode()&fs.ModeSocket != 0, nil
}

func randomSuffix() string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// isClosedConnError reports the read error a socket produces once it has been
// closed from this side, which is an orderly end rather than a failure.
func isClosedConnError(err error) bool {
	return errors.Is(err, net.ErrClosed) || errors.Is(err, syscall.ECONNRESET)
}
