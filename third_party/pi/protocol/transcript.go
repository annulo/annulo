package protocol

import "slices"

// TranscriptItem is one entry in a session transcript: UserTranscriptItem,
// AssistantTranscriptItem, or ToolTranscriptItem, discriminated by role.
type TranscriptItem interface {
	ItemRole() string
	transcriptItem()
}

// UserTranscriptItem is a prompt or steer from the user.
type UserTranscriptItem struct {
	ID        string    `cbor:"id"`
	Role      string    `cbor:"role"`
	Content   []Content `cbor:"content"`
	Timestamp int64     `cbor:"timestamp"`
}

func (*UserTranscriptItem) ItemRole() string { return "user" }

func (i *UserTranscriptItem) Validate() error {
	if i == nil {
		return nilMember(i)
	}
	if err := requireID("id", i.ID); err != nil {
		return err
	}
	if err := requireLiteral("role", i.Role, "user"); err != nil {
		return err
	}
	if err := allowContent("content", i.Content, "text", "image"); err != nil {
		return err
	}
	return requireTimestamp("timestamp", i.Timestamp)
}

// AssistantStatus is the lifecycle state of an assistant item.
type AssistantStatus string

const (
	AssistantStreaming AssistantStatus = "streaming"
	AssistantComplete  AssistantStatus = "complete"
	AssistantError     AssistantStatus = "error"
	AssistantAborted   AssistantStatus = "aborted"
)

// StopReason is why an assistant turn ended.
type StopReason string

const (
	StopStop    StopReason = "stop"
	StopLength  StopReason = "length"
	StopToolUse StopReason = "toolUse"
	StopError   StopReason = "error"
	StopAborted StopReason = "aborted"
)

// AssistantTranscriptItem is one assistant turn.
//
// pi models this as a four-arm union sharing a property block. The port keeps a
// single struct with the shared fields plus the arm-specific ones as optionals,
// and Validate enforces exactly the combinations the union allows — so an
// illegal pairing (a streaming item carrying a stopReason, say) is still
// rejected, without four near-identical Go types.
type AssistantTranscriptItem struct {
	ID            string          `cbor:"id"`
	Role          string          `cbor:"role"`
	Content       []Content       `cbor:"content"`
	Model         ModelRef        `cbor:"model"`
	ResponseModel *string         `cbor:"responseModel,omitempty"`
	Usage         *Usage          `cbor:"usage,omitempty"`
	Timestamp     int64           `cbor:"timestamp"`
	Status        AssistantStatus `cbor:"status"`
	StopReason    *StopReason     `cbor:"stopReason,omitempty"`
	ErrorMessage  *string         `cbor:"errorMessage,omitempty"`
}

func (*AssistantTranscriptItem) ItemRole() string { return "assistant" }

func (i *AssistantTranscriptItem) Validate() error {
	if i == nil {
		return nilMember(i)
	}
	if err := requireID("id", i.ID); err != nil {
		return err
	}
	if err := requireLiteral("role", i.Role, "assistant"); err != nil {
		return err
	}
	if err := allowContent("content", i.Content, "text", "thinking", "toolCall"); err != nil {
		return err
	}
	if err := i.Model.Validate(); err != nil {
		return err
	}
	if i.ResponseModel != nil {
		if err := requireNonEmpty("responseModel", *i.ResponseModel); err != nil {
			return err
		}
	}
	if i.Usage != nil {
		if err := i.Usage.Validate(); err != nil {
			return err
		}
	}
	if err := requireTimestamp("timestamp", i.Timestamp); err != nil {
		return err
	}

	switch i.Status {
	case AssistantStreaming:
		if i.StopReason != nil {
			return invalidf("a streaming assistant item must not carry a stopReason")
		}
		if i.ErrorMessage != nil {
			return invalidf("a streaming assistant item must not carry an errorMessage")
		}
	case AssistantComplete:
		if i.StopReason == nil || !slices.Contains([]StopReason{StopStop, StopLength, StopToolUse}, *i.StopReason) {
			return invalidf("a complete assistant item requires a stopReason of stop, length, or toolUse")
		}
		if i.ErrorMessage != nil {
			return invalidf("a complete assistant item must not carry an errorMessage")
		}
	case AssistantError:
		if i.StopReason == nil || *i.StopReason != StopError {
			return invalidf("an error assistant item requires stopReason %q", string(StopError))
		}
		// pi types this arm's errorMessage as String({minLength: 1}).
		if i.ErrorMessage != nil {
			if err := requireNonEmpty("errorMessage", *i.ErrorMessage); err != nil {
				return err
			}
		}
	case AssistantAborted:
		if i.StopReason == nil || *i.StopReason != StopAborted {
			return invalidf("an aborted assistant item requires stopReason %q", string(StopAborted))
		}
		// Deliberately not minLength-checked: pi's aborted arm allows "".
	default:
		return invalidf("unknown assistant status %q", string(i.Status))
	}
	return nil
}

// ToolStatus is the lifecycle state of a tool item.
type ToolStatus string

const (
	ToolRunning  ToolStatus = "running"
	ToolComplete ToolStatus = "complete"
	ToolError    ToolStatus = "error"
)

// ToolTranscriptItem is one tool invocation and its result.
type ToolTranscriptItem struct {
	ID         string     `cbor:"id"`
	Role       string     `cbor:"role"`
	ToolCallID string     `cbor:"toolCallId"`
	ToolName   string     `cbor:"toolName"`
	Input      any        `cbor:"input"`
	Content    []Content  `cbor:"content"`
	Details    *any       `cbor:"details,omitempty"`
	Usage      *Usage     `cbor:"usage,omitempty"`
	Timestamp  int64      `cbor:"timestamp"`
	Status     ToolStatus `cbor:"status"`
	IsError    bool       `cbor:"isError"`
}

func (*ToolTranscriptItem) ItemRole() string { return "tool" }

func (i *ToolTranscriptItem) Validate() error {
	if i == nil {
		return nilMember(i)
	}
	if err := requireID("id", i.ID); err != nil {
		return err
	}
	if err := requireLiteral("role", i.Role, "tool"); err != nil {
		return err
	}
	if err := requireID("toolCallId", i.ToolCallID); err != nil {
		return err
	}
	if err := requireID("toolName", i.ToolName); err != nil {
		return err
	}
	if err := requireJSONValue("input", i.Input); err != nil {
		return err
	}
	if err := allowContent("content", i.Content, "text", "image"); err != nil {
		return err
	}
	// *any, not any: pi's details is Optional(JsonValue) and JsonValue
	// includes Null, so absent and null are distinct on the wire. A nil
	// interface behind a non-nil pointer is the null.
	if i.Details != nil {
		if err := requireJSONValue("details", *i.Details); err != nil {
			return err
		}
	}
	if i.Usage != nil {
		if err := i.Usage.Validate(); err != nil {
			return err
		}
	}
	if err := requireTimestamp("timestamp", i.Timestamp); err != nil {
		return err
	}
	// isError is a literal in pi: false while running or complete, true on
	// error. It is the discriminant, not an independent flag.
	switch i.Status {
	case ToolRunning, ToolComplete:
		if i.IsError {
			return invalidf("a %s tool item must have isError false", string(i.Status))
		}
	case ToolError:
		if !i.IsError {
			return invalidf("an error tool item must have isError true")
		}
	default:
		return invalidf("unknown tool status %q", string(i.Status))
	}
	return nil
}

// allowContent enforces the per-role content unions: pi gives user and tool
// items TextContent|ImageContent and assistant items
// TextContent|ThinkingContent|ToolCallContent.
func allowContent(name string, content []Content, allowed ...string) error {
	for _, block := range content {
		if block == nil {
			return invalidf("%s must not contain empty blocks", name)
		}
		if !slices.Contains(allowed, block.ContentType()) {
			return invalidf("%s must not contain a %q block", name, block.ContentType())
		}
		if v, ok := block.(Validator); ok {
			if err := v.Validate(); err != nil {
				return err
			}
		}
	}
	return nil
}

// TranscriptProgress is normalized incremental activity. Snapshots remain
// authoritative; progress is an optimization, never the source of truth.
type TranscriptProgress interface {
	ProgressType() string
	transcriptProgress()
}

// ItemStartedProgress announces a new transcript item.
type ItemStartedProgress struct {
	Type string         `cbor:"type"`
	Item TranscriptItem `cbor:"item"`
}

func (*ItemStartedProgress) ProgressType() string { return "item_started" }

func (p *ItemStartedProgress) Validate() error {
	if p == nil {
		return nilMember(p)
	}
	if err := requireLiteral("type", p.Type, "item_started"); err != nil {
		return err
	}
	if p.Item == nil {
		return invalidf("item is required")
	}
	return validateItem(p.Item)
}

// AssistantDeltaKind is which part of an assistant item a delta appends to.
type AssistantDeltaKind string

const (
	DeltaText     AssistantDeltaKind = "text"
	DeltaThinking AssistantDeltaKind = "thinking"
	DeltaToolCall AssistantDeltaKind = "toolCall"
)

// AssistantDeltaProgress appends to a streaming assistant content block.
type AssistantDeltaProgress struct {
	Type         string             `cbor:"type"`
	MessageID    string             `cbor:"messageId"`
	ContentIndex int64              `cbor:"contentIndex"`
	Kind         AssistantDeltaKind `cbor:"kind"`
	Delta        string             `cbor:"delta"`
}

func (*AssistantDeltaProgress) ProgressType() string { return "assistant_delta" }

func (p *AssistantDeltaProgress) Validate() error {
	if p == nil {
		return nilMember(p)
	}
	if err := requireLiteral("type", p.Type, "assistant_delta"); err != nil {
		return err
	}
	if err := requireID("messageId", p.MessageID); err != nil {
		return err
	}
	if err := requireNonNegative("contentIndex", p.ContentIndex); err != nil {
		return err
	}
	if !slices.Contains([]AssistantDeltaKind{DeltaText, DeltaThinking, DeltaToolCall}, p.Kind) {
		return invalidf("unknown delta kind %q", string(p.Kind))
	}
	return nil
}

// ItemUpdatedProgress replaces an in-flight assistant or tool item.
type ItemUpdatedProgress struct {
	Type string         `cbor:"type"`
	Item TranscriptItem `cbor:"item"`
}

func (*ItemUpdatedProgress) ProgressType() string { return "item_updated" }

func (p *ItemUpdatedProgress) Validate() error {
	if p == nil {
		return nilMember(p)
	}
	if err := requireLiteral("type", p.Type, "item_updated"); err != nil {
		return err
	}
	switch p.Item.(type) {
	case *AssistantTranscriptItem, *ToolTranscriptItem:
	default:
		return invalidf("item_updated carries an assistant or tool item")
	}
	return validateItem(p.Item)
}

// ItemFinishedProgress closes out an item. pi narrows this arm to the terminal
// variants only — a streaming assistant item or a running tool item cannot be
// "finished".
type ItemFinishedProgress struct {
	Type string         `cbor:"type"`
	Item TranscriptItem `cbor:"item"`
}

func (*ItemFinishedProgress) ProgressType() string { return "item_finished" }

func (p *ItemFinishedProgress) Validate() error {
	if p == nil {
		return nilMember(p)
	}
	if err := requireLiteral("type", p.Type, "item_finished"); err != nil {
		return err
	}
	// The nil checks come before the status reads: a typed nil satisfies
	// TranscriptItem and matches its arm, so this switch is where a hand-built
	// message dereferences nil unless it is stopped here.
	switch item := p.Item.(type) {
	case *AssistantTranscriptItem:
		if item == nil {
			return nilMember(item)
		}
		if item.Status == AssistantStreaming {
			return invalidf("item_finished must not carry a streaming assistant item")
		}
	case *ToolTranscriptItem:
		if item == nil {
			return nilMember(item)
		}
		if item.Status == ToolRunning {
			return invalidf("item_finished must not carry a running tool item")
		}
	default:
		return invalidf("item_finished carries an assistant or tool item")
	}
	return validateItem(p.Item)
}

func validateItem(item TranscriptItem) error {
	if v, ok := item.(Validator); ok {
		return v.Validate()
	}
	return nil
}

// Sealing markers. See the note in messages.go.
func (*UserTranscriptItem) transcriptItem()      {}
func (*AssistantTranscriptItem) transcriptItem() {}
func (*ToolTranscriptItem) transcriptItem()      {}

func (*ItemStartedProgress) transcriptProgress()    {}
func (*AssistantDeltaProgress) transcriptProgress() {}
func (*ItemUpdatedProgress) transcriptProgress()    {}
func (*ItemFinishedProgress) transcriptProgress()   {}
