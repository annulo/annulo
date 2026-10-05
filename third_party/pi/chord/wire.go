package chord

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/sky-valley/pi/chord/delta"
	"github.com/sky-valley/pi/internal/jsonstrict"
)

// This file mirrors upstream's src/services/wire.ts: the $chord.service
// control grammar and the strict parsers a transport runs on every value it
// lifts out of an envelope. The parsers never see bytes — the envelope
// decoder has already produced a tree of nil, bool, float64 or int64, string,
// []any and map[string]any — so they walk that tree with internal/jsonstrict,
// which fills each struct in types.go under the same rule TypeBox's
// additionalProperties:false imposes upstream: a key the shape does not
// declare is a rejection. A peer that can smuggle a field past the parser can
// reach code the shape was meant to gate, so extra-key rejection is a security
// boundary, not tidiness.

// The wire forms of the containers in types.go: the same shapes carrying
// interned operations. A state codec turns one into the other; the parsers
// below accept each grammar and refuse the other's forms.
type (
	WireServiceMemberSnapshot       = ServiceMemberSnapshot[delta.WireOp]
	WireServiceInstanceSnapshot     = ServiceInstanceSnapshot[delta.WireOp]
	WireServiceSubscriptionSnapshot = ServiceSubscriptionSnapshot[delta.WireOp]
	WireServiceProviderUpdate       = ServiceProviderUpdate[delta.WireOp]
)

// ServiceValueError is what every parser in this file returns for a value that
// does not satisfy its contract: What names the contract in upstream's words
// ("service call", "service catalogue", ...), Err says which rule failed and
// where. The text never crosses the wire — a server answers a bad call with a
// fixed message — so it is written for the author of the peer that sent it.
type ServiceValueError struct {
	What string
	Err  error
}

func (e *ServiceValueError) Error() string { return "invalid " + e.What + ": " + e.Err.Error() }
func (e *ServiceValueError) Unwrap() error { return e.Err }

// ─── $chord.service ──────────────────────────────────────────────────────────

// serviceControlID is the reserved service every provider answers for: its
// members list the catalogue and open and close subscriptions. It is the
// reason DefineService refuses IDs under reservedServicePrefix.
const serviceControlID = reservedServicePrefix + "service"

const (
	serviceCatalogueMember   = "catalogue"
	serviceSubscribeMember   = "subscribe"
	serviceUnsubscribeMember = "unsubscribe"
)

// ServiceControlCall is one of the three calls to $chord.service, sealed to
// [CatalogueCall], [SubscribeCall] and [UnsubscribeCall]. Call encodes it as
// the [ServiceCall] a transport sends; [DecodeServiceControlCall] is the
// inverse, for the endpoint that receives one.
type ServiceControlCall interface {
	Call() ServiceCall
	control()
}

// CatalogueCall asks for every service the provider publishes; the result is
// a catalogue ([ParseServiceCatalogue]).
type CatalogueCall struct{}

// SubscribeCall opens a subscription to one service under a caller-chosen ID;
// the result is a [ServiceSubscriptionSnapshot], and updates follow under the
// same ID until an [UnsubscribeCall].
type SubscribeCall struct {
	SubscriptionID string
	ServiceID      string
	Mode           ServiceMode
}

// UnsubscribeCall closes the subscription opened under SubscriptionID.
type UnsubscribeCall struct{ SubscriptionID string }

func (CatalogueCall) control()   {}
func (SubscribeCall) control()   {}
func (UnsubscribeCall) control() {}

func (CatalogueCall) Call() ServiceCall {
	return ServiceCall{ServiceID: serviceControlID, Member: serviceCatalogueMember, Args: []Value{}}
}

func (c SubscribeCall) Call() ServiceCall {
	return ServiceCall{
		ServiceID: serviceControlID,
		Member:    serviceSubscribeMember,
		Args:      []Value{c.SubscriptionID, c.ServiceID, string(c.Mode)},
	}
}

func (c UnsubscribeCall) Call() ServiceCall {
	return ServiceCall{ServiceID: serviceControlID, Member: serviceUnsubscribeMember, Args: []Value{c.SubscriptionID}}
}

// DecodeServiceControlCall recognizes a control call: the control service,
// unaddressed, invoking one of its members with exactly that member's
// arguments. Anything else — an ordinary call, or a malformed control call —
// reports false and is the provider's to refuse.
func DecodeServiceControlCall(call ServiceCall) (ServiceControlCall, bool) {
	if call.ServiceID != serviceControlID || call.Instance != nil {
		return nil, false
	}
	args := call.Args
	switch call.Member {
	case serviceCatalogueMember:
		if len(args) == 0 {
			return CatalogueCall{}, true
		}
	case serviceSubscribeMember:
		if len(args) != 3 {
			break
		}
		subscriptionID, ok1 := idArg(args[0])
		serviceID, ok2 := idArg(args[1])
		mode, ok3 := modeArg(args[2])
		if ok1 && ok2 && ok3 {
			return SubscribeCall{SubscriptionID: subscriptionID, ServiceID: serviceID, Mode: mode}, true
		}
	case serviceUnsubscribeMember:
		if len(args) == 1 {
			if subscriptionID, ok := idArg(args[0]); ok {
				return UnsubscribeCall{SubscriptionID: subscriptionID}, true
			}
		}
	}
	return nil, false
}

// idArg is upstream's isId on an argument: a non-empty string.
func idArg(v Value) (string, bool) {
	s, ok := v.(string)
	return s, ok && s != ""
}

// modeArg is upstream's mode check on an argument. A Go caller building the
// args by hand may spell the mode as a ServiceMode; it is the same string and
// the same JSON, so both spellings are read.
func modeArg(v Value) (ServiceMode, bool) {
	var mode ServiceMode
	switch m := v.(type) {
	case string:
		mode = ServiceMode(m)
	case ServiceMode:
		mode = m
	default:
		return "", false
	}
	return mode, mode.validate() == nil
}

// ─── Parsers ─────────────────────────────────────────────────────────────────

// wireDecoder walks a tree into the structs of types.go. One decoder serves
// the package: its union table is fixed at init and its field cache is safe
// for concurrent use.
var wireDecoder = newWireDecoder()

func newWireDecoder() *jsonstrict.Decoder {
	d := &jsonstrict.Decoder{Tag: "json"}
	// Each op grammar resolves through its own delta validator; the member
	// and update unions resolve through their discriminator, once per grammar.
	// Upstream threads assertValidOp or assertValidWireOp through its walkers
	// as a parameter; here the op type of the target field selects it.
	jsonstrict.RegisterUnion(d, delta.ParseOp)
	jsonstrict.RegisterUnion(d, delta.ParseWireOp)
	jsonstrict.RegisterUnion(d, memberDecoder[delta.Op](d))
	jsonstrict.RegisterUnion(d, memberDecoder[delta.WireOp](d))
	jsonstrict.RegisterUnion(d, updateDecoder[delta.Op](d))
	jsonstrict.RegisterUnion(d, updateDecoder[delta.WireOp](d))
	return d
}

// memberDecoder resolves {"kind": "method" | "state", ...} into the matching
// arm of ServiceMemberSnapshot[O].
func memberDecoder[O opTuple](d *jsonstrict.Decoder) func(any) (ServiceMemberSnapshot[O], error) {
	return func(v any) (ServiceMemberSnapshot[O], error) {
		kind, entries, err := jsonstrict.Discriminant(v, "kind")
		if err != nil {
			return nil, err
		}
		switch kind {
		case "method":
			return jsonstrict.DecodeMember[MethodSnapshot[O]](d, without(entries, "kind"))
		case "state":
			return jsonstrict.DecodeMember[StateSnapshot[O]](d, without(entries, "kind"))
		}
		return nil, jsonstrict.Errorf("member kind must be \"method\" or \"state\", got %q", kind)
	}
}

// updateDecoder resolves {"type": ..., ...} into the matching arm of
// ServiceProviderUpdate[O].
func updateDecoder[O opTuple](d *jsonstrict.Decoder) func(any) (ServiceProviderUpdate[O], error) {
	return func(v any) (ServiceProviderUpdate[O], error) {
		kind, entries, err := jsonstrict.Discriminant(v, "type")
		if err != nil {
			return nil, err
		}
		rest := without(entries, "type")
		switch kind {
		case "state":
			return jsonstrict.DecodeMember[StateUpdate[O]](d, rest)
		case "unavailable":
			return jsonstrict.DecodeMember[UnavailableUpdate[O]](d, rest)
		case "replaced":
			return jsonstrict.DecodeMember[ReplacedUpdate[O]](d, rest)
		case "spawned":
			return jsonstrict.DecodeMember[SpawnedUpdate[O]](d, rest)
		case "closed":
			return jsonstrict.DecodeMember[ClosedUpdate[O]](d, rest)
		}
		return nil, jsonstrict.Errorf("update type must be one of state, unavailable, replaced, spawned or closed, got %q", kind)
	}
}

// without is the object minus its discriminator, which the arm's struct does
// not declare as a field: it is the arm's identity, carried by the Go type.
func without(entries map[string]any, key string) map[string]any {
	rest := make(map[string]any, len(entries)-1)
	for k, v := range entries {
		if k != key {
			rest[k] = v
		}
	}
	return rest
}

// parse decodes v into a T under upstream's description of it.
func parse[T any](v any, what string) (T, error) {
	var out T
	if err := wireDecoder.Decode(v, &out); err != nil {
		return out, &ServiceValueError{What: what, Err: err}
	}
	return out, nil
}

// ParseServiceCall validates a received call: a service ID, an optional
// address, a member and an argument array, and nothing else. The args are
// borrowed from the tree, not copied.
func ParseServiceCall(v any) (ServiceCall, error) {
	return parse[ServiceCall](v, "service call")
}

// ParseServiceCatalogue validates a catalogue result: entries with distinct
// service IDs.
func ParseServiceCatalogue(v any) ([]ServiceCatalogueEntry, error) {
	const what = "service catalogue"
	entries, err := parse[[]ServiceCatalogueEntry](v, what)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(entries))
	for i, entry := range entries {
		if seen[entry.ServiceID] {
			return nil, &ServiceValueError{What: what, Err: fmt.Errorf("[%d] duplicates serviceId %q; a catalogue lists each service once", i, entry.ServiceID)}
		}
		seen[entry.ServiceID] = true
	}
	return entries, nil
}

// ParseServiceSubscriptionSnapshot validates a subscribe result whose
// operations are decoded ops, as a provider emits them.
func ParseServiceSubscriptionSnapshot(v any) (ServiceSubscriptionSnapshot[delta.Op], error) {
	return parse[ServiceSubscriptionSnapshot[delta.Op]](v, "service subscription snapshot")
}

// ParseWireServiceSubscriptionSnapshot validates a subscribe result whose
// operations are wire ops, as a codec emits them.
func ParseWireServiceSubscriptionSnapshot(v any) (WireServiceSubscriptionSnapshot, error) {
	return parse[WireServiceSubscriptionSnapshot](v, "service subscription snapshot")
}

// ParseServiceProviderUpdate validates a subscription update whose operations
// are decoded ops.
func ParseServiceProviderUpdate(v any) (ServiceProviderUpdate[delta.Op], error) {
	return parse[ServiceProviderUpdate[delta.Op]](v, "service provider update")
}

// ParseWireServiceProviderUpdate validates a subscription update whose
// operations are wire ops.
func ParseWireServiceProviderUpdate(v any) (WireServiceProviderUpdate, error) {
	return parse[WireServiceProviderUpdate](v, "service provider update")
}

// marshalJSON writes v the way JSON.stringify does: compact, and without
// escaping <, > and & — the HTML-safe escapes encoding/json applies by
// default are a different byte sequence for the same value.
func marshalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}
