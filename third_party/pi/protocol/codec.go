package protocol

import (
	"fmt"
	"unicode/utf8"

	"github.com/sky-valley/pi/protocol/cbor"
)

// IsSupportedVersion reports whether a peer's advertised version is one this
// package speaks.
func IsSupportedVersion(version int64) bool { return version == ProtocolVersion }

// maxEmbeddedErrorLength is pi's cap on an error message quoted inside another
// error, including the ellipsis that replaces what was cut.
const maxEmbeddedErrorLength = 500

// boundedErrorMessage caps an error before it is embedded in another error, so
// a hostile peer cannot make us build an unbounded string out of its input.
func boundedErrorMessage(err error) string {
	if err == nil {
		return "Unknown codec error"
	}
	msg := err.Error()
	if len(msg) <= maxEmbeddedErrorLength {
		return msg
	}
	// pi slices a UTF-16 string, where the cut cannot land inside a code unit.
	// Go slices bytes, and the text being cut can carry a peer's own property
	// name, so the cut is walked back to a rune boundary rather than emitting a
	// replacement character for a rune the peer chose.
	truncated := msg[:maxEmbeddedErrorLength-len("...")]
	for len(truncated) > 0 {
		if r, size := utf8.DecodeLastRuneInString(truncated); r != utf8.RuneError || size > 1 {
			break
		}
		truncated = truncated[:len(truncated)-1]
	}
	return truncated + "..."
}

func encodeMessage(message any, kind string, opts *FrameOptions) ([]byte, error) {
	if v, ok := message.(Validator); ok {
		if err := v.Validate(); err != nil {
			return nil, &ValidationError{Msg: fmt.Sprintf("Invalid %s protocol message: %s", kind, err.Error())}
		}
	}
	maxFrameLength, err := resolveMaxFrameLength(opts)
	if err != nil {
		return nil, err
	}
	payload, err := cbor.Encode(message, &cbor.Options{MaxByteLength: &maxFrameLength})
	if err != nil {
		return nil, &ValidationError{
			Msg: fmt.Sprintf("Unable to encode %s protocol message: %s", kind, boundedErrorMessage(err)),
		}
	}
	frame, err := EncodeFrame(payload)
	if err != nil {
		return nil, &ValidationError{
			Msg: fmt.Sprintf("Unable to encode %s protocol message: %s", kind, boundedErrorMessage(err)),
		}
	}
	if err := AssertCompleteFrame(frame, &FrameOptions{MaxFrameLength: &maxFrameLength}); err != nil {
		return nil, &ValidationError{
			Msg: fmt.Sprintf("Unable to encode %s protocol message: %s", kind, boundedErrorMessage(err)),
		}
	}
	return frame, nil
}

// EncodeClientMessage validates and encodes one complete length-prefixed client
// message.
func EncodeClientMessage(message ClientMessage, opts *FrameOptions) ([]byte, error) {
	if message == nil {
		return nil, &ValidationError{Msg: "Invalid client protocol message: message is required"}
	}
	return encodeMessage(message, "client", opts)
}

// EncodeServerMessage validates and encodes one complete length-prefixed server
// message.
func EncodeServerMessage(message ServerMessage, opts *FrameOptions) ([]byte, error) {
	if message == nil {
		return nil, &ValidationError{Msg: "Invalid server protocol message: message is required"}
	}
	return encodeMessage(message, "server", opts)
}

// messageDecoder incrementally decodes and validates framed messages of one
// direction.
//
// Like FrameDecoder it is poisoned by its first error: a peer that has sent one
// invalid frame has lost the right to be interpreted, and continuing would mean
// guessing which of its bytes were meant to be a message.
type messageDecoder[T any] struct {
	frames         *FrameDecoder
	kind           string
	maxFrameLength int
	parse          func(any) (T, error)
	failed         bool
}

func newMessageDecoder[T any](kind string, parse func(any) (T, error), opts *FrameOptions) (*messageDecoder[T], error) {
	frames, err := NewFrameDecoder(opts)
	if err != nil {
		return nil, err
	}
	maxFrameLength, err := resolveMaxFrameLength(opts)
	if err != nil {
		return nil, err
	}
	return &messageDecoder[T]{frames: frames, kind: kind, maxFrameLength: maxFrameLength, parse: parse}, nil
}

// failedMessage explains a poisoned decoder. See the note on
// decoderFailedMessage in framing.go for why it says more than pi's.
func (d *messageDecoder[T]) failedMessage() string {
	return d.kind + " message decoder has failed: a peer that sent one invalid message " +
		"has lost the right to be interpreted, so discard this decoder and re-establish the connection"
}

func (d *messageDecoder[T]) push(chunk []byte) ([]T, error) {
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
		value, err := cbor.Decode(frame, &cbor.Options{MaxByteLength: &d.maxFrameLength})
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

func (d *messageDecoder[T]) end() error {
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

func (d *messageDecoder[T]) wrap(err error) error {
	return &ValidationError{Msg: fmt.Sprintf("Invalid %s protocol frame: %s", d.kind, boundedErrorMessage(err))}
}

// ClientMessageDecoder incrementally decodes framed client messages. It is not
// safe for concurrent use; drive it from the goroutine reading the connection.
type ClientMessageDecoder struct {
	inner *messageDecoder[ClientMessage]
}

// NewClientMessageDecoder returns a decoder bounded by opts.
func NewClientMessageDecoder(opts *FrameOptions) (*ClientMessageDecoder, error) {
	inner, err := newMessageDecoder("client", ParseClientMessage, opts)
	if err != nil {
		return nil, err
	}
	return &ClientMessageDecoder{inner: inner}, nil
}

// Push consumes a chunk and returns every message it completed.
func (d *ClientMessageDecoder) Push(chunk []byte) ([]ClientMessage, error) {
	return d.inner.push(chunk)
}

// End asserts the stream finished on a frame boundary.
func (d *ClientMessageDecoder) End() error { return d.inner.end() }

// ServerMessageDecoder incrementally decodes framed server messages. It is not
// safe for concurrent use; drive it from the goroutine reading the connection.
type ServerMessageDecoder struct {
	inner *messageDecoder[ServerMessage]
}

// NewServerMessageDecoder returns a decoder bounded by opts.
func NewServerMessageDecoder(opts *FrameOptions) (*ServerMessageDecoder, error) {
	inner, err := newMessageDecoder("server", ParseServerMessage, opts)
	if err != nil {
		return nil, err
	}
	return &ServerMessageDecoder{inner: inner}, nil
}

// Push consumes a chunk and returns every message it completed.
func (d *ServerMessageDecoder) Push(chunk []byte) ([]ServerMessage, error) {
	return d.inner.push(chunk)
}

// End asserts the stream finished on a frame boundary.
func (d *ServerMessageDecoder) End() error { return d.inner.end() }
