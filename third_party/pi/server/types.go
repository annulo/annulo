// Package server is the Go port of pi's transport-neutral session server
// (@earendil-works/pi-server): it serves the protocol package's wire format to
// pi clients over any ordered byte transport.
//
// The core in this package owns the handshake, the connection lifecycle, and
// session ownership. It knows nothing about sockets — a transport supplies
// connections through a Listener. The Unix-domain transport lives in
// server/unix.
//
// Authentication is the transport's, not the protocol's: a Listener completes
// whatever its transport requires before handing a connection over, so the
// core treats every connection it accepts as already authorized. The Unix
// transport leans on socket filesystem permissions; a network transport would
// authenticate while establishing the connection.
//
// DIVERGENCE (deliberate): pi's legacy/ tree (process supervision, CLI, OAuth)
// is not ported. It is Node process machinery rather than protocol behaviour,
// and the owner ruled it out of scope (docs/UPSTREAM.md, 2026-08-01).
package server

import (
	"context"

	"github.com/sky-valley/pi/protocol"
)

// Unsubscribe cancels a subscription. It is safe to call more than once.
type Unsubscribe func()

// CreateSessionOptions is what a Service needs to create one durable session.
//
// The pointer fields are the client's optionals: nil means "the client did not
// ask", which is not the same as an explicit empty string. pi distinguishes
// those with `undefined`; Go needs the pointer to keep the distinction.
type CreateSessionOptions struct {
	// ID is a collision-resistant ID assigned by the Server. The service must
	// persist this exact ID.
	ID            string
	Cwd           *string
	Name          *string
	Model         *protocol.ModelRef
	ThinkingLevel *protocol.ThinkingLevel
}

// PromptInput is a prompt command with its routing fields removed.
type PromptInput struct{ Text string }

// SteerInput is a steer command with its routing fields removed.
type SteerInput struct{ Text string }

// RuntimeEventType discriminates a RuntimeEvent.
type RuntimeEventType string

const (
	// RuntimeSnapshotEvent says the session's snapshot changed and should be
	// re-read and rebroadcast.
	RuntimeSnapshotEvent RuntimeEventType = "snapshot"
	// RuntimeProgressEvent carries incremental transcript activity.
	RuntimeProgressEvent RuntimeEventType = "progress"
	// RuntimeErrorEvent is terminal: the runtime has lost the session and every
	// attached connection is disconnected.
	RuntimeErrorEvent RuntimeEventType = "error"
)

// RuntimeEvent is one notification from a SessionRuntime.
//
// DIVERGENCE (deliberate): pi models this as a discriminated union of three
// object shapes. One struct with a Type tag keeps the payloads on the same
// value, which is how a Go caller wants to switch on it; Progress is set only
// for RuntimeProgressEvent and Err only for RuntimeErrorEvent.
type RuntimeEvent struct {
	Type     RuntimeEventType
	Progress protocol.TranscriptProgress
	Err      *Error
}

// SessionRuntime is one acquired durable session.
//
// Conflicting operations must fail rather than queue: a Prompt issued while a
// turn is running returns a busy Error, it does not wait. The Server relies on
// that — it never serialises operations itself.
//
// Every method may be called concurrently from several connections.
type SessionRuntime interface {
	// Snapshot returns the session's current authoritative view.
	Snapshot(ctx context.Context) (*protocol.SessionSnapshot, error)
	// Phase reports what the session is doing right now. The Server calls it
	// often and expects it to be cheap and non-blocking.
	Phase() protocol.SessionPhase
	Prompt(ctx context.Context, input PromptInput) error
	Steer(ctx context.Context, input SteerInput) error
	Abort(ctx context.Context) error
	SetModel(ctx context.Context, model protocol.ModelRef) error
	SetThinking(ctx context.Context, level protocol.ThinkingLevel) error
	// Subscribe registers an event listener and returns its canceller.
	//
	// The Server dispatches snapshot and error events on its own goroutine, so
	// a runtime may emit those while holding its own lock. Progress events are
	// forwarded to the connections attached at the moment of the emit, on the
	// emitting goroutine — that is what keeps a delta ahead of a snapshot
	// emitted before it, which a client applying deltas depends on. It also
	// means a runtime must not hold a lock the Server needs while emitting
	// progress: a send that fails there disconnects the connection, and that
	// path calls back into Phase.
	Subscribe(listener func(RuntimeEvent)) Unsubscribe
	// Dispose releases the runtime and whatever lock it holds on durable
	// storage. The Server calls it exactly once per acquired runtime.
	Dispose(ctx context.Context) error
}

// Service is the boundary for durable sessions and the exclusively acquired
// runtimes in front of them.
type Service interface {
	ListSessions(ctx context.Context) ([]protocol.SessionMetadata, error)
	ListModels(ctx context.Context) ([]protocol.ModelMetadata, error)
	// CreateSession creates a session under the server-assigned options.ID. A
	// runtime whose snapshot reports a different ID is rejected and disposed.
	CreateSession(ctx context.Context, options CreateSessionOptions) (SessionRuntime, error)
	// OpenSession acquires an existing session, failing with a session_locked
	// Error when another holder owns it.
	OpenSession(ctx context.Context, sessionID string) (SessionRuntime, error)
}
