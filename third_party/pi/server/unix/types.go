//go:build unix

// Package unix is the Go port of pi's Unix-domain-socket transport for the
// session server (@earendil-works/pi-server transports/unix): it binds a
// filesystem socket and hands the connections it accepts to a server.Server.
//
// Binding is deliberately careful with the filesystem. The socket is bound at a
// private derived path and hard-linked into place, and it is only ever removed
// after its device and inode have been confirmed to be the one this process
// bound — so a listener never unlinks a file it does not own, and never removes
// a replacement another process put there.
//
// Every file in the package is constrained to unix, so the package is empty on
// a platform with no Unix-domain sockets rather than exporting option structs
// nothing can construct a listener from.
package unix

import (
	"time"

	"github.com/sky-valley/pi/server"
)

// ListenerOptions configures one Unix-domain listener.
type ListenerOptions struct {
	// Path is where the socket is published. It must fit in sockaddr_un.
	Path string
	// Mode is the socket's filesystem permissions. Zero takes owner read/write
	// only (0o600).
	//
	// DIVERGENCE (deliberate): pi distinguishes an absent mode from an explicit
	// 0o000. Go's zero value cannot, and a socket nobody may open is not a
	// configuration worth preserving the ability to express.
	Mode uint32
	// MaxPendingBytes bounds the framed bytes queued for one connection before
	// a slow peer is disconnected. Zero takes MaxFrameLength * 4.
	MaxPendingBytes int
	// GracefulCloseTimeout bounds how long a closing connection may take to
	// flush before its socket is torn down. Zero takes 5s.
	GracefulCloseTimeout time.Duration
	// MaxFrameLength is used to derive and validate MaxPendingBytes. It must
	// match the server's when customised. nil takes the protocol default.
	MaxFrameLength *int
	// OnError observes transport failures. It may be nil.
	OnError func(error)
}

// ServerOptions configures a server with exactly one Unix-domain listener. It
// is the server's own options plus the listener's, minus the listener list the
// preset supplies.
type ServerOptions struct {
	MaxFrameLength   *int
	HandshakeTimeout time.Duration
	ServerID         string
	OnError          func(error)

	Path                 string
	Mode                 uint32
	MaxPendingBytes      int
	GracefulCloseTimeout time.Duration
}

// serverOptions projects the server half of ServerOptions.
func (o ServerOptions) serverOptions(listener server.Listener) server.Options {
	return server.Options{
		Listeners:        []server.Listener{listener},
		MaxFrameLength:   o.MaxFrameLength,
		HandshakeTimeout: o.HandshakeTimeout,
		ServerID:         o.ServerID,
		OnError:          o.OnError,
	}
}

// listenerOptions projects the listener half of ServerOptions.
func (o ServerOptions) listenerOptions() ListenerOptions {
	return ListenerOptions{
		Path:                 o.Path,
		Mode:                 o.Mode,
		MaxPendingBytes:      o.MaxPendingBytes,
		GracefulCloseTimeout: o.GracefulCloseTimeout,
		MaxFrameLength:       o.MaxFrameLength,
		OnError:              o.OnError,
	}
}
