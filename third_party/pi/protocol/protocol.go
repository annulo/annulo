package protocol

import (
	"fmt"

	"github.com/sky-valley/pi/chord"
	"github.com/sky-valley/pi/internal/jsonstrict"
	"github.com/sky-valley/pi/protocol/cbor"
)

// The v8 envelope set: pi's packages/protocol/src/protocol.ts at 64eeb82a4,
// which replaced the session-RPC schemas (schemas.go, messages.go) with
// service-addressed routing whose payloads the protocol layer relays without
// interpreting.
//
// This file is ADDITIVE. The v1 types stay until the server and client are
// rewritten against these envelopes, and then they are deleted in one cut. So
// that the cut is a mechanical rename, every name here that collides with a v1
// type carries a V8 suffix (ClientHelloV8, RequestEnvelopeV8, ...) and every
// name that does not collide is already final (SessionTarget, CancelEnvelope,
// ServiceEventEnvelope, AttachmentEnvelope, IsServerID). Nothing in this file
// is reachable from the v1 codec: the two versions share only the strict
// decoder, the CBOR codec and the framing.
//
// Struct field order below is the wire order and mirrors the property order of
// pi's TypeBox schemas, so a Go encoding is byte-identical to a Node one.

// ProtocolVersionV8 is the wire version the envelope set below speaks (pi's
// PROTOCOL_VERSION). It becomes ProtocolVersion at the cutover.
const ProtocolVersionV8 = 8

// IsSupportedVersionV8 reports whether a peer's advertised version is one the
// v8 envelopes speak (pi's isSupportedProtocolVersion). pi also refuses a
// non-integer here; a Go caller cannot pass one, and ClientHelloV8 already
// refuses to decode a fractional version.
func IsSupportedVersionV8(version int64) bool { return version == ProtocolVersionV8 }

// ServerID identifies one logical server: a canonical lowercase UUIDv4 (pi's
// ServerIdSchema pattern). Every RPC target is fenced to one, so a request
// that survives a reconnect to a different server is refused rather than
// silently re-routed.
type ServerID string

// IsServerID reports whether s is a canonical lowercase UUIDv4:
// ^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$.
//
// The pattern is fixed and this runs on every target of every message, so it
// is a 36-byte scan rather than a regexp.
func IsServerID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := range 36 {
		c := s[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		case 14: // version nibble
			if c != '4' {
				return false
			}
		case 19: // variant nibble
			switch c {
			case '8', '9', 'a', 'b':
			default:
				return false
			}
		default:
			if !isLowerHex(c) {
				return false
			}
		}
	}
	return true
}

func isLowerHex(c byte) bool {
	return ('0' <= c && c <= '9') || ('a' <= c && c <= 'f')
}

func requireServerID(name string, id ServerID) error {
	if !IsServerID(string(id)) {
		return invalidf("%s must be a canonical lowercase UUIDv4 (8-4-4-4-12 hex digits, "+
			"version 4, variant 8/9/a/b), got %q", name, string(id))
	}
	return nil
}

// ProtocolErrorV8 is a structured failure carried in a response or hello_error.
//
// Code is an open string, deliberately: pi declares `ProtocolErrorCode =
// string` because the codes are owned by whichever service raised the error
// (wrong_server, cancelled, service_not_found, application_error, ...), and a
// closed Go enum would silently reject a code a real peer sends. It is
// pi's IdSchema, so it must not be empty.
type ProtocolErrorV8 struct {
	Code    string `cbor:"code"`
	Message string `cbor:"message"`
}

func (e *ProtocolErrorV8) Validate() error {
	return requireID("code", e.Code)
}

// Error lets a ProtocolErrorV8 be returned as a Go error.
func (e *ProtocolErrorV8) Error() string {
	return e.Code + ": " + e.Message
}

// ClientHelloV8 must be the first frame a client sends. Version is any
// non-negative integer, not just ProtocolVersionV8: the server negotiates,
// and a hello for a version it does not speak is answered with a hello_error
// rather than refused at the codec.
type ClientHelloV8 struct {
	Type    string `cbor:"type"`
	Version int64  `cbor:"version"`
}

func (*ClientHelloV8) clientMessageV8() {}

func (m *ClientHelloV8) Validate() error {
	if m == nil {
		return nilMember(m)
	}
	if err := requireLiteral("type", m.Type, "hello"); err != nil {
		return err
	}
	return requireNonNegative("version", m.Version)
}

// NewClientHelloV8 builds a hello for the version this package speaks.
func NewClientHelloV8() *ClientHelloV8 {
	return &ClientHelloV8{Type: "hello", Version: ProtocolVersionV8}
}

// RpcTarget is where a request or cancel is routed: a server-wide call
// (NewServerTarget) or a *SessionTarget. pi discriminates the two arms by the
// presence of sessionId, and so does the decoder.
//
// The union is sealed and the server-wide arm is unexported, as upstream
// keeps its ServerTarget internal: build one with NewServerTarget.
type RpcTarget interface {
	// TargetServerID is the logical server the call is fenced to.
	TargetServerID() ServerID
	rpcTarget()
}

// serverTarget is a server-wide call, fenced to one logical server.
type serverTarget struct {
	ServerID ServerID `cbor:"serverId"`
}

func (t *serverTarget) TargetServerID() ServerID { return t.ServerID }
func (*serverTarget) rpcTarget()                 {}

func (t *serverTarget) Validate() error {
	if t == nil {
		return nilMember(t)
	}
	return requireServerID("serverId", t.ServerID)
}

// NewServerTarget builds the server-wide RPC target for one logical server.
func NewServerTarget(serverID ServerID) RpcTarget {
	return &serverTarget{ServerID: serverID}
}

// SessionTarget is a session call, fenced to one logical server, durable
// session, and live attachment.
type SessionTarget struct {
	ServerID     ServerID `cbor:"serverId"`
	SessionID    string   `cbor:"sessionId"`
	AttachmentID string   `cbor:"attachmentId"`
}

func (t *SessionTarget) TargetServerID() ServerID { return t.ServerID }
func (*SessionTarget) rpcTarget()                 {}

func (t *SessionTarget) Validate() error {
	if t == nil {
		return nilMember(t)
	}
	if err := requireServerID("serverId", t.ServerID); err != nil {
		return err
	}
	if err := requireID("sessionId", t.SessionID); err != nil {
		return err
	}
	return requireID("attachmentId", t.AttachmentID)
}

// requireTarget checks a routed envelope's target: present and well-formed.
func requireTarget(target RpcTarget) error {
	if target == nil {
		return invalidf("target is required; route the call with NewServerTarget or a *SessionTarget")
	}
	return target.(Validator).Validate()
}

// Opaque payloads. pi types `call`, `result` and `update` as chord's JsonValue
// and relays them without interpreting; the port carries them as the exact
// wire bytes (cbor.RawItem, see DecodeRaw) so a relay is byte-exact, and
// validates them the way pi's isJsonValue does — on the decoded form.

// requireOpaqueJSON checks that raw holds exactly one CBOR item and that the
// item is a strict JSON value: no byte strings, no non-finite numbers.
//
// The re-read is bounded by the item's own length, not by cbor's 16MB default.
// Size is the frame's to limit, and it already has: an inbound span was read
// under the decoder's maxFrameLength, and an outbound one is checked against it
// when the envelope is encoded. A default here would refuse, in both
// directions, a payload that a larger maxFrameLength admits and pi accepts.
func requireOpaqueJSON(name string, raw cbor.RawItem) error {
	length := len(raw)
	value, err := cbor.Decode(raw, &cbor.Options{MaxByteLength: &length})
	if err != nil {
		return invalidf("%s must hold exactly one CBOR item: %s", name, err.Error())
	}
	if !chord.IsValue(value) {
		return invalidf("%s must be a JSON value: no byte strings, only finite numbers", name)
	}
	return nil
}

// RequestEnvelopeV8 carries one service call, the target it is fenced to, and
// the id its response will echo. Call is opaque to this layer: its service
// meaning belongs to Chord.
type RequestEnvelopeV8 struct {
	Type   string       `cbor:"type"`
	ID     string       `cbor:"id"`
	Target RpcTarget    `cbor:"target"`
	Call   cbor.RawItem `cbor:"call"`
}

func (*RequestEnvelopeV8) clientMessageV8() {}

func (m *RequestEnvelopeV8) Validate() error {
	if m == nil {
		return nilMember(m)
	}
	if err := requireLiteral("type", m.Type, "request"); err != nil {
		return err
	}
	if err := requireID("id", m.ID); err != nil {
		return err
	}
	if err := requireTarget(m.Target); err != nil {
		return err
	}
	return requireOpaqueJSON("call", m.Call)
}

// NewRequestV8 wraps an encoded call in a request envelope.
func NewRequestV8(id string, target RpcTarget, call cbor.RawItem) *RequestEnvelopeV8 {
	return &RequestEnvelopeV8{Type: "request", ID: id, Target: target, Call: call}
}

// CancelEnvelope asks the server to abandon the request with the same id. The
// target is repeated so a cancel is fenced exactly as the request was.
type CancelEnvelope struct {
	Type   string    `cbor:"type"`
	ID     string    `cbor:"id"`
	Target RpcTarget `cbor:"target"`
}

func (*CancelEnvelope) clientMessageV8() {}

func (m *CancelEnvelope) Validate() error {
	if m == nil {
		return nilMember(m)
	}
	if err := requireLiteral("type", m.Type, "cancel"); err != nil {
		return err
	}
	if err := requireID("id", m.ID); err != nil {
		return err
	}
	return requireTarget(m.Target)
}

// NewCancel builds a cancel for the request with the given id and target.
func NewCancel(id string, target RpcTarget) *CancelEnvelope {
	return &CancelEnvelope{Type: "cancel", ID: id, Target: target}
}

// ClientMessageV8 is anything a client may send: *ClientHelloV8,
// *RequestEnvelopeV8 or *CancelEnvelope.
type ClientMessageV8 interface{ clientMessageV8() }

// ServerHelloV8 answers a ClientHelloV8. Version is the literal
// ProtocolVersionV8: a server that speaks something else sends hello_error.
type ServerHelloV8 struct {
	Type     string   `cbor:"type"`
	Version  int64    `cbor:"version"`
	ServerID ServerID `cbor:"serverId"`
}

func (*ServerHelloV8) serverMessageV8() {}

func (m *ServerHelloV8) Validate() error {
	if m == nil {
		return nilMember(m)
	}
	if err := requireLiteral("type", m.Type, "hello"); err != nil {
		return err
	}
	if m.Version != ProtocolVersionV8 {
		return invalidf("version must be %d, got %d", ProtocolVersionV8, m.Version)
	}
	return requireServerID("serverId", m.ServerID)
}

// NewServerHelloV8 builds the hello a server sends for the version this
// package speaks.
func NewServerHelloV8(serverID ServerID) *ServerHelloV8 {
	return &ServerHelloV8{Type: "hello", Version: ProtocolVersionV8, ServerID: serverID}
}

// ServerHelloErrorV8 rejects a handshake.
type ServerHelloErrorV8 struct {
	Type  string          `cbor:"type"`
	Error ProtocolErrorV8 `cbor:"error"`
}

func (*ServerHelloErrorV8) serverMessageV8() {}

func (m *ServerHelloErrorV8) Validate() error {
	if m == nil {
		return nilMember(m)
	}
	if err := requireLiteral("type", m.Type, "hello_error"); err != nil {
		return err
	}
	return m.Error.Validate()
}

// ResponseEnvelopeV8 answers a RequestEnvelopeV8.
//
// pi splits this into an ok:true arm whose result is OPTIONAL and an ok:false
// arm carrying error. One struct reproduces both arms with presence semantics:
// a nil Result is no result field at all — a void response decodes to nil and
// encodes back without the field — while a Result holding CBOR null is the
// JSON value null. Only one arm is ever populated, so the wire order is the
// same either way.
type ResponseEnvelopeV8 struct {
	Type   string           `cbor:"type"`
	ID     string           `cbor:"id"`
	OK     bool             `cbor:"ok"`
	Result cbor.RawItem     `cbor:"result,omitempty"`
	Error  *ProtocolErrorV8 `cbor:"error,omitempty"`
}

func (*ResponseEnvelopeV8) serverMessageV8() {}

func (m *ResponseEnvelopeV8) Validate() error {
	if m == nil {
		return nilMember(m)
	}
	if err := requireLiteral("type", m.Type, "response"); err != nil {
		return err
	}
	if err := requireID("id", m.ID); err != nil {
		return err
	}
	if m.OK {
		if m.Error != nil {
			return invalidf("a successful response must not carry an error")
		}
		if m.Result == nil {
			return nil
		}
		return requireOpaqueJSON("result", m.Result)
	}
	if m.Error == nil {
		return invalidf("a failed response requires an error")
	}
	if m.Result != nil {
		return invalidf("a failed response must not carry a result")
	}
	return m.Error.Validate()
}

// ServiceEventEnvelope pushes one update on a service subscription. Update is
// opaque to this layer, as Call is.
type ServiceEventEnvelope struct {
	Type           string       `cbor:"type"`
	SubscriptionID string       `cbor:"subscriptionId"`
	Update         cbor.RawItem `cbor:"update"`
}

func (*ServiceEventEnvelope) serverMessageV8() {}

func (m *ServiceEventEnvelope) Validate() error {
	if m == nil {
		return nilMember(m)
	}
	if err := requireLiteral("type", m.Type, "service_update"); err != nil {
		return err
	}
	if err := requireID("subscriptionId", m.SubscriptionID); err != nil {
		return err
	}
	return requireOpaqueJSON("update", m.Update)
}

// AttachmentEnvelope is an out-of-band update to this presentation's selected
// session route. A nil Attachment is pi's null: detached.
type AttachmentEnvelope struct {
	Type       string         `cbor:"type"`
	Attachment *SessionTarget `cbor:"attachment"`
}

func (*AttachmentEnvelope) serverMessageV8() {}

func (m *AttachmentEnvelope) Validate() error {
	if m == nil {
		return nilMember(m)
	}
	if err := requireLiteral("type", m.Type, "attachment"); err != nil {
		return err
	}
	if m.Attachment == nil {
		return nil
	}
	return m.Attachment.Validate()
}

// ServerMessageV8 is anything a server may send: *ServerHelloV8,
// *ServerHelloErrorV8, *ResponseEnvelopeV8, *ServiceEventEnvelope or
// *AttachmentEnvelope.
type ServerMessageV8 interface{ serverMessageV8() }

// Decoding. The RpcTarget union is registered beside its v1 siblings in
// unions.go; the rest of what the v8 envelopes need from the strict decoder is
// registered here so it is deleted with nothing else at the cutover.
func init() {
	registerUnion(decodeClientMessageV8)
	registerUnion(decodeServerMessageV8)
	registerUnion(decodeOpaque)
	registerUnion(decodeNullableSessionTarget)
}

// decodeSessionTarget decodes the session arm. It fills a SessionTarget by
// value: decoding into a *SessionTarget would route back through
// decodeNullableSessionTarget, which is registered for that pointer type.
func decodeSessionTarget(value any) (*SessionTarget, error) {
	var target SessionTarget
	if err := decodeInto(value, &target); err != nil {
		return nil, err
	}
	return &target, nil
}

// decodeRpcTarget resolves pi's Union([ServerTarget, SessionTarget]). TypeBox
// tries both arms; here the presence of sessionId picks one, and the strict
// decoder then rejects whatever does not fit it — {sessionId} alone lacks the
// session arm's serverId, {serverId, attachmentId} has a property the server
// arm does not declare.
func decodeRpcTarget(value any) (RpcTarget, error) {
	entries, ok := value.(map[string]any)
	if !ok {
		return nil, invalidf("expected an object")
	}
	if _, session := entries["sessionId"]; session {
		return decodeSessionTarget(value)
	}
	return decodeMember[*serverTarget](value)
}

// decodeNullableSessionTarget resolves pi's Union([SessionTarget, Null]) for
// AttachmentEnvelope.Attachment: null is a nil pointer, anything else must be
// a session target.
func decodeNullableSessionTarget(value any) (*SessionTarget, error) {
	if value == nil {
		return nil, nil
	}
	return decodeSessionTarget(value)
}

// decodeOpaque accepts an opaque payload only as the span cbor.DecodeRaw
// captured for it. Its JSON-ness is checked by the envelope's Validate, once
// for the decode and encode paths alike.
func decodeOpaque(value any) (cbor.RawItem, error) {
	raw, ok := value.(cbor.RawItem)
	if !ok {
		return nil, invalidf("opaque payload must be a cbor.RawItem, got %T; decode the frame with "+
			"cbor.DecodeRaw naming this key, which the v8 message decoders do", value)
	}
	return raw, nil
}

// Opaque payload keys per direction, captured as RawItem spans by DecodeRaw.
var (
	clientOpaqueKeys = []string{"call"}
	serverOpaqueKeys = []string{"result", "update"}
)

func decodeClientMessageV8(value any) (ClientMessageV8, error) {
	tag, _, err := jsonstrict.Discriminant(value, "type")
	if err != nil {
		return nil, err
	}
	switch tag {
	case "hello":
		return decodeMember[*ClientHelloV8](value)
	case "request":
		return decodeMember[*RequestEnvelopeV8](value)
	case "cancel":
		return decodeMember[*CancelEnvelope](value)
	default:
		return nil, invalidf("unknown client message type %q", tag)
	}
}

func decodeServerMessageV8(value any) (ServerMessageV8, error) {
	tag, _, err := jsonstrict.Discriminant(value, "type")
	if err != nil {
		return nil, err
	}
	switch tag {
	case "hello":
		return decodeMember[*ServerHelloV8](value)
	case "hello_error":
		return decodeMember[*ServerHelloErrorV8](value)
	case "response":
		return decodeMember[*ResponseEnvelopeV8](value)
	case "service_update":
		return decodeMember[*ServiceEventEnvelope](value)
	case "attachment":
		return decodeMember[*AttachmentEnvelope](value)
	default:
		return nil, invalidf("unknown server message type %q", tag)
	}
}

// ParseClientMessageV8 validates an already-decoded value as a v8 client
// message. The value is what cbor.DecodeRaw produces for a frame when asked to
// capture "call": an opaque payload reaches this layer as its wire bytes, never
// as a decoded tree.
func ParseClientMessageV8(value any) (ClientMessageV8, error) {
	message, err := decodeClientMessageV8(value)
	if err != nil {
		return nil, &ValidationError{Msg: "Invalid client protocol message: " + err.Error()}
	}
	return message, nil
}

// ParseServerMessageV8 validates an already-decoded value as a v8 server
// message, decoded by cbor.DecodeRaw capturing "result" and "update".
func ParseServerMessageV8(value any) (ServerMessageV8, error) {
	message, err := decodeServerMessageV8(value)
	if err != nil {
		return nil, &ValidationError{Msg: "Invalid server protocol message: " + err.Error()}
	}
	return message, nil
}

// EncodeClientMessageV8 validates and encodes one complete length-prefixed v8
// client message.
func EncodeClientMessageV8(message ClientMessageV8, opts *FrameOptions) ([]byte, error) {
	if message == nil {
		return nil, &ValidationError{Msg: "Invalid client protocol message: message is required"}
	}
	return encodeMessage(message, "client", opts)
}

// EncodeServerMessageV8 validates and encodes one complete length-prefixed v8
// server message.
func EncodeServerMessageV8(message ServerMessageV8, opts *FrameOptions) ([]byte, error) {
	if message == nil {
		return nil, &ValidationError{Msg: "Invalid server protocol message: message is required"}
	}
	return encodeMessage(message, "server", opts)
}

// messageDecoderV8 incrementally decodes and validates framed v8 messages of
// one direction. It is the v1 messageDecoder with cbor.DecodeRaw in place of
// cbor.Decode, so the opaque payloads come out as the spans they arrived in;
// it replaces the v1 one at the cutover.
//
// Like FrameDecoder it is poisoned by its first error: a peer that has sent one
// invalid frame has lost the right to be interpreted, and continuing would mean
// guessing which of its bytes were meant to be a message.
type messageDecoderV8[T any] struct {
	frames         *FrameDecoder
	kind           string
	maxFrameLength int
	opaqueKeys     []string
	parse          func(any) (T, error)
	failed         bool
}

func newMessageDecoderV8[T any](kind string, opaqueKeys []string, parse func(any) (T, error), opts *FrameOptions) (*messageDecoderV8[T], error) {
	frames, err := NewFrameDecoder(opts)
	if err != nil {
		return nil, err
	}
	maxFrameLength, err := resolveMaxFrameLength(opts)
	if err != nil {
		return nil, err
	}
	return &messageDecoderV8[T]{
		frames: frames, kind: kind, maxFrameLength: maxFrameLength, opaqueKeys: opaqueKeys, parse: parse,
	}, nil
}

// failedMessage explains a poisoned decoder. See the note on
// decoderFailedMessage in framing.go for why it says more than pi's.
func (d *messageDecoderV8[T]) failedMessage() string {
	return d.kind + " message decoder has failed: a peer that sent one invalid message " +
		"has lost the right to be interpreted, so discard this decoder and re-establish the connection"
}

func (d *messageDecoderV8[T]) push(chunk []byte) ([]T, error) {
	if d.failed {
		return nil, &ValidationError{Msg: d.failedMessage()}
	}
	frames, err := d.frames.Push(chunk)
	if err != nil {
		d.failed = true
		return nil, d.wrap(err)
	}
	messages := make([]T, 0, len(frames))
	for _, frame := range frames {
		value, err := cbor.DecodeRaw(frame, &cbor.Options{MaxByteLength: &d.maxFrameLength}, d.opaqueKeys...)
		if err != nil {
			d.failed = true
			return nil, d.wrap(err)
		}
		message, err := d.parse(value)
		if err != nil {
			d.failed = true
			return nil, err
		}
		messages = append(messages, message)
	}
	return messages, nil
}

func (d *messageDecoderV8[T]) end() error {
	if d.failed {
		return &ValidationError{Msg: d.failedMessage()}
	}
	if err := d.frames.End(); err != nil {
		d.failed = true
		return &ValidationError{
			Msg: fmt.Sprintf("Invalid %s protocol framing: %s", d.kind, boundedErrorMessage(err)),
		}
	}
	return nil
}

func (d *messageDecoderV8[T]) wrap(err error) error {
	return &ValidationError{Msg: fmt.Sprintf("Invalid %s protocol frame: %s", d.kind, boundedErrorMessage(err))}
}

// ClientMessageDecoderV8 incrementally decodes framed v8 client messages. It is
// not safe for concurrent use; drive it from the goroutine reading the
// connection.
type ClientMessageDecoderV8 struct {
	inner *messageDecoderV8[ClientMessageV8]
}

// NewClientMessageDecoderV8 returns a decoder bounded by opts.
func NewClientMessageDecoderV8(opts *FrameOptions) (*ClientMessageDecoderV8, error) {
	inner, err := newMessageDecoderV8("client", clientOpaqueKeys, ParseClientMessageV8, opts)
	if err != nil {
		return nil, err
	}
	return &ClientMessageDecoderV8{inner: inner}, nil
}

// Push consumes a chunk and returns every message it completed.
func (d *ClientMessageDecoderV8) Push(chunk []byte) ([]ClientMessageV8, error) {
	return d.inner.push(chunk)
}

// End asserts the stream finished on a frame boundary.
func (d *ClientMessageDecoderV8) End() error { return d.inner.end() }

// ServerMessageDecoderV8 incrementally decodes framed v8 server messages. It is
// not safe for concurrent use; drive it from the goroutine reading the
// connection.
type ServerMessageDecoderV8 struct {
	inner *messageDecoderV8[ServerMessageV8]
}

// NewServerMessageDecoderV8 returns a decoder bounded by opts.
func NewServerMessageDecoderV8(opts *FrameOptions) (*ServerMessageDecoderV8, error) {
	inner, err := newMessageDecoderV8("server", serverOpaqueKeys, ParseServerMessageV8, opts)
	if err != nil {
		return nil, err
	}
	return &ServerMessageDecoderV8{inner: inner}, nil
}

// Push consumes a chunk and returns every message it completed.
func (d *ServerMessageDecoderV8) Push(chunk []byte) ([]ServerMessageV8, error) {
	return d.inner.push(chunk)
}

// End asserts the stream finished on a frame boundary.
func (d *ServerMessageDecoderV8) End() error { return d.inner.end() }
