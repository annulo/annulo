package delta

import (
	"errors"
	"weak"
)

// ─── Immutable tracking ──────────────────────────────────────────────────────
//
// Upstream's delta/tracker.ts. A tracker holds one committed, immutable
// revision. A change opens an overlay draft on it; preparing the change emits
// the ops the overlay recorded and materializes the revision they produce;
// adopting the prepared change commits it by swapping the root. Nothing is
// published until the caller adopts — which is what lets a replicated-state
// runtime commit, persist or discard a change as one unit.
//
// Roots are trusted. Track and PrepareReplace take ownership of the value they
// are handed without walking or copying it: it must be alias-free strict JSON
// in the representation chord.CopyJSON makes (nil, bool, a finite float64,
// string, []any and map[string]any, none of them a nil map or slice, no
// container reachable twice), and the caller must never modify it again. Only
// what a draft is handed is checked and copied.

// The lifecycle errors, with upstream's text. Adopt checks them in this order:
// owner, used, aborted, stale.
var (
	ErrChangeSettled   = errors.New("delta: Change has already been settled (it was prepared or aborted; begin a new change with Tracker.BeginChange)")
	ErrForeignPrepared = errors.New("delta: Prepared change belongs to a different tracker (adopt it on the tracker that prepared it)")
	ErrPreparedUsed    = errors.New("delta: Prepared change has already been used (each prepared change is adopted at most once; prepare the next one from Tracker.Value)")
	ErrPreparedAborted = errors.New("delta: Prepared change has been aborted (it, or the change that prepared it, was aborted; prepare it again)")
	ErrStalePrepared   = errors.New("delta: Prepared change is stale (the tracker adopted another revision after it was prepared; prepare it again against Tracker.Value)")

	// errNotReady is upstream's refusal of a prepared change whose context is
	// neither prepared nor settled, which no Go caller can hand Adopt.
	errNotReady = errors.New("delta: Prepared change is not ready (prepare it with Change.Prepare or Tracker.PrepareReplace)")

	// ErrScalarRevision is the refusal of a scalar root, with the text of the
	// TypeError pi's overlay throws: it keys its nodes by their containers in a
	// WeakMap, which takes only objects. A change drafts an object or array,
	// and PrepareReplace takes one.
	ErrScalarRevision = errors.New("delta: Invalid value used as weak map key (a change drafts a JSON object or array, a map[string]any or []any, and a replacement is one; this revision or value is a scalar, null — a nil map or slice among them — or a Go type the tracker does not hold)")

	// ErrZeroTracker is Go's own: a Tracker that Track did not make holds no
	// revision, so BeginChange, PrepareReplace and Adopt refuse it.
	ErrZeroTracker = errors.New("delta: Tracker holds no revision (it is a zero Tracker; make one with Track)")
)

type status uint8

const (
	statusOpen status = iota
	statusPrepared
	statusConsumed
	statusAborted
	statusStale
)

// statusCell is a context's status, shared with the Change that prepared it
// so that aborting the change after Prepare reaches the prepared change
// without the Change retaining its context.
type statusCell struct{ value status }

// owner is a tracker's identity as its contexts record it, and the registry
// of its open changes. It is not the tracker itself, so that a retained change
// or prepared change does not retain the tracker or its revision (upstream's
// `#owner = {}` and its WeakRef'd tracker). The registry holds its contexts
// weakly: a change the caller drops is not kept alive by the tracker.
type owner struct {
	contexts    map[weak.Pointer[overlayContext]]struct{}
	pruneBudget int
}

func (o *owner) register(c *overlayContext) {
	c.registration = weak.Make(c)
	c.registered = true
	o.contexts[c.registration] = struct{}{}
	o.pruneBudget--
	if o.pruneBudget == 0 {
		for p := range o.contexts {
			if p.Value() == nil {
				delete(o.contexts, p)
			}
		}
		o.pruneBudget = max(256, len(o.contexts))
	}
}

func (o *owner) release(c *overlayContext) {
	if c.registered {
		delete(o.contexts, c.registration)
		c.registered = false
	}
}

// invalidate is upstream's: adopting one change makes every other change open
// on the tracker stale, and drops what their overlays hold. A change prepared
// before is not registered; Adopt finds it stale by its revision.
func (o *owner) invalidate(winner *overlayContext) {
	for p := range o.contexts {
		c := p.Value()
		if c == nil || c == winner {
			continue
		}
		if c.status.value == statusOpen || c.status.value == statusPrepared {
			c.status.value = statusStale
		}
		c.release()
	}
	clear(o.contexts)
	o.pruneBudget = 256
}

// overlayContext is upstream's OverlayContext: one change's overlay, or one
// replacement's.
type overlayContext struct {
	owner        *owner
	baseRevision int
	status       *statusCell
	root         *node
	baseValue    any
	dirty        []*node
	nodes        []*node
	// placed finds the node of a container this change placed, by identity:
	// upstream's rawNodes, for the containers a draft copied in. Containers
	// read from the revision are found by their slot (node.keyChildren,
	// node.indexChildren), which needs no identity: an empty []any that
	// encoding/json decoded has none of its own (value.go, anonymous).
	placed      map[identity]*node
	ops         []Op
	opsReady    bool
	replacement bool
	// released is upstream's overlayReleased: the overlay is gone, and so is
	// every use of its drafts.
	released bool
	// simpleObjectMaterialization is set when every edit was to objects below
	// objects, so the prepared value is the overlay copied, not the base with
	// the ops applied.
	simpleObjectMaterialization bool
	registration                weak.Pointer[overlayContext]
	registered                  bool
}

func newContext(o *owner, baseRevision int, root any, replacement bool, base any) *overlayContext {
	c := &overlayContext{
		owner:        o,
		baseRevision: baseRevision,
		status:       &statusCell{},
		baseValue:    base,
		replacement:  replacement,
		placed:       map[identity]*node{},
	}
	c.root = c.newNode(root, nil, slot{}, false)
	return c
}

// newNode is upstream's createNode, the lookup aside (node.childFor).
func (c *overlayContext) newNode(base any, parent *node, s slot, placement bool) *node {
	n := &node{ctx: c, base: base, parent: parent, kind: s.kind, key: s.key, index: s.index, source: s.source, placement: placement}
	n.draft = &Draft{n: n}
	c.nodes = append(c.nodes, n)
	return n
}

// settled is upstream's isSettledContext: no draft of the context may be used.
func (c *overlayContext) settled() bool {
	switch c.status.value {
	case statusConsumed, statusAborted, statusStale:
		return true
	}
	return c.released
}

// abort is upstream's abortContext.
func (c *overlayContext) abort() {
	switch c.status.value {
	case statusAborted, statusConsumed, statusStale:
		return
	}
	c.status.value = statusAborted
	c.release()
}

// release is upstream's clearContext and releaseOverlayReferences: the
// context leaves the registry and drops its overlay, node by node, so that a
// draft handle the caller keeps retains neither the revision nor what the
// change placed.
func (c *overlayContext) release() {
	c.owner.release(c)
	for _, n := range c.nodes {
		*n = node{ctx: c, draft: n.draft}
	}
	c.dirty, c.nodes, c.placed = nil, nil, nil
	c.ops, c.root, c.baseValue = nil, nil, nil
	c.released = true
}

// draftable reports whether a root can have an overlay: an object or array —
// not a scalar, and not a nil map or slice, which is JSON null (encoding/json
// writes it so, and a base batch carries it so), as pi's overlay refuses a
// null root.
func draftable(root any) bool {
	switch x := root.(type) {
	case map[string]any:
		return x != nil
	case []any:
		return x != nil
	}
	return false
}

// Tracker holds one committed revision of a JSON value and turns changes to it
// into batches of ops. T is the root's Go type: map[string]any, []any, or any
// when it is not known.
//
// The committed revision is immutable: Value, and the Base, Value and Ops of
// every Prepared, share its containers, and none of them may be modified.
// Change it through BeginChange's draft, or replace it with PrepareReplace;
// either way the result takes effect only when adopted.
//
// Several changes may be open or prepared against one revision at a time;
// adopting one makes every other stale. A Tracker is not safe for concurrent
// use. The zero Tracker holds no revision and refuses every change with
// ErrZeroTracker; make one with Track.
type Tracker[T any] struct {
	owner    *owner
	value    T
	revision int
}

// Track takes ownership of initial as the first committed revision, in O(1):
// it is neither walked nor copied, so it must be alias-free strict JSON in the
// representation encoding/json decodes — nil, bool, a finite float64, string,
// []any and map[string]any, with no container reachable twice — and the
// caller must never modify it again. A nil map or slice is not an empty one
// here: encoding/json writes it as null, which a replica cannot hold members
// of. A revision that breaks the contract has unspecified behavior, in pi as
// here; make one from any Go value with chord.CopyJSON, whose copy is exactly
// this representation (chord.IsValue accepts more: aliases, other numeric
// kinds, typed containers).
func Track[T any](initial T) *Tracker[T] {
	return &Tracker[T]{owner: &owner{contexts: map[weak.Pointer[overlayContext]]struct{}{}, pruneBudget: 256}, value: initial}
}

// Value is the committed revision.
func (t *Tracker[T]) Value() T { return t.value }

// Revision counts the changes adopted: 0 for the initial revision. A no-op
// counts too.
func (t *Tracker[T]) Revision() int { return t.revision }

// BeginChange opens a change: an overlay draft of the committed revision that
// the caller mutates, across any amount of work, then prepares or aborts. It
// never modifies the committed revision. A scalar or null revision — a nil
// map or slice is null — has no draft: ErrScalarRevision.
func (t *Tracker[T]) BeginChange() (*Change[T], error) {
	if t.owner == nil {
		return nil, ErrZeroTracker
	}
	if !draftable(any(t.value)) {
		return nil, ErrScalarRevision
	}
	c := newContext(t.owner, t.revision, any(t.value), false, any(t.value))
	t.owner.register(c)
	return &Change[T]{ctx: c, root: c.root.draft}, nil
}

// PrepareReplace prepares replacing the committed revision with value, whole:
// taking ownership of it, on Track's terms (build it with chord.CopyJSON). When value is deeply equal to the
// committed revision the ops are empty and the prepared Value is the committed
// revision itself; otherwise they are one Replace, and the prepared Value is
// value. value must be an object or array, not nil: ErrScalarRevision.
func (t *Tracker[T]) PrepareReplace(value T) (*Prepared[T], error) {
	if t.owner == nil {
		return nil, ErrZeroTracker
	}
	if !draftable(any(value)) {
		return nil, ErrScalarRevision
	}
	c := newContext(t.owner, t.revision, any(value), true, any(t.value))
	c.status.value = statusPrepared
	t.owner.register(c)
	p, err := materializePrepared[T](c)
	if err != nil {
		c.status.value = statusAborted
		c.release()
		return nil, err
	}
	return p, nil
}

// Adopt commits a prepared change: an infallible swap of the root, since the
// revision was materialized when it was prepared. It must come from this
// tracker, be neither used nor aborted, and have been prepared against the
// committed revision. Adopting a change makes every other open or prepared
// change of the tracker stale. A nil or zero Prepared belongs to no tracker.
func (t *Tracker[T]) Adopt(p *Prepared[T]) error {
	if t.owner == nil {
		return ErrZeroTracker
	}
	if p == nil || p.ctx == nil || p.ctx.owner != t.owner {
		return ErrForeignPrepared
	}
	c := p.ctx
	switch c.status.value {
	case statusConsumed:
		return ErrPreparedUsed
	case statusAborted:
		return ErrPreparedAborted
	case statusStale:
		return ErrStalePrepared
	case statusPrepared:
	default:
		return errNotReady
	}
	if c.baseRevision != t.revision {
		c.status.value = statusStale
		c.release()
		return ErrStalePrepared
	}
	// Upstream also refuses a base that is not the committed value; with the
	// revisions equal it always is (only Adopt moves either), and Go cannot
	// compare an empty []any with no capacity by identity (value.go).
	t.value = p.value
	c.status.value = statusConsumed
	t.revision++
	t.owner.invalidate(c)
	return nil
}

// materializePrepared is upstream's: the ops, and the revision they produce —
// the base itself for no ops, the overlay copied when only objects changed,
// and otherwise the base with the ops applied. The overlay is dropped either
// way.
func materializePrepared[T any](c *overlayContext) (*Prepared[T], error) {
	ops := c.ensureOperations()
	base := c.baseValue
	value := base
	switch {
	case len(ops) == 0:
	case c.simpleObjectMaterialization:
		value = c.root.cloneNode()
	default:
		v, err := materializeOperations(base, ops)
		if err != nil {
			return nil, err
		}
		value = v
	}
	p := &Prepared[T]{ctx: c, base: asT[T](base), value: asT[T](value), ops: ops}
	c.release()
	return p, nil
}

// materializeOperations is upstream's: the ops applied to base as
// ApplyImmutable applies them, copying each container they touch once. A
// batch with no splice or permutation skips validation, being the tracker's
// own (upstream's applyImmutableTrusted).
func materializeOperations(base any, ops []Op) (any, error) {
	for _, op := range ops {
		switch op.(type) {
		case Splice, Permute:
			return applyOps(base, ops, newOwnedSet())
		}
	}
	return applyTrusted(base, ops)
}

// ensureOperations is upstream's: a change's ops were emitted when it was
// prepared; a replacement's are none when it equals the base and one Replace
// otherwise.
func (c *overlayContext) ensureOperations() []Op {
	if c.opsReady {
		return c.ops
	}
	// Upstream reads the tracker's value here, which is the base: a
	// replacement is materialized as it is prepared.
	if jsonEqual(c.baseValue, c.root.base) {
		c.ops = []Op{}
	} else {
		c.ops = []Op{Replace{Value: c.root.base}}
	}
	c.opsReady = true
	return c.ops
}

// asT is v as the tracker's root type; the zero T for a nil v.
func asT[T any](v any) T {
	t, _ := v.(T)
	return t
}

// Prepared is a change ready to adopt: the revision it was prepared against,
// the revision it produces, and the ops between them. All three are shared
// and must not be modified; the ops' payloads may be the same containers as
// parts of Value.
type Prepared[T any] struct {
	ctx   *overlayContext
	base  T
	value T
	ops   []Op
}

// Base is the committed revision the change was prepared against.
func (p *Prepared[T]) Base() T { return p.base }

// Value is the revision adopting the change commits. It is Base itself when
// the change turned out to be a no-op.
func (p *Prepared[T]) Value() T { return p.value }

// Ops is the batch that turns Base into Value — what a replica applies. It is
// empty, not nil, for a no-op. Only the resulting value is contractual: the
// same change may be published as different tuples.
func (p *Prepared[T]) Ops() []Op { return p.ops }

// BaseRevision is the tracker revision the change was prepared against.
func (p *Prepared[T]) BaseRevision() int {
	if p.ctx == nil {
		return 0
	}
	return p.ctx.baseRevision
}

// Abort keeps the prepared change from being adopted; Value stays readable.
// Aborting one already adopted, aborted or stale does nothing.
func (p *Prepared[T]) Abort() {
	if p.ctx != nil {
		p.ctx.abort()
	}
}

// Change is one open edit of a tracker's committed revision. Mutate State,
// then Prepare it — or Abort it. Either settles the change; its drafts may be
// held across any amount of work in between, but not past that, nor past the
// tracker adopting a competing change.
//
// A Change is not safe for concurrent use.
type Change[T any] struct {
	ctx      *overlayContext
	prepared *statusCell
	settled  bool
	root     *Draft
}

// State is the draft of the root. After the change settles it is still
// returned, but every use of it fails.
func (c *Change[T]) State() *Draft { return c.root }

// errZeroChange is Go's own: a Change that BeginChange did not open has no
// draft to prepare.
var errZeroChange = errors.New("delta: Change was never begun (it is a zero Change; open one with Tracker.BeginChange)")

// Prepare settles the change: it emits the ops its overlay recorded and
// materializes the revision they produce, without changing the tracker. A
// change made stale by a competing adoption cannot be prepared: its drafts are
// settled.
func (c *Change[T]) Prepare() (*Prepared[T], error) {
	if c.settled {
		return nil, ErrChangeSettled
	}
	ctx := c.ctx
	if ctx == nil {
		return nil, errZeroChange
	}
	if ctx.settled() {
		return nil, ErrDraftSettled
	}
	if ctx.status.value != statusOpen {
		return nil, errReadOnly
	}
	ctx.status.value = statusPrepared
	defer func() { c.ctx = nil }()
	if len(ctx.dirty) == 0 {
		ctx.ops = []Op{}
	} else {
		ctx.ops = ctx.emitOperations()
	}
	ctx.opsReady = true
	p, err := materializePrepared[T](ctx)
	if err != nil {
		ctx.status.value = statusAborted
		ctx.release()
		c.settled = true
		return nil, err
	}
	c.prepared = ctx.status
	c.settled = true
	return p, nil
}

// Abort settles the change without preparing it, or — after Prepare —
// aborts the prepared change if it is still waiting to be adopted. Aborting
// twice, or aborting a zero Change, does nothing.
func (c *Change[T]) Abort() {
	if c.settled {
		if c.prepared != nil && c.prepared.value == statusPrepared {
			c.prepared.value = statusAborted
		}
		c.prepared = nil
		return
	}
	if c.ctx == nil {
		return
	}
	c.settled = true
	c.ctx.abort()
	c.ctx = nil
}
