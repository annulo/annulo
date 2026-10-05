package server

import (
	"slices"

	"github.com/sky-valley/pi/protocol"
)

// Default messages for the three named failures pi exports as Error subclasses.
const (
	defaultBusyMessage     = "Session is busy"
	defaultLockedMessage   = "Session is locked"
	defaultNotFoundMessage = "Session was not found"
)

// The two messages the server substitutes for a failure's own text. They are
// byte-golden: a peer reads them off the wire.
const (
	// InternalErrorMessage answers any failure that is not safe to describe.
	InternalErrorMessage = "Internal server error"
	// NotImplementedMessage answers a missing operation without naming which
	// internals are missing.
	NotImplementedMessage = "Operation is not implemented"
)

// operationErrorCodes are the protocol error codes a service or runtime is
// allowed to put on the wire. auth and version belong to the handshake and are
// the server's alone to report.
var operationErrorCodes = []protocol.ProtocolErrorCode{
	protocol.ErrorBusy,
	protocol.ErrorSessionLocked,
	protocol.ErrorNotFound,
	protocol.ErrorInvalidRequest,
	protocol.ErrorNotImplemented,
}

// Error is a service or runtime failure that can safely cross the protocol
// boundary: its code and message reach the client verbatim — except under code
// not_implemented, whose message is replaced by NotImplementedMessage. Any
// other error a service returns is reported to the server's error observer and
// answered with a generic internal_error, so private detail never leaks to a
// peer.
//
// DIVERGENCE (deliberate): pi exports SessionBusyError, SessionLockedError and
// SessionNotFoundError as subclasses that differ only in their default message.
// Go has no subclassing worth imitating here, and a caller branches on the code
// rather than the type — so this is one type plus three constructors, and
// errors.As(err, &serverErr) followed by a check on Code replaces instanceof.
type Error struct {
	Code    protocol.ProtocolErrorCode
	Message string
	Details any
}

func (e *Error) Error() string { return e.Message }

// NewError builds an Error. details may be nil.
func NewError(code protocol.ProtocolErrorCode, message string, details any) *Error {
	return &Error{Code: code, Message: message, Details: details}
}

// NewBusyError reports that a conflicting operation is already running. An
// empty message takes pi's default.
func NewBusyError(message string, details any) *Error {
	return NewError(protocol.ErrorBusy, orDefault(message, defaultBusyMessage), details)
}

// NewLockedError reports that another holder owns the session. An empty message
// takes pi's default.
func NewLockedError(message string, details any) *Error {
	return NewError(protocol.ErrorSessionLocked, orDefault(message, defaultLockedMessage), details)
}

// NewNotFoundError reports that the session does not exist. An empty message
// takes pi's default.
func NewNotFoundError(message string, details any) *Error {
	return NewError(protocol.ErrorNotFound, orDefault(message, defaultNotFoundMessage), details)
}

// NewNotImplementedError reports that the operation does not exist. It takes no
// message: the one that reaches the peer is always NotImplementedMessage.
func NewNotImplementedError() *Error {
	return NewError(protocol.ErrorNotImplemented, NotImplementedMessage, nil)
}

// InternalError is a failure that is NOT safe to describe to a peer. It carries
// its cause for the server's own error observer, and the wire sees only
// InternalErrorMessage — a service wraps a storage or runtime failure in one of
// these to have it reported locally without leaking what broke.
//
// DIVERGENCE (deliberate): pi's InternalServerError extends Error rather than
// PiServerError, which is how it stays out of the branch that copies a code and
// message onto the wire. Go has no hierarchy to lean on, so the separation is
// the type itself — InternalError is not an *Error and carries no protocol
// code, and toProtocolError matches it first.
type InternalError struct {
	Cause error
}

// Error names the cause, because that is what an error's message is for and
// not every reporter unwraps — a snapshot broadcast or a queued session job
// hands the observer this wrapper itself. Sanitization is the wire formatter's
// job: toProtocolError writes InternalErrorMessage directly and never asks the
// error for its text.
func (e *InternalError) Error() string {
	if e.Cause == nil {
		return InternalErrorMessage
	}
	return "internal server error: " + e.Cause.Error()
}

func (e *InternalError) Unwrap() error { return e.Cause }

// NewInternalError wraps a failure whose detail must not leave the process.
func NewInternalError(cause error) *InternalError {
	return &InternalError{Cause: cause}
}

func orDefault(message, fallback string) string {
	if message == "" {
		return fallback
	}
	return message
}

// crossesProtocolBoundary reports whether this Error's code is one a service
// may report.
//
// DIVERGENCE (deliberate): pi constrains the code with a TypeScript type, which
// costs nothing at runtime and cannot be violated. Go cannot express that, so
// the check moves to the one place it matters — the wire — and an out-of-range
// code is downgraded to the generic internal error rather than letting a
// service forge an auth or version failure.
func (e *Error) crossesProtocolBoundary() bool {
	return slices.Contains(operationErrorCodes, e.Code)
}
