package chord

import (
	"encoding/json"
	"fmt"

	"github.com/sky-valley/pi/chord/delta"
)

// This file mirrors the value types of upstream's src/types.ts: the shapes
// that describe services and cross a service boundary. Every one marshals to
// exactly the JSON pi writes, key for key, and validates itself the way
// upstream's wire.ts parsers do; the parsers themselves live in wire.go.
//
// Snapshots and updates carry operations, and there are two operation
// grammars — delta.Op as a provider emits and a replica applies them, and
// delta.WireOp as a codec interns them between peers. Upstream spells the
// containers out twice (ServiceSubscriptionSnapshot and
// WireServiceSubscriptionSnapshot); here each container is generic over the
// grammar, so ServiceSubscriptionSnapshot[delta.WireOp] is the wire form and
// the two never need to agree by hand.

// opTuple is the constraint on an operation grammar: delta.Op or delta.WireOp.
// Both are interfaces whose method sets satisfy it. It is what lets a
// snapshot or update validate and marshal the operations it carries without
// knowing which grammar they are in.
type opTuple interface {
	json.Marshaler
	Validate() error
}

// ServiceMode is how a service is instantiated: one implementation, or one
// live instance per key.
type ServiceMode string

const (
	Singleton ServiceMode = "singleton"
	Keyed     ServiceMode = "keyed"
)

func (m ServiceMode) validate() error {
	switch m {
	case Singleton, Keyed:
		return nil
	}
	return fmt.Errorf("mode must be %q or %q, got %q", Singleton, Keyed, string(m))
}

// ServiceCatalogueEntry names one service a provider publishes and how it is
// instantiated.
type ServiceCatalogueEntry struct {
	ServiceID string      `json:"serviceId"`
	Mode      ServiceMode `json:"mode"`
}

// Validate is the entry's contract: a non-empty ID and a known mode.
func (e ServiceCatalogueEntry) Validate() error {
	if err := checkID("serviceId", e.ServiceID); err != nil {
		return err
	}
	return e.Mode.validate()
}

// ServiceInstanceAddress names one live instance of a keyed service. The
// generation distinguishes a respawned instance from the one it replaced under
// the same key, so a call addressed to the old one is stale rather than
// misdelivered.
type ServiceInstanceAddress struct {
	Key        string `json:"key"`
	Generation int    `json:"generation"`
}

// Validate is the address's contract: a non-empty key and a generation of at
// least 1.
func (a ServiceInstanceAddress) Validate() error {
	if err := checkID("key", a.Key); err != nil {
		return err
	}
	return checkMinimum("generation", a.Generation, 1)
}

// ServiceMemberSnapshot describes one member of a service instance as a
// subscriber first sees it: a callable method, or a replicated state with the
// operations that rebuild its current value. It is sealed to [MethodSnapshot]
// and [StateSnapshot].
//
// O is the operation grammar the snapshot's stream is in. Every arm carries it,
// the op-free MethodSnapshot included, because the seal binds an arm to one
// grammar: a StateSnapshot[delta.Op] is not a member of a wire snapshot, and
// the compiler says so.
type ServiceMemberSnapshot[O opTuple] interface {
	json.Marshaler
	Validate() error
	member(O)
}

// MethodSnapshot is {"name": ..., "kind": "method"}: a member invoked through
// a [ServiceCall].
type MethodSnapshot[O opTuple] struct {
	Name string `json:"name"`
}

// StateSnapshot is {"name": ..., "kind": "state", "sequence": ..., "ops":
// [...]}: a replicated state at Sequence, whose Ops rebuild its value from
// nothing — in practice a single Replace.
type StateSnapshot[O opTuple] struct {
	Name     string `json:"name"`
	Sequence int    `json:"sequence"`
	Ops      []O    `json:"ops"`
}

func (MethodSnapshot[O]) member(O) {}
func (StateSnapshot[O]) member(O)  {}

// Validate requires a non-empty name.
func (m MethodSnapshot[O]) Validate() error { return checkID("name", m.Name) }

// Validate requires a non-empty name, a sequence of at least 0 and valid ops.
// A nil Ops is an empty array, as it marshals.
func (s StateSnapshot[O]) Validate() error {
	if err := checkID("name", s.Name); err != nil {
		return err
	}
	if err := checkMinimum("sequence", s.Sequence, 0); err != nil {
		return err
	}
	return validateOps(s.Ops)
}

func (m MethodSnapshot[O]) MarshalJSON() ([]byte, error) {
	return marshalJSON(struct {
		Name string `json:"name"`
		Kind string `json:"kind"`
	}{m.Name, "method"})
}

func (s StateSnapshot[O]) MarshalJSON() ([]byte, error) {
	return marshalJSON(struct {
		Name     string `json:"name"`
		Kind     string `json:"kind"`
		Sequence int    `json:"sequence"`
		Ops      []O    `json:"ops"`
	}{s.Name, "state", s.Sequence, nonNil(s.Ops)})
}

// ServiceInstanceSnapshot is one instance and its members. Instance is nil
// for a singleton, which has no address, and the key is then omitted from
// the JSON.
type ServiceInstanceSnapshot[O opTuple] struct {
	Instance *ServiceInstanceAddress    `json:"instance,omitempty"`
	Members  []ServiceMemberSnapshot[O] `json:"members"`
}

// Validate checks the address, when present, and every member.
func (s ServiceInstanceSnapshot[O]) Validate() error {
	if s.Instance != nil {
		if err := s.Instance.Validate(); err != nil {
			return fmt.Errorf("instance: %w", err)
		}
	}
	for i, member := range s.Members {
		if member == nil {
			return fmt.Errorf("members[%d] is nil; a member is a MethodSnapshot or a StateSnapshot", i)
		}
		if err := member.Validate(); err != nil {
			return fmt.Errorf("members[%d]: %w", i, err)
		}
	}
	return nil
}

func (s ServiceInstanceSnapshot[O]) MarshalJSON() ([]byte, error) {
	type plain ServiceInstanceSnapshot[O]
	s.Members = nonNil(s.Members)
	return marshalJSON(plain(s))
}

// ServiceSubscriptionSnapshot is what a subscribe control call returns: the
// service, its mode, and every live instance — exactly one, unaddressed, for
// an available singleton; none for an unavailable one; any number for a
// keyed service.
type ServiceSubscriptionSnapshot[O opTuple] struct {
	ServiceID string                       `json:"serviceId"`
	Mode      ServiceMode                  `json:"mode"`
	Instances []ServiceInstanceSnapshot[O] `json:"instances"`
}

// Validate checks the ID, the mode and every instance.
func (s ServiceSubscriptionSnapshot[O]) Validate() error {
	if err := checkID("serviceId", s.ServiceID); err != nil {
		return err
	}
	if err := s.Mode.validate(); err != nil {
		return err
	}
	for i, instance := range s.Instances {
		if err := instance.Validate(); err != nil {
			return fmt.Errorf("instances[%d]: %w", i, err)
		}
	}
	return nil
}

func (s ServiceSubscriptionSnapshot[O]) MarshalJSON() ([]byte, error) {
	type plain ServiceSubscriptionSnapshot[O]
	s.Instances = nonNil(s.Instances)
	return marshalJSON(plain(s))
}

// ServiceProviderUpdate is one change a provider pushes to a subscription
// after its snapshot. It is sealed to [StateUpdate], [UnavailableUpdate],
// [ReplacedUpdate], [SpawnedUpdate] and [ClosedUpdate], each of which
// marshals with its "type" discriminator first.
//
// O is the operation grammar, as for [ServiceMemberSnapshot]: every arm
// carries it so that an update belongs to exactly one stream.
type ServiceProviderUpdate[O opTuple] interface {
	json.Marshaler
	Validate() error
	update(O)
}

// StateUpdate is {"type": "state", ...}: the operations that move one
// replicated state member from Sequence-1 to Sequence. Instance addresses a
// keyed instance and is nil, and omitted, for a singleton.
type StateUpdate[O opTuple] struct {
	Instance *ServiceInstanceAddress `json:"instance,omitempty"`
	Member   string                  `json:"member"`
	Sequence int                     `json:"sequence"`
	Ops      []O                     `json:"ops"`
}

// UnavailableUpdate is {"type": "unavailable"}: the singleton's implementation
// went away.
type UnavailableUpdate[O opTuple] struct{}

// ReplacedUpdate is {"type": "replaced", "snapshot": ...}: the singleton's
// implementation was swapped for a new one, described in full.
type ReplacedUpdate[O opTuple] struct {
	Snapshot ServiceInstanceSnapshot[O] `json:"snapshot"`
}

// SpawnedUpdate is {"type": "spawned", "instance": ...}: a keyed instance came
// to life, described in full with its address.
type SpawnedUpdate[O opTuple] struct {
	Instance ServiceInstanceSnapshot[O] `json:"instance"`
}

// ClosedUpdate is {"type": "closed", "instance": ...}: the addressed keyed
// instance is gone.
type ClosedUpdate[O opTuple] struct {
	Instance ServiceInstanceAddress `json:"instance"`
}

func (StateUpdate[O]) update(O)       {}
func (UnavailableUpdate[O]) update(O) {}
func (ReplacedUpdate[O]) update(O)    {}
func (SpawnedUpdate[O]) update(O)     {}
func (ClosedUpdate[O]) update(O)      {}

// Validate requires a non-empty member, a sequence of at least 1 — a
// snapshot is sequence 0, so an update is always later — a valid address when
// one is present, and valid ops.
func (u StateUpdate[O]) Validate() error {
	if err := checkID("member", u.Member); err != nil {
		return fmt.Errorf("state update %w", err)
	}
	if err := checkMinimum("sequence", u.Sequence, 1); err != nil {
		return fmt.Errorf("state update %w", err)
	}
	if u.Instance != nil {
		if err := u.Instance.Validate(); err != nil {
			return fmt.Errorf("state update instance: %w", err)
		}
	}
	if err := validateOps(u.Ops); err != nil {
		return fmt.Errorf("state update %w", err)
	}
	return nil
}

// Validate always succeeds: the update carries nothing.
func (UnavailableUpdate[O]) Validate() error { return nil }

// Validate checks the replacement snapshot.
func (u ReplacedUpdate[O]) Validate() error {
	if err := u.Snapshot.Validate(); err != nil {
		return fmt.Errorf("replacement update snapshot: %w", err)
	}
	return nil
}

// Validate checks the spawned instance's snapshot.
func (u SpawnedUpdate[O]) Validate() error {
	if err := u.Instance.Validate(); err != nil {
		return fmt.Errorf("spawn update instance: %w", err)
	}
	return nil
}

// Validate checks the closed instance's address.
func (u ClosedUpdate[O]) Validate() error {
	if err := u.Instance.Validate(); err != nil {
		return fmt.Errorf("close update instance: %w", err)
	}
	return nil
}

func (u StateUpdate[O]) MarshalJSON() ([]byte, error) {
	return marshalJSON(struct {
		Type     string                  `json:"type"`
		Instance *ServiceInstanceAddress `json:"instance,omitempty"`
		Member   string                  `json:"member"`
		Sequence int                     `json:"sequence"`
		Ops      []O                     `json:"ops"`
	}{"state", u.Instance, u.Member, u.Sequence, nonNil(u.Ops)})
}

func (UnavailableUpdate[O]) MarshalJSON() ([]byte, error) {
	return []byte(`{"type":"unavailable"}`), nil
}

func (u ReplacedUpdate[O]) MarshalJSON() ([]byte, error) {
	return marshalJSON(struct {
		Type     string                     `json:"type"`
		Snapshot ServiceInstanceSnapshot[O] `json:"snapshot"`
	}{"replaced", u.Snapshot})
}

func (u SpawnedUpdate[O]) MarshalJSON() ([]byte, error) {
	return marshalJSON(struct {
		Type     string                     `json:"type"`
		Instance ServiceInstanceSnapshot[O] `json:"instance"`
	}{"spawned", u.Instance})
}

func (u ClosedUpdate[O]) MarshalJSON() ([]byte, error) {
	return marshalJSON(struct {
		Type     string                 `json:"type"`
		Instance ServiceInstanceAddress `json:"instance"`
	}{"closed", u.Instance})
}

// ServiceCall invokes one member of a service: a method, or one of the
// control members of $chord.service (see [ServiceControlCall]). Instance is
// nil, and omitted, when the service is a singleton.
//
// Args are borrowed immutable values: chord validates their shape at the
// boundary but does not clone them, and [ParseServiceCall] does not check that
// each is strict JSON — the envelope decoder that produced the tree already
// guarantees that.
type ServiceCall struct {
	ServiceID string                  `json:"serviceId"`
	Instance  *ServiceInstanceAddress `json:"instance,omitempty"`
	Member    string                  `json:"member"`
	Args      []Value                 `json:"args"`
}

// Validate requires a non-empty service ID and member and a valid address
// when one is present. A nil Args is an empty array, as it marshals.
func (c ServiceCall) Validate() error {
	if err := checkID("serviceId", c.ServiceID); err != nil {
		return err
	}
	if err := checkID("member", c.Member); err != nil {
		return err
	}
	if c.Instance != nil {
		if err := c.Instance.Validate(); err != nil {
			return fmt.Errorf("instance: %w", err)
		}
	}
	return nil
}

func (c ServiceCall) MarshalJSON() ([]byte, error) {
	type plain ServiceCall
	c.Args = nonNil(c.Args)
	return marshalJSON(plain(c))
}

// checkID is upstream's isId: a non-empty string.
func checkID(name, value string) error {
	if value == "" {
		return fmt.Errorf("%s must be a non-empty string", name)
	}
	return nil
}

// checkMinimum is upstream's isInteger(value, minimum); the integer half is
// the type's.
func checkMinimum(name string, value, minimum int) error {
	if value < minimum {
		return fmt.Errorf("%s must be an integer of at least %d, got %d", name, minimum, value)
	}
	return nil
}

// validateOps runs each op's own validator — ParseOp or ParseWireOp has
// already done so for a parsed value; this is for values built in Go.
func validateOps[O opTuple](ops []O) error {
	for i, op := range ops {
		// An O is an interface value, and a nil one has nothing to validate
		// with; catch it here rather than as a nil dereference inside.
		if any(op) == nil {
			return fmt.Errorf("ops[%d] is nil; every op is a value delta.ParseOp or delta.ParseWireOp returns", i)
		}
		if err := op.Validate(); err != nil {
			return fmt.Errorf("ops[%d]: %w", i, err)
		}
	}
	return nil
}

// nonNil is how a nil slice marshals: as the empty array pi's parsers require,
// not as null.
func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// Compile-time proof that the two grammars instantiate every container.
var (
	_ ServiceMemberSnapshot[delta.Op]     = StateSnapshot[delta.Op]{}
	_ ServiceMemberSnapshot[delta.WireOp] = MethodSnapshot[delta.WireOp]{}
	_ ServiceProviderUpdate[delta.Op]     = StateUpdate[delta.Op]{}
	_ ServiceProviderUpdate[delta.WireOp] = UnavailableUpdate[delta.WireOp]{}
	_ ServiceProviderUpdate[delta.WireOp] = ReplacedUpdate[delta.WireOp]{}
	_ ServiceProviderUpdate[delta.Op]     = SpawnedUpdate[delta.Op]{}
	_ ServiceProviderUpdate[delta.Op]     = ClosedUpdate[delta.Op]{}
)
