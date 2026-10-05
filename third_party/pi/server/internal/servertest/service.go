// Package servertest is an in-memory Service and a raw protocol client, used
// to exercise the server package and its transports.
//
// It is the Go counterpart of pi's packages/server/src/testing tree. That tree
// is published upstream; this one is internal, because the Go port has no
// reason to make a fake service part of its public API — the tests that need it
// all live under server/.
package servertest

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/sky-valley/pi/protocol"
	"github.com/sky-valley/pi/server"
)

// Model is the single model the fake service publishes.
var Model = protocol.ModelMetadata{
	Provider:                "test",
	ID:                      "small",
	Name:                    "Test Small",
	API:                     "test-api",
	Reasoning:               true,
	Input:                   []protocol.ModelInputModality{protocol.InputText, protocol.InputImage},
	ContextWindow:           16_000,
	MaxTokens:               2_000,
	Cost:                    protocol.ModelCost{},
	SupportedThinkingLevels: []protocol.ThinkingLevel{protocol.ThinkingOff, protocol.ThinkingMedium, protocol.ThinkingHigh},
	Authenticated:           true,
}

// ModelRef points at Model.
func ModelRef() protocol.ModelRef {
	return protocol.ModelRef{Provider: Model.Provider, ID: Model.ID}
}

// Gate is a one-shot rendezvous: a caller reaching it announces itself on
// Entered and blocks until Release is closed.
type Gate struct {
	Entered chan struct{}
	Release chan struct{}

	once sync.Once
}

// NewGate returns an unentered, unreleased gate.
func NewGate() *Gate {
	return &Gate{Entered: make(chan struct{}), Release: make(chan struct{})}
}

func (g *Gate) enter() {
	g.once.Do(func() { close(g.Entered) })
	<-g.Release
}

// Open releases everyone waiting at the gate.
func (g *Gate) Open() { close(g.Release) }

type storedSession struct {
	mu       sync.Mutex
	snapshot protocol.SessionSnapshot
}

func (s *storedSession) get() protocol.SessionSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneSnapshot(s.snapshot)
}

func cloneSnapshot(snapshot protocol.SessionSnapshot) protocol.SessionSnapshot {
	clone := snapshot
	clone.Transcript = append([]protocol.TranscriptItem(nil), snapshot.Transcript...)
	clone.QueuedSteer = append([]protocol.UserTranscriptItem(nil), snapshot.QueuedSteer...)
	return clone
}

// Service is an in-memory server.Service that records every runtime it hands
// out, so a test can inspect what the server did with them.
type Service struct {
	mu             sync.Mutex
	sessions       map[string]*storedSession
	order          []string
	runtimes       map[string][]*Runtime
	locked         map[string]bool
	lastCreatedID  string
	lastAssignedID string
	listGate       *Gate

	listSessionsHook func(call int) error
	listModelsHook   func(call int) error
	createIDOverride string
	listCalls        int
	modelCalls       int
}

// SetListSessionsHook installs a hook that runs at the top of ListSessions;
// returning an error fails the call. It is safe to call on a running server.
func (s *Service) SetListSessionsHook(hook func(call int) error) {
	s.mu.Lock()
	s.listSessionsHook = hook
	s.mu.Unlock()
}

// SetListModelsHook installs a hook that runs at the top of ListModels.
func (s *Service) SetListModelsHook(hook func(call int) error) {
	s.mu.Lock()
	s.listModelsHook = hook
	s.mu.Unlock()
}

// SetCreateSessionIDOverride rewrites the ID the service persists, which is how
// a test makes a service disobey its server-assigned ID.
func (s *Service) SetCreateSessionIDOverride(id string) {
	s.mu.Lock()
	s.createIDOverride = id
	s.mu.Unlock()
}

var _ server.Service = (*Service)(nil)

// NewService returns an empty service.
func NewService() *Service {
	return &Service{
		sessions: map[string]*storedSession{},
		runtimes: map[string][]*Runtime{},
		locked:   map[string]bool{},
	}
}

// Seed stores one idle session with default fields.
func (s *Service) Seed(id string) {
	if id == "" {
		id = "session-1"
	}
	name := "Session " + id
	s.SeedWith(id, &name, "/tmp/pi-server-conformance", ModelRef(), protocol.ThinkingOff)
}

// SeedWith stores one idle session with explicit fields.
func (s *Service) SeedWith(
	id string,
	name *string,
	cwd string,
	model protocol.ModelRef,
	thinkingLevel protocol.ThinkingLevel,
) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[id]; !ok {
		s.order = append(s.order, id)
	}
	s.sessions[id] = &storedSession{snapshot: protocol.SessionSnapshot{
		ID:            id,
		Name:          name,
		Cwd:           cwd,
		CreatedAt:     1,
		UpdatedAt:     1,
		Phase:         protocol.PhaseIdle,
		Model:         model,
		ThinkingLevel: thinkingLevel,
	}}
}

// DelayNextList makes the next ListSessions block at the returned gate.
func (s *Service) DelayNextList() *Gate {
	gate := NewGate()
	s.mu.Lock()
	s.listGate = gate
	s.mu.Unlock()
	return gate
}

// ForceLock marks a session held by somebody the server cannot see, which is
// how a test provokes a session_locked failure from OpenSession.
func (s *Service) ForceLock(id string) {
	s.mu.Lock()
	s.locked[id] = true
	s.mu.Unlock()
}

// Locked reports whether the service still considers a session held.
func (s *Service) Locked(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.locked[id]
}

// LastCreatedID is the ID the most recent CreateSession actually persisted,
// which is the override when SetCreateSessionIDOverride is in force.
func (s *Service) LastCreatedID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastCreatedID
}

// LastAssignedID is the ID the server assigned to the most recent
// CreateSession, before any override was applied.
func (s *Service) LastAssignedID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastAssignedID
}

// Runtimes is every runtime ever handed out for a session, oldest first.
func (s *Service) Runtimes(id string) []*Runtime {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*Runtime(nil), s.runtimes[id]...)
}

// LatestRuntime is the most recent runtime handed out for a session.
func (s *Service) LatestRuntime(id string) *Runtime {
	runtimes := s.Runtimes(id)
	if len(runtimes) == 0 {
		return nil
	}
	return runtimes[len(runtimes)-1]
}

func (s *Service) ListSessions(context.Context) ([]protocol.SessionMetadata, error) {
	s.mu.Lock()
	gate := s.listGate
	s.listGate = nil
	s.listCalls++
	call := s.listCalls
	hook := s.listSessionsHook
	s.mu.Unlock()

	if hook != nil {
		if err := hook(call); err != nil {
			return nil, err
		}
	}
	if gate != nil {
		gate.enter()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	metadata := make([]protocol.SessionMetadata, 0, len(s.order))
	for _, id := range s.order {
		stored := s.sessions[id]
		if stored == nil {
			continue
		}
		// Since 6189e53b3 the stored view is durable metadata only: the live
		// fields the fake used to report here (attached, locked) are no longer
		// part of the listed shape.
		snapshot := stored.get()
		metadata = append(metadata, snapshot.Metadata())
	}
	return metadata, nil
}

func (s *Service) ListModels(context.Context) ([]protocol.ModelMetadata, error) {
	s.mu.Lock()
	s.modelCalls++
	call := s.modelCalls
	hook := s.listModelsHook
	s.mu.Unlock()
	if hook != nil {
		if err := hook(call); err != nil {
			return nil, err
		}
	}
	return []protocol.ModelMetadata{Model}, nil
}

func (s *Service) CreateSession(_ context.Context, options server.CreateSessionOptions) (server.SessionRuntime, error) {
	s.mu.Lock()
	id := options.ID
	s.lastAssignedID = options.ID
	if s.createIDOverride != "" {
		id = s.createIDOverride
	}
	s.lastCreatedID = id
	_, exists := s.sessions[id]
	s.mu.Unlock()
	if exists {
		return nil, server.NewLockedError("Session already exists", nil)
	}

	cwd := "/tmp/pi-server-conformance"
	if options.Cwd != nil {
		cwd = *options.Cwd
	}
	model := ModelRef()
	if options.Model != nil {
		model = *options.Model
	}
	thinkingLevel := protocol.ThinkingOff
	if options.ThinkingLevel != nil {
		thinkingLevel = *options.ThinkingLevel
	}
	name := options.Name
	if name == nil {
		fallback := "Session " + id
		name = &fallback
	}
	s.SeedWith(id, name, cwd, model, thinkingLevel)
	return s.acquire(id)
}

func (s *Service) OpenSession(_ context.Context, sessionID string) (server.SessionRuntime, error) {
	s.mu.Lock()
	_, exists := s.sessions[sessionID]
	locked := s.locked[sessionID]
	s.mu.Unlock()
	if !exists {
		return nil, server.NewNotFoundError("Unknown session: "+sessionID, nil)
	}
	if locked {
		return nil, server.NewLockedError("Session is locked: "+sessionID, nil)
	}
	return s.acquire(sessionID)
}

func (s *Service) acquire(id string) (*Runtime, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored := s.sessions[id]
	if stored == nil {
		return nil, fmt.Errorf("unknown session: %s", id)
	}
	s.locked[id] = true
	runtime := &Runtime{
		service:   s,
		stored:    stored,
		id:        id,
		listeners: map[int]func(server.RuntimeEvent){},
		disposed:  make(chan struct{}),
	}
	s.runtimes[id] = append(s.runtimes[id], runtime)
	return runtime, nil
}

func (s *Service) release(id string) {
	s.mu.Lock()
	delete(s.locked, id)
	s.mu.Unlock()
}

type promptOutcome int

const (
	promptCompleted promptOutcome = iota
	promptAborted
)

// Runtime is one acquired fake session.
type Runtime struct {
	service *Service
	stored  *storedSession
	id      string

	mu           sync.Mutex
	listeners    map[int]func(server.RuntimeEvent)
	nextListener int
	disposeCount int
	steers       []server.SteerInput
	pending      chan promptOutcome
	snapshotGate chan struct{}

	disposed  chan struct{}
	closeOnce sync.Once
}

var _ server.SessionRuntime = (*Runtime)(nil)

// Disposed is closed after the first Dispose.
func (r *Runtime) Disposed() <-chan struct{} { return r.disposed }

// DisposeCount is how many times the server released this runtime.
func (r *Runtime) DisposeCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.disposeCount
}

// Steers is every steer this runtime accepted.
func (r *Runtime) Steers() []server.SteerInput {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]server.SteerInput(nil), r.steers...)
}

func (r *Runtime) Snapshot(context.Context) (*protocol.SessionSnapshot, error) {
	r.mu.Lock()
	gate := r.snapshotGate
	r.mu.Unlock()
	if gate != nil {
		<-gate
	}
	snapshot := r.stored.get()
	return &snapshot, nil
}

// BlockSnapshots holds every Snapshot call open until ReleaseSnapshots. It is
// how a test keeps one runtime event in flight while it provokes another.
func (r *Runtime) BlockSnapshots() {
	r.mu.Lock()
	if r.snapshotGate == nil {
		r.snapshotGate = make(chan struct{})
	}
	r.mu.Unlock()
}

// ReleaseSnapshots lets the blocked Snapshot calls, and every later one,
// through.
func (r *Runtime) ReleaseSnapshots() {
	r.mu.Lock()
	gate := r.snapshotGate
	r.snapshotGate = nil
	r.mu.Unlock()
	if gate != nil {
		close(gate)
	}
}

// StoredSnapshot is the runtime's current view, for assertions.
func (r *Runtime) StoredSnapshot() protocol.SessionSnapshot { return r.stored.get() }

func (r *Runtime) Phase() protocol.SessionPhase { return r.stored.get().Phase }

func (r *Runtime) Prompt(ctx context.Context, input server.PromptInput) error {
	if r.Phase() != protocol.PhaseIdle {
		return server.NewBusyError("A prompt is already running", nil)
	}
	done := make(chan promptOutcome, 1)
	r.mu.Lock()
	r.pending = done
	r.mu.Unlock()

	r.update(func(snapshot *protocol.SessionSnapshot) {
		snapshot.Phase = protocol.PhaseTurn
		snapshot.Transcript = append(snapshot.Transcript, &protocol.UserTranscriptItem{
			ID:        fmt.Sprintf("user-%d", snapshot.Revision+1),
			Role:      "user",
			Content:   []protocol.Content{&protocol.TextContent{Type: "text", Text: input.Text}},
			Timestamp: snapshot.Revision + 1,
		})
	})

	var outcome promptOutcome
	select {
	case outcome = <-done:
	case <-ctx.Done():
		return ctx.Err()
	}

	r.update(func(snapshot *protocol.SessionSnapshot) {
		snapshot.Phase = protocol.PhaseIdle
		item := &protocol.AssistantTranscriptItem{
			ID:        fmt.Sprintf("assistant-%d", snapshot.Revision+1),
			Role:      "assistant",
			Model:     snapshot.Model,
			Timestamp: snapshot.Revision + 1,
		}
		if outcome == promptCompleted {
			stop := protocol.StopStop
			item.Content = []protocol.Content{&protocol.TextContent{Type: "text", Text: "reply:" + input.Text}}
			item.Status = protocol.AssistantComplete
			item.StopReason = &stop
		} else {
			aborted := protocol.StopAborted
			item.Content = []protocol.Content{&protocol.TextContent{Type: "text", Text: ""}}
			item.Status = protocol.AssistantAborted
			item.StopReason = &aborted
		}
		snapshot.Transcript = append(snapshot.Transcript, item)
	})

	r.mu.Lock()
	r.pending = nil
	r.mu.Unlock()
	return nil
}

func (r *Runtime) Steer(_ context.Context, input server.SteerInput) error {
	if r.Phase() == protocol.PhaseIdle {
		return server.NewBusyError("There is no active prompt to steer", nil)
	}
	r.mu.Lock()
	r.steers = append(r.steers, input)
	r.mu.Unlock()
	r.update(func(snapshot *protocol.SessionSnapshot) {
		snapshot.QueuedSteerCount++
		snapshot.QueuedSteer = append(snapshot.QueuedSteer, protocol.UserTranscriptItem{
			ID:        fmt.Sprintf("steer-%d", snapshot.Revision+1),
			Role:      "user",
			Content:   []protocol.Content{&protocol.TextContent{Type: "text", Text: input.Text}},
			Timestamp: snapshot.Revision + 1,
		})
	})
	return nil
}

func (r *Runtime) Abort(context.Context) error {
	r.mu.Lock()
	pending := r.pending
	r.mu.Unlock()
	if pending == nil {
		return server.NewBusyError("There is no active prompt to abort", nil)
	}
	select {
	case pending <- promptAborted:
	default:
	}
	return nil
}

func (r *Runtime) SetModel(_ context.Context, model protocol.ModelRef) error {
	if r.Phase() != protocol.PhaseIdle {
		return server.NewBusyError("", nil)
	}
	r.update(func(snapshot *protocol.SessionSnapshot) { snapshot.Model = model })
	return nil
}

func (r *Runtime) SetThinking(_ context.Context, level protocol.ThinkingLevel) error {
	if r.Phase() != protocol.PhaseIdle {
		return server.NewBusyError("", nil)
	}
	r.update(func(snapshot *protocol.SessionSnapshot) { snapshot.ThinkingLevel = level })
	return nil
}

func (r *Runtime) Subscribe(listener func(server.RuntimeEvent)) server.Unsubscribe {
	r.mu.Lock()
	r.nextListener++
	id := r.nextListener
	r.listeners[id] = listener
	r.mu.Unlock()
	return func() {
		r.mu.Lock()
		delete(r.listeners, id)
		r.mu.Unlock()
	}
}

func (r *Runtime) Dispose(context.Context) error {
	r.mu.Lock()
	r.disposeCount++
	r.mu.Unlock()
	r.service.release(r.id)
	r.closeOnce.Do(func() { close(r.disposed) })
	return nil
}

// SetPhase forces the session's phase without emitting anything.
func (r *Runtime) SetPhase(phase protocol.SessionPhase) {
	r.stored.mu.Lock()
	r.stored.snapshot.Phase = phase
	r.stored.mu.Unlock()
}

// FinishPrompt completes the in-flight prompt.
func (r *Runtime) FinishPrompt() error {
	r.mu.Lock()
	pending := r.pending
	r.mu.Unlock()
	if pending == nil {
		return errors.New("no prompt is pending")
	}
	select {
	case pending <- promptCompleted:
	default:
	}
	return nil
}

// EmitProgress publishes one progress event.
func (r *Runtime) EmitProgress(progress protocol.TranscriptProgress) {
	r.emit(server.RuntimeEvent{Type: server.RuntimeProgressEvent, Progress: progress})
}

// EmitError publishes a terminal runtime failure.
func (r *Runtime) EmitError(err *server.Error) {
	r.emit(server.RuntimeEvent{Type: server.RuntimeErrorEvent, Err: err})
}

// EmitSnapshot tells the server the snapshot changed.
func (r *Runtime) EmitSnapshot() {
	r.emit(server.RuntimeEvent{Type: server.RuntimeSnapshotEvent})
}

func (r *Runtime) emit(event server.RuntimeEvent) {
	r.mu.Lock()
	listeners := make([]func(server.RuntimeEvent), 0, len(r.listeners))
	for _, listener := range r.listeners {
		listeners = append(listeners, listener)
	}
	r.mu.Unlock()
	for _, listener := range listeners {
		listener(event)
	}
}

func (r *Runtime) update(mutate func(*protocol.SessionSnapshot)) {
	r.stored.mu.Lock()
	snapshot := cloneSnapshot(r.stored.snapshot)
	mutate(&snapshot)
	snapshot.Revision++
	snapshot.UpdatedAt++
	r.stored.snapshot = snapshot
	r.stored.mu.Unlock()
	r.EmitSnapshot()
}
