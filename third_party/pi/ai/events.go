package ai

import "encoding/json"

// EventType is the discriminator for AssistantMessageEvent.
type EventType string

const (
	EventStart         EventType = "start"
	EventTextStart     EventType = "text_start"
	EventTextDelta     EventType = "text_delta"
	EventTextEnd       EventType = "text_end"
	EventThinkingStart EventType = "thinking_start"
	EventThinkingDelta EventType = "thinking_delta"
	EventThinkingEnd   EventType = "thinking_end"
	EventToolCallStart EventType = "toolcall_start"
	EventToolCallDelta EventType = "toolcall_delta"
	EventToolCallEnd   EventType = "toolcall_end"
	EventDone          EventType = "done"
	EventError         EventType = "error"
)

// AssistantMessageEvent is one event in the streaming protocol. It is a flat
// struct (Go has no discriminated unions) carrying the union of fields used by
// the variants documented in pi's AssistantMessageEvent.
//
// The protocol (pi types.ts AssistantMessageEvent, rewritten by 5c6655e76 and
// amended by 8b5899dce): a successful stream emits "start" before any partial
// update and terminates with "done". A stream may terminate directly with
// "error" when request setup fails before generation starts — no "start" at
// all; after "start", failures also terminate with "error" (final message with
// stopReason "error"/"aborted" and ErrorMessage). Updates and "done" never
// appear before "start".
//
// pi adds one clause the port renders differently: "Direct streamSimple() calls
// throw synchronously when request auth is missing." Under G3 (ai/stream.go:90)
// this port raises no setup failure out of band — a StreamSimple* call whose
// request auth is missing returns a stream carrying exactly that one terminal
// "error" event and no "start". What the port does keep from the eager throw is
// its precedence: the auth failure preempts every other setup failure on the
// StreamSimple path.
//
// Text and thinking blocks are empty when their *_start event is emitted and
// grow only through their *_delta events until the authoritative *_end.
// Redacted thinking may be complete at start and emit no deltas. Tool-call
// arguments at toolcall_start are provider-specific; toolcall_delta carries
// subsequent JSON updates. (Until 8b5899dce the contract was stricter — a tool
// call had to start EMPTY and stream its full raw JSON, and a provider holding
// complete arguments owed a cumulative delta prefix that parsed to them. The
// relaxation only widens what a provider may emit; a provider that still starts
// empty stays conformant.)
//
// Divergence: pi's `partial` is one shared live message that every event points
// at, so a consumer that buffers events and reads `partial` afterwards sees the
// final state. Port emitters pass a snapshot taken when the event was pushed
// (AssistantMessage.Clone), which is what makes buffered consumption and
// cross-goroutine reads safe here. Live-consuming code sees the same values on
// both sides; only a deferred read differs.
type AssistantMessageEvent struct {
	Type EventType `json:"type"`
	// ContentIndex is set for per-block events (text/thinking/toolcall). pi marks
	// it required on those events (types.ts:360-368); no omitempty so index 0 is
	// not dropped on serialize.
	ContentIndex int `json:"contentIndex"`
	// Delta is the incremental text for *_delta events. Required on delta events
	// in pi (types.ts:361,364,367).
	Delta string `json:"delta"`
	// Content is the finished text for text_end / thinking_end events. Required on
	// those events in pi (types.ts:362,365).
	Content string `json:"content"`
	// ToolCall is the finished tool call for toolcall_end events.
	ToolCall *ToolCall `json:"toolCall,omitempty"`
	// Partial is the in-progress assistant message (all non-terminal events).
	Partial *AssistantMessage `json:"partial,omitempty"`
	// Reason is the stop reason for done/error events. Required on those events in
	// pi (types.ts:369-370).
	Reason StopReason `json:"reason"`
	// Message is the final assistant message for "done" events.
	Message *AssistantMessage `json:"message,omitempty"`
	// Error is the final assistant message for "error" events.
	Error *AssistantMessage `json:"error,omitempty"`
}

// MarshalJSON serializes the event with exactly the fields pi's corresponding
// union variant carries (types.ts AssistantMessageEvent):
//
//	start                                  {type, partial}
//	text_start/thinking_start/toolcall_start {type, contentIndex, partial}
//	text_delta/thinking_delta/toolcall_delta {type, contentIndex, delta, partial}
//	text_end/thinking_end                  {type, contentIndex, content, partial}
//	toolcall_end                           {type, contentIndex, toolCall, partial}
//	done                                   {type, reason, message}
//	error                                  {type, reason, error}
//
// Fields a variant requires are always emitted (contentIndex:0 included);
// fields the variant lacks are never emitted (no spurious "reason":"" on
// start, no "contentIndex":0 on done, ...).
func (e AssistantMessageEvent) MarshalJSON() ([]byte, error) {
	switch e.Type {
	case EventStart:
		return json.Marshal(struct {
			Type    EventType         `json:"type"`
			Partial *AssistantMessage `json:"partial"`
		}{e.Type, e.Partial})
	case EventTextStart, EventThinkingStart, EventToolCallStart:
		return json.Marshal(struct {
			Type         EventType         `json:"type"`
			ContentIndex int               `json:"contentIndex"`
			Partial      *AssistantMessage `json:"partial"`
		}{e.Type, e.ContentIndex, e.Partial})
	case EventTextDelta, EventThinkingDelta, EventToolCallDelta:
		return json.Marshal(struct {
			Type         EventType         `json:"type"`
			ContentIndex int               `json:"contentIndex"`
			Delta        string            `json:"delta"`
			Partial      *AssistantMessage `json:"partial"`
		}{e.Type, e.ContentIndex, e.Delta, e.Partial})
	case EventTextEnd, EventThinkingEnd:
		return json.Marshal(struct {
			Type         EventType         `json:"type"`
			ContentIndex int               `json:"contentIndex"`
			Content      string            `json:"content"`
			Partial      *AssistantMessage `json:"partial"`
		}{e.Type, e.ContentIndex, e.Content, e.Partial})
	case EventToolCallEnd:
		return json.Marshal(struct {
			Type         EventType         `json:"type"`
			ContentIndex int               `json:"contentIndex"`
			ToolCall     *ToolCall         `json:"toolCall"`
			Partial      *AssistantMessage `json:"partial"`
		}{e.Type, e.ContentIndex, e.ToolCall, e.Partial})
	case EventDone:
		return json.Marshal(struct {
			Type    EventType         `json:"type"`
			Reason  StopReason        `json:"reason"`
			Message *AssistantMessage `json:"message"`
		}{e.Type, e.Reason, e.Message})
	case EventError:
		return json.Marshal(struct {
			Type   EventType         `json:"type"`
			Reason StopReason        `json:"reason"`
			Error  *AssistantMessage `json:"error"`
		}{e.Type, e.Reason, e.Error})
	default:
		// Unknown discriminator: fall back to the flat struct form.
		type alias AssistantMessageEvent
		return json.Marshal(alias(e))
	}
}

// AssistantMessageEventStream is an EventStream specialized for the assistant
// message protocol. The terminal event ("done" or "error") yields the final
// AssistantMessage.
type AssistantMessageEventStream = EventStream[AssistantMessageEvent, *AssistantMessage]

// NewAssistantMessageEventStream creates an AssistantMessageEventStream.
func NewAssistantMessageEventStream() *AssistantMessageEventStream {
	return NewEventStream(
		func(e AssistantMessageEvent) bool {
			return e.Type == EventDone || e.Type == EventError
		},
		func(e AssistantMessageEvent) *AssistantMessage {
			switch e.Type {
			case EventDone:
				return e.Message
			case EventError:
				return e.Error
			default:
				return nil
			}
		},
	)
}
