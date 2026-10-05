package server

import "context"

// Listener is a transport that supplies established byte connections to a
// Server. Authentication and access control belong to the transport: a
// Listener must complete whatever its transport requires before it hands a
// connection to the Server, which treats every connection it accepts as
// already authorized.
type Listener interface {
	// Address is the human-readable bound address after startup. It is empty
	// before Start, after Close, and for transports that have no address.
	//
	// DIVERGENCE (deliberate): pi types this `string | undefined` and the server
	// filters the undefined ones out of its address list. Go has no undefined
	// string, so the empty string carries that meaning and is filtered the same
	// way — an address that is genuinely "" would not be printable anyway.
	Address() string
	// Start binds the transport and calls accept for each authorized
	// connection. It must not return until the transport is bound or has
	// failed.
	Start(ctx context.Context, accept Acceptor) error
	// Close unbinds the transport and releases its resources. Repeated calls
	// must be harmless. It should honour ctx at anything it waits on: Server
	// closes its listeners with the context its own Close was given, and a
	// caller that asked for a bounded shutdown must get one.
	Close(ctx context.Context) error
}
