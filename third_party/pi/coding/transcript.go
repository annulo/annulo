package coding

import (
	"encoding/json"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/sky-valley/pi/protocol"
)

// TranscriptState is a client-side projection of one session's transcript: the
// authoritative snapshot the server sent, plus the streaming progress laid over
// it. It is the port of pi's TranscriptState
// (packages/coding-agent/src/client/transcript.ts).
//
// The snapshot always wins. Progress is an optimization that lets a viewer
// render a turn as it is produced; the moment a newer snapshot arrives the
// projection is rebuilt from it and every accumulated delta is discarded.
//
// A TranscriptState is immutable. Every operation returns the next state — the
// receiver is never modified — so a value can be read without a lock while
// another goroutine folds the next event into it. Items handed in are deep
// copied on the way in, so a caller that keeps mutating what it passed cannot
// corrupt a projection.
type TranscriptState struct {
	snapshot        protocol.SessionSnapshot
	progressItems   map[string]protocol.TranscriptItem
	progressOrder   []string
	toolCallBuffers map[string]string
}

// NewTranscriptState starts a projection from an authoritative snapshot. It is
// pi's createTranscriptState.
func NewTranscriptState(snapshot *protocol.SessionSnapshot) *TranscriptState {
	return &TranscriptState{
		snapshot:        cloneSessionSnapshot(snapshot),
		progressItems:   map[string]protocol.TranscriptItem{},
		toolCallBuffers: map[string]string{},
	}
}

// Snapshot is the authoritative snapshot this projection is built on. The
// result must be treated as read-only: it is the projection's own copy, shared
// rather than cloned per call, exactly as pi shares its frozen record.
func (s *TranscriptState) Snapshot() *protocol.SessionSnapshot { return &s.snapshot }

// ApplySnapshot folds in a newer authoritative snapshot, discarding every
// projected delta. It is pi's applyTranscriptSnapshot.
//
// A lower revision for the same session is stale and ignored. A lower revision
// for a *different* session id is not: revisions count from the attachment, so
// switching sessions legitimately moves the counter backwards.
func (s *TranscriptState) ApplySnapshot(snapshot *protocol.SessionSnapshot) *TranscriptState {
	if s.snapshot.ID == snapshot.ID && snapshot.Revision < s.snapshot.Revision {
		return s
	}
	return NewTranscriptState(snapshot)
}

// ApplyProgress folds in one incremental update. It is pi's
// applyTranscriptProgress.
//
// A progress variant this port does not recognize leaves the state untouched,
// which is what pi does with one too: it reaches its delta branch by
// elimination, looks the unknown shape's absent message id up, finds nothing,
// and returns the state it was given.
func (s *TranscriptState) ApplyProgress(progress protocol.TranscriptProgress) *TranscriptState {
	switch typed := progress.(type) {
	case *protocol.ItemStartedProgress:
		return s.withProgressItem(s.toolCallBuffers, typed.Item)
	case *protocol.ItemUpdatedProgress:
		return s.withProgressItem(s.toolCallBuffers, typed.Item)
	case *protocol.ItemFinishedProgress:
		// The item is settled, so the partial tool arguments buffered for it are
		// spent: its final content carries the parsed input.
		return s.withProgressItem(s.forgetToolCallBuffers(transcriptItemID(typed.Item)), typed.Item)
	case *protocol.AssistantDeltaProgress:
		return s.applyAssistantDelta(typed)
	default:
		return s
	}
}

// Transcript renders the projection: the snapshot's transcript with any
// projected replacements applied, then the items progress introduced that the
// snapshot does not know about yet, then the steering messages the server has
// accepted but not yet folded into a turn. It is pi's selectTranscript.
func (s *TranscriptState) Transcript() []protocol.TranscriptItem {
	transcript := make([]protocol.TranscriptItem, 0, len(s.snapshot.Transcript)+len(s.progressOrder)+len(s.snapshot.QueuedSteer))
	seen := make(map[string]bool, cap(transcript))
	for _, item := range s.snapshot.Transcript {
		id := transcriptItemID(item)
		if projected, ok := s.progressItems[id]; ok {
			item = projected
		}
		transcript = append(transcript, item)
		seen[id] = true
	}
	for _, id := range s.progressOrder {
		if seen[id] {
			continue
		}
		if item, ok := s.progressItems[id]; ok {
			transcript = append(transcript, item)
			seen[id] = true
		}
	}
	for i := range s.snapshot.QueuedSteer {
		queued := &s.snapshot.QueuedSteer[i]
		if seen[queued.ID] {
			continue
		}
		transcript = append(transcript, queued)
		seen[queued.ID] = true
	}
	return transcript
}

// applyAssistantDelta appends a streamed fragment to one content block of one
// assistant item. A delta naming an item this projection has never seen, or one
// that is not an assistant turn, is dropped.
func (s *TranscriptState) applyAssistantDelta(delta *protocol.AssistantDeltaProgress) *TranscriptState {
	item, found := s.progressItems[delta.MessageID]
	if !found {
		for _, candidate := range s.snapshot.Transcript {
			if transcriptItemID(candidate) == delta.MessageID {
				item = candidate
				break
			}
		}
	}
	assistant, ok := item.(*protocol.AssistantTranscriptItem)
	if !ok {
		return s
	}

	buffers := s.toolCallBuffers
	content := make([]protocol.Content, len(assistant.Content))
	copy(content, assistant.Content)
	if delta.ContentIndex >= 0 && delta.ContentIndex < int64(len(content)) {
		part := content[delta.ContentIndex]
		switch delta.Kind {
		case protocol.DeltaText:
			if text, isText := part.(*protocol.TextContent); isText {
				next := *text
				next.Text += delta.Delta
				content[delta.ContentIndex] = &next
			}
		case protocol.DeltaThinking:
			if thinking, isThinking := part.(*protocol.ThinkingContent); isThinking {
				next := *thinking
				next.Thinking += delta.Delta
				content[delta.ContentIndex] = &next
			}
		case protocol.DeltaToolCall:
			if call, isCall := part.(*protocol.ToolCallContent); isCall {
				key := delta.MessageID + ":" + strconv.FormatInt(delta.ContentIndex, 10)
				// Only a string input is a partial argument prefix to resume
				// from; a parsed one came from a completed parse and restarting
				// on it would concatenate a rendering with a raw fragment.
				buffered, cached := s.toolCallBuffers[key]
				if !cached {
					buffered, _ = call.Input.(string)
				}
				buffered += delta.Delta
				buffers = cloneBuffers(s.toolCallBuffers)
				buffers[key] = buffered
				next := *call
				next.Input = parsePartialToolInput(buffered)
				content[delta.ContentIndex] = &next
			}
		}
	}

	updated := *assistant
	updated.Content = content
	// The item was built here out of values this projection already owns, so it
	// is recorded as-is: cloning it would copy the whole content slice and
	// deep-copy the tool input parsed one line above, once per streamed token.
	return s.withOwnedProgressItem(buffers, &updated)
}

// withProgressItem records the newest version of one item, deep copying it on
// the way in. It is pi's setProgressItem, and the clone is pi's structuredClone:
// the item comes from whoever produced the progress, and a producer that keeps
// mutating it must not be able to reach into the projection.
func (s *TranscriptState) withProgressItem(
	buffers map[string]string,
	item protocol.TranscriptItem,
) *TranscriptState {
	return s.withOwnedProgressItem(buffers, cloneTranscriptItem(item))
}

// withOwnedProgressItem records an item this projection already owns. Replacing
// an item keeps its original position, and a new one is appended to the
// projection order.
func (s *TranscriptState) withOwnedProgressItem(
	buffers map[string]string,
	item protocol.TranscriptItem,
) *TranscriptState {
	id := transcriptItemID(item)
	next := &TranscriptState{
		snapshot:        s.snapshot,
		progressItems:   cloneItems(s.progressItems),
		progressOrder:   s.progressOrder,
		toolCallBuffers: buffers,
	}
	if _, exists := s.progressItems[id]; !exists {
		next.progressOrder = append(slices.Clip(s.progressOrder), id)
	}
	next.progressItems[id] = item
	return next
}

// forgetToolCallBuffers drops every partial tool argument buffered for one item.
func (s *TranscriptState) forgetToolCallBuffers(itemID string) map[string]string {
	buffers := cloneBuffers(s.toolCallBuffers)
	prefix := itemID + ":"
	for key := range buffers {
		if strings.HasPrefix(key, prefix) {
			delete(buffers, key)
		}
	}
	return buffers
}

// cloneItems and cloneBuffers copy a projection's maps for the next state. They
// exist because maps.Clone answers nil with nil, and the zero TranscriptState is
// a value an exported type has to survive being handed: the next write would
// otherwise land on a nil map and panic.
func cloneItems(items map[string]protocol.TranscriptItem) map[string]protocol.TranscriptItem {
	if items == nil {
		return map[string]protocol.TranscriptItem{}
	}
	return maps.Clone(items)
}

func cloneBuffers(buffers map[string]string) map[string]string {
	if buffers == nil {
		return map[string]string{}
	}
	return maps.Clone(buffers)
}

// parsePartialToolInput renders a tool call's arguments as they stream in: the
// parsed value once the accumulated text is valid JSON, and the raw prefix until
// then. It is pi's parsePartialToolInput.
//
// pi additionally screens the parsed result through isJsonValue, whose only
// reachable rejection is a non-finite number — JSON.parse turns "1e999" into
// Infinity. encoding/json refuses that input outright, so the raw prefix is
// preserved by the same path, and no separate screen is needed.
func parsePartialToolInput(value string) any {
	var parsed any
	if err := json.Unmarshal([]byte(value), &parsed); err == nil {
		return parsed
	}
	return value
}

// transcriptItemID reads an item's id.
//
// DIVERGENCE (deliberate): protocol.TranscriptItem exposes only ItemRole, so the
// id is recovered by type switch here rather than by adding a method upstream —
// the protocol package is not this port's to change.
func transcriptItemID(item protocol.TranscriptItem) string {
	switch typed := item.(type) {
	case *protocol.UserTranscriptItem:
		return typed.ID
	case *protocol.AssistantTranscriptItem:
		return typed.ID
	case *protocol.ToolTranscriptItem:
		return typed.ID
	default:
		return ""
	}
}

// Deep copies below stand in for pi's structuredClone at the reducer's ingest
// points. Only ingest is copied, matching pi: a projection is insulated from the
// producer that handed it an item, not from a consumer that decides to mutate
// what Transcript returned.

func cloneSessionSnapshot(snapshot *protocol.SessionSnapshot) protocol.SessionSnapshot {
	clone := *snapshot
	clone.Name = clonePtr(snapshot.Name)
	if snapshot.Transcript != nil {
		clone.Transcript = make([]protocol.TranscriptItem, len(snapshot.Transcript))
		for i, item := range snapshot.Transcript {
			clone.Transcript[i] = cloneTranscriptItem(item)
		}
	}
	if snapshot.QueuedSteer != nil {
		clone.QueuedSteer = make([]protocol.UserTranscriptItem, len(snapshot.QueuedSteer))
		for i := range snapshot.QueuedSteer {
			clone.QueuedSteer[i] = *cloneUserItem(&snapshot.QueuedSteer[i])
		}
	}
	return clone
}

func cloneTranscriptItem(item protocol.TranscriptItem) protocol.TranscriptItem {
	switch typed := item.(type) {
	case *protocol.UserTranscriptItem:
		return cloneUserItem(typed)
	case *protocol.AssistantTranscriptItem:
		return cloneAssistantItem(typed)
	case *protocol.ToolTranscriptItem:
		return cloneToolItem(typed)
	default:
		return item
	}
}

func cloneUserItem(item *protocol.UserTranscriptItem) *protocol.UserTranscriptItem {
	clone := *item
	clone.Content = cloneContents(item.Content)
	return &clone
}

func cloneAssistantItem(item *protocol.AssistantTranscriptItem) *protocol.AssistantTranscriptItem {
	clone := *item
	clone.Content = cloneContents(item.Content)
	clone.ResponseModel = clonePtr(item.ResponseModel)
	clone.Usage = cloneUsage(item.Usage)
	clone.StopReason = clonePtr(item.StopReason)
	clone.ErrorMessage = clonePtr(item.ErrorMessage)
	return &clone
}

func cloneToolItem(item *protocol.ToolTranscriptItem) *protocol.ToolTranscriptItem {
	clone := *item
	clone.Content = cloneContents(item.Content)
	clone.Input = cloneJSONValue(item.Input)
	if item.Details != nil {
		details := cloneJSONValue(*item.Details)
		clone.Details = &details
	}
	clone.Usage = cloneUsage(item.Usage)
	return &clone
}

func cloneContents(content []protocol.Content) []protocol.Content {
	if content == nil {
		return nil
	}
	clone := make([]protocol.Content, len(content))
	for i, part := range content {
		clone[i] = cloneContent(part)
	}
	return clone
}

func cloneContent(part protocol.Content) protocol.Content {
	switch typed := part.(type) {
	case *protocol.TextContent:
		clone := *typed
		return &clone
	case *protocol.ThinkingContent:
		clone := *typed
		clone.Redacted = clonePtr(typed.Redacted)
		return &clone
	case *protocol.ImageContent:
		clone := *typed
		return &clone
	case *protocol.ToolCallContent:
		clone := *typed
		clone.Input = cloneJSONValue(typed.Input)
		return &clone
	default:
		return part
	}
}

func cloneUsage(usage *protocol.Usage) *protocol.Usage {
	if usage == nil {
		return nil
	}
	clone := *usage
	clone.Reasoning = clonePtr(usage.Reasoning)
	return &clone
}

// cloneJSONValue copies the containers of a protocol JsonValue. Every scalar the
// protocol admits — nil, bool, string, int64, float64 — is immutable, so only
// objects and arrays need copying.
func cloneJSONValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		clone := make(map[string]any, len(typed))
		for key, item := range typed {
			clone[key] = cloneJSONValue(item)
		}
		return clone
	case []any:
		clone := make([]any, len(typed))
		for i, item := range typed {
			clone[i] = cloneJSONValue(item)
		}
		return clone
	default:
		return value
	}
}

func clonePtr[T any](value *T) *T {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}
