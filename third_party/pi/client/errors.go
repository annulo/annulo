// Package client is the Go port of pi's runtime-neutral remote-session client
// (@earendil-works/pi-client): it speaks the protocol package's wire format to
// a pi server over any byte transport.
package client

import (
	"errors"

	"github.com/sky-valley/pi/protocol"
)

// defaultDisconnectedMessage is pi's PiDisconnectedError default.
const defaultDisconnectedMessage = "Pi client is disconnected"

// ServerError is a failure the server reported in a response or hello_error.
//
// These are distinct types rather than formatted strings so a caller can branch
// on the code with errors.As — which is the whole point of the protocol
// carrying a code alongside the message.
type ServerError struct {
	Code    protocol.ProtocolErrorCode
	Message string
	Details any
}

func (e *ServerError) Error() string { return e.Message }

// DisconnectedError means the connection is not usable.
type DisconnectedError struct {
	Message string
	// Cause is the failure that ended the connection, when there was one: the
	// transport's own error, or the context error that abandoned the attempt.
	// It is nil for a disconnect this side asked for.
	//
	// DIVERGENCE (deliberate): pi's PiDisconnectedError carries a message only,
	// because toDisconnectedError flattens whatever it was handed into one.
	// Keeping the cause is what makes errors.Is work across the boundary — a
	// caller retrying on syscall.ECONNREFUSED, or distinguishing its own
	// cancellation from a server that went away, has nothing else to branch on.
	Cause error
}

func (e *DisconnectedError) Error() string {
	if e.Message == "" {
		return defaultDisconnectedMessage
	}
	return e.Message
}

// Unwrap exposes the cause to errors.Is and errors.As.
func (e *DisconnectedError) Unwrap() error { return e.Cause }

// DisposedError means the client has been disposed and will not reconnect.
type DisposedError struct{}

func (e *DisposedError) Error() string { return "Pi client is disposed" }

// SessionOwnershipError means this client does not own the session.
type SessionOwnershipError struct {
	SessionID string
	Message   string
}

func (e *SessionOwnershipError) Error() string { return e.Message }

// SessionDetachedError means the session is not attached on this connection.
type SessionDetachedError struct{ SessionID string }

func (e *SessionDetachedError) Error() string {
	return "Session " + e.SessionID + " is not attached"
}

// newServerError converts a protocol error into a client-side error.
// The caller is the response path, where protocol validation has already
// established that a failed response carries an error — so there is no nil
// branch here. Guarding it would mean inventing a code that a caller could
// then branch on, which is worse than the nil dereference a real bug deserves.
func newServerError(err *protocol.ProtocolError) *ServerError {
	serverErr := &ServerError{Code: err.Code, Message: err.Message}
	if err.Details != nil {
		serverErr.Details = *err.Details
	}
	return serverErr
}

// asDisconnectedError coerces any error into a DisconnectedError.
//
// An error that is already a disconnect is returned unchanged rather than
// re-wrapped, so the original message survives every hop — pi's
// toDisconnectedError does the same.
//
// DIVERGENCE (deliberate): a nil cause yields the default message. pi's
// toError(undefined) would produce the string "undefined"; in Go a nil error is
// the ordinary way a transport reports an orderly close, so surfacing that as
// the literal text "undefined" would be a worse message, not a more faithful
// one.
func asDisconnectedError(err error) *DisconnectedError {
	if err == nil {
		return &DisconnectedError{}
	}
	// errors.As, not a type assertion: by the time a transport failure reaches
	// here it has usually been wrapped at least once, and flattening a wrapped
	// disconnect into a fresh one loses the original message.
	var disconnected *DisconnectedError
	if errors.As(err, &disconnected) {
		return disconnected
	}
	return &DisconnectedError{Message: err.Error(), Cause: err}
}
