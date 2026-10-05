package client

import (
	"context"

	"github.com/sky-valley/pi/protocol"
)

// SessionLeaseMode is how strongly a lease claims its session.
type SessionLeaseMode string

const (
	// LeaseShared allows other shared leases on the same session; the server
	// is detached only when the last of them is released.
	LeaseShared SessionLeaseMode = "shared"
	// LeaseExclusive refuses to coexist with any other lease on the session.
	LeaseExclusive SessionLeaseMode = "exclusive"
)

// leaseController is everything a SessionHandle needs from the client that
// issued it. It exists so session_handle.go stays a thin, testable projection
// of one session, exactly as pi's SessionHandle/SessionHandleCallbacks split
// does — the lease bookkeeping lives in client.go.
type leaseController interface {
	isActive() bool
	sessionSnapshot() *protocol.SessionSnapshot
	subscribe(listener func(*protocol.SessionSnapshot)) (Unsubscribe, error)
	onEvent(listener func(protocol.ServerEvent)) (Unsubscribe, error)
	detach(ctx context.Context) error
	close(ctx context.Context) error
	request(ctx context.Context, command protocol.Command) (protocol.CommandResult, error)
}

// SessionHandle is one lease on one remote session: a scoped view of the
// session's snapshot and events, plus the commands that act on it. It is the
// port of pi's SessionHandle (packages/client/src/session-handle.ts).
//
// A handle stops working the moment its lease ends — released explicitly, or
// invalidated because the session was removed or the connection dropped. Every
// method reports that as a SessionDetachedError rather than acting on a session
// this client no longer holds.
type SessionHandle struct {
	id         string
	controller leaseController
}

// newSessionHandle binds a handle to its lease.
func newSessionHandle(id string, controller leaseController) *SessionHandle {
	return &SessionHandle{id: id, controller: controller}
}

// ID is the session's server-assigned identifier.
func (h *SessionHandle) ID() string { return h.id }

// Attached reports whether this lease still holds an attached session.
func (h *SessionHandle) Attached() bool { return h.controller.isActive() }

// Active is Attached under pi's other name for it; the two are the same
// condition and both are kept so ported call sites read unchanged.
func (h *SessionHandle) Active() bool { return h.Attached() }

// Snapshot returns the cached session snapshot, or nil if the lease is no
// longer active or nothing has been cached yet. It is the cached pointer, not a
// copy, and is shared with every other holder and subscriber: treat it as
// read-only, and expect new state to arrive as a replacement rather than as an
// edit to this one.
func (h *SessionHandle) Snapshot() *protocol.SessionSnapshot {
	return h.controller.sessionSnapshot()
}

// Subscribe registers a listener for this session's snapshots. The listener
// stops being called once the lease ends.
func (h *SessionHandle) Subscribe(listener func(*protocol.SessionSnapshot)) (Unsubscribe, error) {
	return h.controller.subscribe(listener)
}

// OnEvent registers a listener for this session's events. Removal is delivered
// even though it ends the lease, so a subscriber learns why it went quiet.
func (h *SessionHandle) OnEvent(listener func(protocol.ServerEvent)) (Unsubscribe, error) {
	return h.controller.onEvent(listener)
}

// Detach releases this lease, detaching from the server when it was the last
// one. A failed detach leaves the lease usable so the caller can retry.
func (h *SessionHandle) Detach(ctx context.Context) error {
	return h.controller.detach(ctx)
}

// Close releases this lease and gives it up even if the detach failed, marking
// the session for cleanup on the next acquisition. It is pi's dispose(), named
// for Go's io.Closer convention, and is safe to call more than once.
func (h *SessionHandle) Close(ctx context.Context) error {
	return h.controller.close(ctx)
}

// Prompt submits a new user turn and returns the resulting session snapshot.
func (h *SessionHandle) Prompt(ctx context.Context, text string) (*protocol.SessionSnapshot, error) {
	return h.sessionRequest(ctx, protocol.NewPromptCommand(h.id, text))
}

// Steer injects text into the running turn.
func (h *SessionHandle) Steer(ctx context.Context, text string) (*protocol.SessionSnapshot, error) {
	return h.sessionRequest(ctx, protocol.NewSteerCommand(h.id, text))
}

// Abort stops the active turn.
func (h *SessionHandle) Abort(ctx context.Context) (*protocol.SessionSnapshot, error) {
	return h.sessionRequest(ctx, protocol.NewAbortCommand(h.id))
}

// SetModel switches the session's model.
func (h *SessionHandle) SetModel(ctx context.Context, model protocol.ModelRef) (*protocol.SessionSnapshot, error) {
	return h.sessionRequest(ctx, protocol.NewSetModelCommand(h.id, model))
}

// SetThinking switches the session's thinking level.
func (h *SessionHandle) SetThinking(
	ctx context.Context,
	level protocol.ThinkingLevel,
) (*protocol.SessionSnapshot, error) {
	return h.sessionRequest(ctx, protocol.NewSetThinkingCommand(h.id, level))
}

// sessionRequest issues a session-scoped command and unwraps its snapshot.
//
// Every command routed through here answers with a SessionResult; anything else
// is the server contradicting the protocol, which the response correlator has
// already rejected — so a mismatch here can only be a bug in this package and
// is surfaced as a validation error rather than a nil dereference.
func (h *SessionHandle) sessionRequest(
	ctx context.Context,
	command protocol.Command,
) (*protocol.SessionSnapshot, error) {
	result, err := h.controller.request(ctx, command)
	if err != nil {
		return nil, err
	}
	session, ok := result.(*protocol.SessionResult)
	if !ok {
		return nil, &protocol.ValidationError{
			Msg: "Response command " + result.ResultCommandName() + " does not carry a session",
		}
	}
	snapshot := session.Session
	return &snapshot, nil
}
