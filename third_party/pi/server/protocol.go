package server

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"time"

	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/protocol"
	"github.com/sky-valley/pi/protocol/cbor"
)

// This file is the bridge between pi's execution types (the ai package) and the
// protocol's wire types. It is the only place a value crosses that boundary, so
// it is also the only place that decides what an execution detail is allowed to
// tell a client.
//
// DIVERGENCE (deliberate): pi asserts the field-by-field correspondence between
// the two type sets with compile-time Assert<ExactKeys<...>> helpers, so adding
// a field upstream breaks this file's compilation. Go's type system cannot
// express that. The obligation is unchanged — a new field in ai must be either
// mapped or consciously withheld — but it is carried by the port's review
// process rather than by the compiler.

// maxDetailDepth bounds recursion through a value being converted. pi is
// protected by cycle detection alone; Go values reached through reflection can
// also be deeply nested without a cycle, and a stack overflow is not a
// recoverable error.
const maxDetailDepth = 256

// ToProtocolJSONValue validates and copies a value from an execution boundary
// into the protocol's JSON-compatible subset. Anything it cannot represent
// exactly is an error rather than a lossy substitution — this feeds tool input,
// where a silent change of meaning is worse than a failed turn.
//
// An ai.OrderedObject is copied to a cbor.OrderedObject, so a value that
// arrived with its authored key order reaches the wire in that order. pi gets
// this for free: it walks a JS object with Object.entries and assigns into a
// plain object, both of which keep insertion order, and its CBOR encoder then
// writes Object.keys. A Go map cannot carry the order at all.
//
// DIVERGENCE (deliberate): pi additionally rejects sparse arrays and
// `undefined`, neither of which exists in Go. A Go nil inside a slice is a JSON
// null and is accepted, which is what pi does with an explicit null too.
func ToProtocolJSONValue(value any) (any, error) {
	return toProtocolJSONValue(reflect.ValueOf(value), map[uintptr]struct{}{}, 0)
}

var orderedObjectType = reflect.TypeOf(ai.OrderedObject(nil))

func toProtocolJSONValue(v reflect.Value, seen map[uintptr]struct{}, depth int) (any, error) {
	if depth > maxDetailDepth {
		return nil, errors.New("Protocol JSON values must not be nested this deeply")
	}
	v, ok, err := deref(v)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}

	// An ordered object is a slice by construction but an object in pi, so it
	// is recognised before the kind switch would copy it as an array.
	if v.Type() == orderedObjectType {
		release, err := enter(v, seen)
		if err != nil {
			return nil, err
		}
		defer release()
		fields := v.Interface().(ai.OrderedObject)
		out := make(cbor.OrderedObject, len(fields))
		for i, field := range fields {
			entry, err := toProtocolJSONValue(reflect.ValueOf(field.Value), seen, depth+1)
			if err != nil {
				return nil, err
			}
			out[i] = cbor.OrderedField{Key: field.Key, Value: entry}
		}
		return out, nil
	}

	switch v.Kind() {
	case reflect.Bool:
		return v.Bool(), nil
	case reflect.String:
		return v.String(), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(v.Int()), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return float64(v.Uint()), nil
	case reflect.Float32, reflect.Float64:
		f := v.Float()
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, errors.New("Protocol JSON numbers must be finite")
		}
		return f, nil
	case reflect.Slice, reflect.Array:
		release, err := enter(v, seen)
		if err != nil {
			return nil, err
		}
		defer release()
		out := make([]any, v.Len())
		for i := range out {
			entry, err := toProtocolJSONValue(v.Index(i), seen, depth+1)
			if err != nil {
				return nil, err
			}
			out[i] = entry
		}
		return out, nil
	case reflect.Map:
		if v.Type().Key().Kind() != reflect.String {
			return nil, fmt.Errorf("Protocol JSON objects must be keyed by strings, got %s", v.Type())
		}
		release, err := enter(v, seen)
		if err != nil {
			return nil, err
		}
		defer release()
		out := make(map[string]any, v.Len())
		iter := v.MapRange()
		for iter.Next() {
			entry, err := toProtocolJSONValue(iter.Value(), seen, depth+1)
			if err != nil {
				return nil, err
			}
			out[iter.Key().String()] = entry
		}
		return out, nil
	default:
		return nil, fmt.Errorf("Unsupported protocol JSON value: %s", v.Kind())
	}
}

// SanitizeProtocolDetails lossily converts diagnostic tool details into the
// protocol's JSON subset. Details never affect execution semantics, so a value
// that cannot be represented is coerced or dropped rather than failing the
// turn.
func SanitizeProtocolDetails(value any) *any {
	// A nil value is Go's "unset", which is pi's `undefined` — and pi omits the
	// property for undefined while keeping it for null. Nested nils still come
	// back present-and-null, matching sanitizeProtocolDetails's own contract.
	if value == nil {
		return nil
	}
	result, present := sanitizeProtocolDetails(reflect.ValueOf(value), map[uintptr]struct{}{}, 0)
	if !present {
		return nil
	}
	return &result
}

// sanitizeProtocolDetails reports the sanitized value and whether it is present
// at all — the second result is pi's `undefined`, which is dropped from objects
// and becomes null inside arrays.
func sanitizeProtocolDetails(v reflect.Value, seen map[uintptr]struct{}, depth int) (any, bool) {
	if depth > maxDetailDepth {
		return "[MaxDepth]", true
	}
	v, ok, err := deref(v)
	if err != nil {
		return "[Circular]", true
	}
	if !ok {
		return nil, true
	}
	if t, isTime := timeValue(v); isTime {
		return t.UTC().Format("2006-01-02T15:04:05.000Z"), true
	}

	switch v.Kind() {
	case reflect.Bool:
		return v.Bool(), true
	case reflect.String:
		return v.String(), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(v.Int()), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return float64(v.Uint()), true
	case reflect.Float32, reflect.Float64:
		f := v.Float()
		if math.IsNaN(f) {
			return "NaN", true
		}
		if math.IsInf(f, 1) {
			return "Infinity", true
		}
		if math.IsInf(f, -1) {
			return "-Infinity", true
		}
		return f, true
	case reflect.Func, reflect.Chan, reflect.UnsafePointer:
		// pi drops functions and symbols; these are their nearest analogue.
		return nil, false
	case reflect.Slice, reflect.Array:
		release, err := enter(v, seen)
		if err != nil {
			return "[Circular]", true
		}
		defer release()
		out := make([]any, v.Len())
		for i := range out {
			entry, present := sanitizeProtocolDetails(v.Index(i), seen, depth+1)
			if !present {
				entry = nil
			}
			out[i] = entry
		}
		return out, true
	case reflect.Map:
		if v.Type().Key().Kind() != reflect.String {
			return fmt.Sprint(v.Interface()), true
		}
		release, err := enter(v, seen)
		if err != nil {
			return "[Circular]", true
		}
		defer release()
		out := map[string]any{}
		iter := v.MapRange()
		for iter.Next() {
			entry, present := sanitizeProtocolDetails(iter.Value(), seen, depth+1)
			if present {
				out[iter.Key().String()] = entry
			}
		}
		return out, true
	case reflect.Struct:
		// A struct is Go's plain object. Only exported fields are reachable,
		// which is the same visibility rule Object.entries applies.
		out := map[string]any{}
		t := v.Type()
		for i := range t.NumField() {
			field := t.Field(i)
			if !field.IsExported() {
				continue
			}
			entry, present := sanitizeProtocolDetails(v.Field(i), seen, depth+1)
			if present {
				out[field.Name] = entry
			}
		}
		return out, true
	default:
		return fmt.Sprint(v.Interface()), true
	}
}

// deref unwraps interfaces and pointers, reporting false for anything that is
// or resolves to nil.
//
// The chain is bounded because a pointer can refer to itself — `var x any; x =
// &x` walks interface to pointer to interface forever. enter's cycle detection
// cannot see that one: it only marks slices and maps, and this chain reaches
// neither. A chain this long is a cycle whatever else it might be, so it is
// reported as one; pi has no value that can express it.
func deref(v reflect.Value) (reflect.Value, bool, error) {
	for range maxDetailDepth {
		if !v.IsValid() {
			return v, false, nil
		}
		switch v.Kind() {
		case reflect.Interface, reflect.Pointer:
			if v.IsNil() {
				return v, false, nil
			}
			v = v.Elem()
		default:
			return v, true, nil
		}
	}
	return v, false, errors.New("Protocol JSON values must not contain circular references")
}

// enter marks a reference value as being walked, so a cycle through it is
// caught, and returns the release that unmarks it.
func enter(v reflect.Value, seen map[uintptr]struct{}) (func(), error) {
	if v.Kind() == reflect.Array || (v.Kind() == reflect.Slice && v.IsNil()) {
		return func() {}, nil
	}
	key := v.Pointer()
	if key == 0 {
		return func() {}, nil
	}
	if _, ok := seen[key]; ok {
		return nil, errors.New("Protocol JSON values must not contain circular references")
	}
	seen[key] = struct{}{}
	return func() { delete(seen, key) }, nil
}

func timeValue(v reflect.Value) (time.Time, bool) {
	if v.Type() == reflect.TypeOf(time.Time{}) {
		return v.Interface().(time.Time), true
	}
	return time.Time{}, false
}

func nonNegativeInt(value int) int64 {
	if value < 0 {
		return 0
	}
	return int64(value)
}

// nonNegativeFloat is pi's nonNegativeNumber: a non-finite cost is zero, not an
// error, because a cost is a report rather than an instruction.
func nonNegativeFloat(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return 0
	}
	return value
}

func identifier(value, label string) (string, error) {
	if value == "" {
		return "", fmt.Errorf("%s must be a non-empty string", label)
	}
	return value, nil
}

// maxSafeInteger is JavaScript's Number.MAX_SAFE_INTEGER. A protocol timestamp
// larger than this survives Go's int64 but not the peer's number, so pi refuses
// it and so does this.
const maxSafeInteger int64 = 1<<53 - 1

func protocolTimestamp(value int64) (int64, error) {
	if value < 0 || value > maxSafeInteger {
		return 0, errors.New("Protocol timestamps must be non-negative integers")
	}
	return value, nil
}

// ToProtocolUsage maps token accounting onto the wire.
//
// DIVERGENCE (deliberate): pi emits `reasoning` only when the provider reported
// it, which it tracks with `undefined`. ai.Usage.Reasoning is a plain int, so
// zero and unreported are the same value; a zero is treated as unreported and
// omitted, matching the choice ai/types.go already made for session files.
func ToProtocolUsage(usage *ai.Usage) *protocol.Usage {
	if usage == nil {
		return nil
	}
	result := &protocol.Usage{
		Input:       nonNegativeInt(usage.Input),
		Output:      nonNegativeInt(usage.Output),
		CacheRead:   nonNegativeInt(usage.CacheRead),
		CacheWrite:  nonNegativeInt(usage.CacheWrite),
		TotalTokens: nonNegativeInt(usage.TotalTokens),
		Cost: protocol.UsageCost{
			Input:      nonNegativeFloat(usage.Cost.Input),
			Output:     nonNegativeFloat(usage.Cost.Output),
			CacheRead:  nonNegativeFloat(usage.Cost.CacheRead),
			CacheWrite: nonNegativeFloat(usage.Cost.CacheWrite),
			Total:      nonNegativeFloat(usage.Cost.Total),
		},
	}
	if usage.Reasoning != 0 {
		reasoning := nonNegativeInt(usage.Reasoning)
		result.Reasoning = &reasoning
	}
	return result
}

// ToProtocolModelMetadata publishes one model to clients. authenticated says
// whether the server holds usable credentials for it.
func ToProtocolModelMetadata(model *ai.Model, authenticated bool) (*protocol.ModelMetadata, error) {
	if model == nil {
		return nil, errors.New("Model must not be nil")
	}
	provider, err := identifier(model.Provider, "Model provider")
	if err != nil {
		return nil, err
	}
	id, err := identifier(model.ID, "Model id")
	if err != nil {
		return nil, err
	}
	name, err := identifier(model.Name, "Model name")
	if err != nil {
		return nil, err
	}
	api, err := identifier(model.Api, "Model API")
	if err != nil {
		return nil, err
	}

	input := make([]protocol.ModelInputModality, 0, len(model.Input))
	for _, modality := range model.Input {
		input = append(input, protocol.ModelInputModality(modality))
	}
	levels := ai.GetSupportedThinkingLevels(model)
	supported := make([]protocol.ThinkingLevel, 0, len(levels))
	for _, level := range levels {
		supported = append(supported, protocol.ThinkingLevel(level))
	}

	return &protocol.ModelMetadata{
		Provider:      provider,
		ID:            id,
		Name:          name,
		API:           api,
		Reasoning:     model.Reasoning,
		Input:         input,
		ContextWindow: max(1, int64(model.ContextWindow)),
		MaxTokens:     max(1, int64(model.MaxTokens)),
		Cost: protocol.ModelCost{
			Input:      nonNegativeFloat(model.Cost.Input),
			Output:     nonNegativeFloat(model.Cost.Output),
			CacheRead:  nonNegativeFloat(model.Cost.CacheRead),
			CacheWrite: nonNegativeFloat(model.Cost.CacheWrite),
		},
		SupportedThinkingLevels: supported,
		Authenticated:           authenticated,
	}, nil
}

// ToProtocolUserMessage maps one user turn onto a transcript item.
func ToProtocolUserMessage(message ai.UserMessage, id string) (*protocol.UserTranscriptItem, error) {
	itemID, err := identifier(id, "Transcript item id")
	if err != nil {
		return nil, err
	}
	content, err := toProtocolTextOrImage(message.Content)
	if err != nil {
		return nil, err
	}
	timestamp, err := protocolTimestamp(message.Timestamp)
	if err != nil {
		return nil, err
	}
	return &protocol.UserTranscriptItem{
		ID:        itemID,
		Role:      "user",
		Content:   content,
		Timestamp: timestamp,
	}, nil
}

func toProtocolTextOrImage(content ai.ContentList) ([]protocol.Content, error) {
	out := make([]protocol.Content, 0, len(content))
	for _, part := range content {
		switch block := part.(type) {
		case ai.TextContent:
			out = append(out, &protocol.TextContent{Type: "text", Text: block.Text})
		case ai.ImageContent:
			out = append(out, &protocol.ImageContent{Type: "image", Data: block.Data, MimeType: block.MimeType})
		default:
			return nil, fmt.Errorf("Unsupported content block for this role: %T", part)
		}
	}
	return out, nil
}

func toProtocolAssistantContent(content ai.ContentList) ([]protocol.Content, error) {
	out := make([]protocol.Content, 0, len(content))
	for _, part := range content {
		switch block := part.(type) {
		case ai.TextContent:
			out = append(out, &protocol.TextContent{Type: "text", Text: block.Text})
		case ai.ThinkingContent:
			// DIVERGENCE (deliberate): pi emits `redacted: false` when the
			// provider said so explicitly and omits it when it said nothing.
			// ai.ThinkingContent.Redacted is a plain bool, so false and unsaid
			// are one value; it is emitted only when true, which is the only
			// case a client acts on.
			thinking := &protocol.ThinkingContent{Type: "thinking", Thinking: block.Thinking}
			if block.Redacted {
				redacted := true
				thinking.Redacted = &redacted
			}
			out = append(out, thinking)
		case ai.ToolCall:
			callID, err := identifier(block.ID, "Tool call id")
			if err != nil {
				return nil, err
			}
			callName, err := identifier(block.Name, "Tool call name")
			if err != nil {
				return nil, err
			}
			input, err := ToProtocolJSONValue(block.OrderedArguments())
			if err != nil {
				return nil, err
			}
			out = append(out, &protocol.ToolCallContent{
				Type:       "toolCall",
				ToolCallID: callID,
				ToolName:   callName,
				Input:      input,
			})
		default:
			return nil, fmt.Errorf("Unsupported assistant content block: %T", part)
		}
	}
	return out, nil
}

// ToProtocolAssistantMessage maps one assistant turn onto a transcript item,
// deriving the item's status from the message's stop reason.
//
// DIVERGENCE (deliberate): pi rejects an assistant error whose errorMessage is
// the empty string, and separately allows an empty one on the aborted arm.
// ai.AssistantMessage.ErrorMessage is a plain string, so absent and empty are
// the same value and neither the rejection nor the empty aborted message is
// expressible. An empty message is treated as absent on both arms, which is the
// only reading that never puts a value on the wire the schema would refuse.
func ToProtocolAssistantMessage(
	message ai.AssistantMessage,
	id string,
) (*protocol.AssistantTranscriptItem, error) {
	itemID, err := identifier(id, "Transcript item id")
	if err != nil {
		return nil, err
	}
	content, err := toProtocolAssistantContent(message.Content)
	if err != nil {
		return nil, err
	}
	provider, err := identifier(message.Provider, "Assistant provider")
	if err != nil {
		return nil, err
	}
	modelID, err := identifier(message.Model, "Assistant model")
	if err != nil {
		return nil, err
	}
	timestamp, err := protocolTimestamp(message.Timestamp)
	if err != nil {
		return nil, err
	}

	item := &protocol.AssistantTranscriptItem{
		ID:        itemID,
		Role:      "assistant",
		Content:   content,
		Model:     protocol.ModelRef{Provider: provider, ID: modelID},
		Usage:     ToProtocolUsage(&message.Usage),
		Timestamp: timestamp,
	}
	if message.ResponseModel != "" {
		responseModel, err := identifier(message.ResponseModel, "Assistant response model")
		if err != nil {
			return nil, err
		}
		item.ResponseModel = &responseModel
	}

	stopReason := func(reason protocol.StopReason) *protocol.StopReason { return &reason }
	switch message.StopReason {
	case ai.StopPending:
		item.Status = protocol.AssistantStreaming
	case ai.StopStop:
		item.Status = protocol.AssistantComplete
		item.StopReason = stopReason(protocol.StopStop)
	case ai.StopLength:
		item.Status = protocol.AssistantComplete
		item.StopReason = stopReason(protocol.StopLength)
	case ai.StopToolUse:
		item.Status = protocol.AssistantComplete
		item.StopReason = stopReason(protocol.StopToolUse)
	case ai.StopError:
		item.Status = protocol.AssistantError
		item.StopReason = stopReason(protocol.StopError)
		if message.ErrorMessage != "" {
			errorMessage := message.ErrorMessage
			item.ErrorMessage = &errorMessage
		}
	case ai.StopDeferred:
		// Protocol v1 has no deferred transcript status, so the bridge refuses
		// the message rather than putting an unrepresentable stop reason on the
		// wire (pi 382aa641c, server/src/protocol.ts).
		return nil, errors.New("Deferred assistant messages are not supported by protocol v1" +
			"; redeem the handle with Models.FetchDeferred before bridging the turn")
	case ai.StopAborted:
		item.Status = protocol.AssistantAborted
		item.StopReason = stopReason(protocol.StopAborted)
		if message.ErrorMessage != "" {
			errorMessage := message.ErrorMessage
			item.ErrorMessage = &errorMessage
		}
	default:
		return nil, fmt.Errorf("Unknown assistant stop reason %q", string(message.StopReason))
	}
	return item, nil
}

// ToProtocolToolResultMessage maps one tool result onto a transcript item. call
// is the assistant's originating tool call; a result that does not match it is
// rejected rather than silently re-labelled.
func ToProtocolToolResultMessage(
	message ai.ToolResultMessage,
	id string,
	call ai.ToolCall,
) (*protocol.ToolTranscriptItem, error) {
	itemID, err := identifier(id, "Transcript item id")
	if err != nil {
		return nil, err
	}
	callID, err := identifier(call.ID, "Tool call id")
	if err != nil {
		return nil, err
	}
	callName, err := identifier(call.Name, "Tool call name")
	if err != nil {
		return nil, err
	}
	resultID, err := identifier(message.ToolCallID, "Tool result call id")
	if err != nil {
		return nil, err
	}
	if resultID != callID {
		return nil, fmt.Errorf("Tool result %s does not match tool call %s", resultID, callID)
	}
	resultName, err := identifier(message.ToolName, "Tool result name")
	if err != nil {
		return nil, err
	}
	if resultName != callName {
		return nil, fmt.Errorf("Tool result %s does not match tool call %s", resultName, callName)
	}
	input, err := ToProtocolJSONValue(call.OrderedArguments())
	if err != nil {
		return nil, err
	}
	content, err := toProtocolTextOrImage(message.Content)
	if err != nil {
		return nil, err
	}
	timestamp, err := protocolTimestamp(message.Timestamp)
	if err != nil {
		return nil, err
	}

	item := &protocol.ToolTranscriptItem{
		ID:         itemID,
		Role:       "tool",
		ToolCallID: callID,
		ToolName:   callName,
		Input:      input,
		Content:    content,
		Details:    SanitizeProtocolDetails(message.Details),
		Usage:      ToProtocolUsage(message.Usage),
		Timestamp:  timestamp,
		IsError:    message.IsError,
	}
	if message.IsError {
		item.Status = protocol.ToolError
	} else {
		item.Status = protocol.ToolComplete
	}
	return item, nil
}
