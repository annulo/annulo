package client

import "context"

// ConnectionState is where a client sits in its connection lifecycle.
type ConnectionState string

const (
	Disconnected ConnectionState = "disconnected"
	Connecting   ConnectionState = "connecting"
	Connected    ConnectionState = "connected"
)

// ConnectionStateChange is delivered to connection-state subscribers. Err is
// set only when the move to Disconnected was caused by a failure.
type ConnectionStateChange struct {
	State ConnectionState
	Err   error
}

// Unsubscribe cancels a subscription. It is safe to call more than once.
type Unsubscribe func()

// Transport carries opaque byte chunks for one connection attempt.
type Transport interface {
	// Send delivers one chunk. Calls must arrive in invocation order.
	Send(chunk []byte) error
	// Close shuts the transport down. Repeated calls must be harmless.
	Close() error
}

// TransportHandlers receives everything a transport observes. Exactly one of
// OnClose or OnError is expected, once.
//
// All three fields are required: a transport calling a nil handler panics, and
// the client always supplies a complete set. They are a struct of funcs rather
// than an interface so a transport can be written as a closure over a
// connection without declaring a type.
type TransportHandlers struct {
	// OnData delivers an arbitrary inbound byte chunk.
	OnData func(chunk []byte)
	// OnClose reports an orderly terminal close.
	OnClose func()
	// OnError reports a terminal transport failure.
	OnError func(err error)
}

// TransportFactory creates a fresh connected, authenticated transport for each
// connection attempt. Authentication belongs to the transport, so a factory
// must complete whatever its transport requires before it returns.
//
// ctx bounds establishment only: a factory must abandon a connect that is still
// in progress when ctx is done, and must ignore ctx once it has returned a
// transport. It is Connect's context, and Connect cannot give up on an attempt
// the factory is still setting up unless the factory cooperates.
//
// DIVERGENCE (deliberate): pi's ByteTransportFactory takes handlers only — it
// has no cancellation anywhere, so a factory that never settles hangs connect()
// forever. The context is surface this port invented; nothing upstream
// corresponds to it.
type TransportFactory func(ctx context.Context, handlers TransportHandlers) (Transport, error)
