package protocol

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

const frameHeaderLength = 4

// DefaultMaxFrameLength is the default upper bound for one framed CBOR payload.
const DefaultMaxFrameLength = 16 * 1024 * 1024

// maxUint32 is the largest value the 4-byte length prefix can carry. It is
// uint64 because it does not fit in an int on a 32-bit build, and the wire
// length must be range-checked before it is narrowed to int.
const maxUint32 uint64 = 0xffff_ffff

// FrameError is a malformed or over-long frame — a peer's fault.
type FrameError struct{ Msg string }

func (e *FrameError) Error() string { return e.Msg }

// RangeError is an out-of-range framing limit: a caller bug, not a peer's, which
// is why it is a distinct type from FrameError. It mirrors the RangeError pi
// throws from resolveMaxFrameLength and encodeFrame, and the one cbor already
// raises for an invalid Options.
type RangeError struct{ Msg string }

func (e *RangeError) Error() string { return e.Msg }

// FrameOptions bounds a single frame. A nil MaxFrameLength takes the default.
type FrameOptions struct {
	MaxFrameLength *int
}

func resolveMaxFrameLength(o *FrameOptions) (int, error) {
	v := DefaultMaxFrameLength
	if o != nil && o.MaxFrameLength != nil {
		v = *o.MaxFrameLength
	}
	if v < 0 || uint64(v) > maxUint32 {
		return 0, &RangeError{Msg: fmt.Sprintf("maxFrameLength must be an integer between 0 and %d", maxUint32)}
	}
	return v, nil
}

// EncodeFrame prefixes a payload with its unsigned 32-bit big-endian length.
func EncodeFrame(payload []byte) ([]byte, error) {
	if uint64(len(payload)) > maxUint32 {
		return nil, &RangeError{
			Msg: fmt.Sprintf("frame payload of %d bytes exceeds the unsigned 32-bit length limit of %d; "+
				"split the payload across frames", len(payload), maxUint32),
		}
	}
	frame := make([]byte, frameHeaderLength+len(payload))
	binary.BigEndian.PutUint32(frame, uint32(len(payload)))
	copy(frame[frameHeaderLength:], payload)
	return frame, nil
}

// AssertCompleteFrame validates that bytes hold exactly one complete frame
// within the configured limit.
func AssertCompleteFrame(frame []byte, opts *FrameOptions) error {
	if len(frame) < frameHeaderLength {
		return &FrameError{Msg: "Frame does not contain a complete length prefix"}
	}
	length := int(binary.BigEndian.Uint32(frame))
	maxFrameLength, err := resolveMaxFrameLength(opts)
	if err != nil {
		return err
	}
	if length > maxFrameLength {
		return &FrameError{Msg: fmt.Sprintf("Frame length %d exceeds configured limit of %d", length, maxFrameLength)}
	}
	if len(frame) != frameHeaderLength+length {
		return &FrameError{Msg: "Frame must contain exactly one complete payload"}
	}
	return nil
}

type decoderState int

const (
	decoderOpen decoderState = iota
	decoderEnded
	decoderFailed
)

// decoderFailedMessage is returned by every call on a poisoned decoder.
//
// DIVERGENCE (deliberate): pi says only "Frame decoder has failed". A failed
// decoder is a dead end by construction — the stream position is unknown, so no
// later byte can be interpreted — and there is exactly one way forward, which
// the message now names instead of leaving the caller to retry into a permanent
// error. The text is local to the process; it never reaches a peer.
const decoderFailedMessage = "Frame decoder has failed: a framing error is unrecoverable by design, " +
	"so discard this decoder and re-establish the connection"

// FrameDecoder incrementally splits arbitrary byte chunks into length-prefixed
// payloads.
//
// The payload is accumulated in a bytes.Buffer that grows as data arrives,
// never by pre-allocating the length the peer declared. That is the same
// protection pi gets from its 64 KiB block list: a peer can announce a 16 MiB
// frame and send one byte without costing 16 MiB of memory.
//
// A FrameDecoder is not safe for concurrent use; drive it from a single
// connection-reading goroutine.
type FrameDecoder struct {
	header         [frameHeaderLength]byte
	headerLength   int
	maxFrameLength int
	payload        bytes.Buffer
	expected       int
	hasExpected    bool
	state          decoderState
}

// NewFrameDecoder returns a decoder bounded by opts.
func NewFrameDecoder(opts *FrameOptions) (*FrameDecoder, error) {
	maxFrameLength, err := resolveMaxFrameLength(opts)
	if err != nil {
		return nil, err
	}
	return &FrameDecoder{maxFrameLength: maxFrameLength}, nil
}

// Push consumes a chunk and returns every complete payload it completed. The
// returned payloads do not alias chunk.
//
// A decoder that returns an error is poisoned: every later call fails. Framing
// errors are unrecoverable by construction — once the stream position is
// unknown, no later byte can be interpreted.
func (d *FrameDecoder) Push(chunk []byte) ([][]byte, error) {
	switch d.state {
	case decoderEnded:
		return nil, &FrameError{Msg: "Frame decoder has ended"}
	case decoderFailed:
		return nil, &FrameError{Msg: decoderFailedMessage}
	}

	var frames [][]byte
	offset := 0
	for offset < len(chunk) {
		if !d.hasExpected {
			headerBytes := min(frameHeaderLength-d.headerLength, len(chunk)-offset)
			copy(d.header[d.headerLength:], chunk[offset:offset+headerBytes])
			d.headerLength += headerBytes
			offset += headerBytes
			if d.headerLength < frameHeaderLength {
				continue
			}

			// Range-check as uint64 before narrowing: on a 32-bit build a
			// declared length >= 2^31 would go negative as an int, slip past
			// the limit check, and panic on the negative slice bound below.
			declared := uint64(binary.BigEndian.Uint32(d.header[:]))
			d.headerLength = 0
			if declared > uint64(d.maxFrameLength) {
				return nil, d.fail(fmt.Sprintf("Frame length %d exceeds configured limit of %d",
					declared, d.maxFrameLength))
			}
			frameLength := int(declared)
			if frameLength == 0 {
				frames = append(frames, []byte{})
				continue
			}
			d.expected = frameLength
			d.hasExpected = true
			d.payload.Reset()
		}

		remaining := d.expected - d.payload.Len()
		available := min(remaining, len(chunk)-offset)
		d.payload.Write(chunk[offset : offset+available])
		offset += available

		if d.payload.Len() == d.expected {
			payload := make([]byte, d.expected)
			copy(payload, d.payload.Bytes())
			frames = append(frames, payload)
			d.payload.Reset()
			d.expected = 0
			d.hasExpected = false
		}
	}
	return frames, nil
}

// End asserts the stream finished on a frame boundary.
func (d *FrameDecoder) End() error {
	switch d.state {
	case decoderEnded:
		return &FrameError{Msg: "Frame decoder has ended"}
	case decoderFailed:
		return &FrameError{Msg: decoderFailedMessage}
	}
	if d.headerLength != 0 || d.hasExpected {
		return d.fail("Truncated frame at end of stream")
	}
	d.state = decoderEnded
	return nil
}

func (d *FrameDecoder) fail(message string) error {
	d.state = decoderFailed
	d.headerLength = 0
	d.payload.Reset()
	d.expected = 0
	d.hasExpected = false
	return &FrameError{Msg: message}
}
