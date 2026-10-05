package protocol

import "slices"

// ProtocolVersion is the wire version this package speaks.
const ProtocolVersion = 1

// Struct field order below is significant: it is the order the fields go on the
// wire, and it mirrors the property order of pi's TypeBox schemas so a Go
// encoding is byte-identical to a Node one. Reordering fields is a wire change.

// ThinkingLevel mirrors pi's thinking-level vocabulary.
type ThinkingLevel string

const (
	ThinkingOff     ThinkingLevel = "off"
	ThinkingMinimal ThinkingLevel = "minimal"
	ThinkingLow     ThinkingLevel = "low"
	ThinkingMedium  ThinkingLevel = "medium"
	ThinkingHigh    ThinkingLevel = "high"
	ThinkingXHigh   ThinkingLevel = "xhigh"
	ThinkingMax     ThinkingLevel = "max"
)

var thinkingLevels = []ThinkingLevel{
	ThinkingOff, ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh, ThinkingXHigh, ThinkingMax,
}

func (t ThinkingLevel) Validate() error {
	if !slices.Contains(thinkingLevels, t) {
		return invalidf("unknown thinking level %q", string(t))
	}
	return nil
}

// SessionPhase matches AgentHarnessPhase so adapters do not need a second
// phase vocabulary.
type SessionPhase string

const (
	PhaseIdle          SessionPhase = "idle"
	PhaseTurn          SessionPhase = "turn"
	PhaseCompaction    SessionPhase = "compaction"
	PhaseBranchSummary SessionPhase = "branch_summary"
	PhaseRetry         SessionPhase = "retry"
)

var sessionPhases = []SessionPhase{PhaseIdle, PhaseTurn, PhaseCompaction, PhaseBranchSummary, PhaseRetry}

func (p SessionPhase) Validate() error {
	if !slices.Contains(sessionPhases, p) {
		return invalidf("unknown session phase %q", string(p))
	}
	return nil
}

// ModelRef identifies a model by provider and id.
type ModelRef struct {
	Provider string `cbor:"provider"`
	ID       string `cbor:"id"`
}

func (m *ModelRef) Validate() error {
	if err := requireID("provider", m.Provider); err != nil {
		return err
	}
	return requireID("id", m.ID)
}

// ModelCost is per-token pricing.
type ModelCost struct {
	Input      float64 `cbor:"input"`
	Output     float64 `cbor:"output"`
	CacheRead  float64 `cbor:"cacheRead"`
	CacheWrite float64 `cbor:"cacheWrite"`
}

func (c *ModelCost) Validate() error {
	for _, f := range []struct {
		name  string
		value float64
	}{
		{"input", c.Input}, {"output", c.Output},
		{"cacheRead", c.CacheRead}, {"cacheWrite", c.CacheWrite},
	} {
		if err := requireNonNegativeFloat(f.name, f.value); err != nil {
			return err
		}
	}
	return nil
}

// ModelInputModality is a modality a model accepts.
type ModelInputModality string

const (
	InputText  ModelInputModality = "text"
	InputImage ModelInputModality = "image"
)

// ModelMetadata describes a model the server can run.
type ModelMetadata struct {
	Provider                string               `cbor:"provider"`
	ID                      string               `cbor:"id"`
	Name                    string               `cbor:"name"`
	API                     string               `cbor:"api"`
	Reasoning               bool                 `cbor:"reasoning"`
	Input                   []ModelInputModality `cbor:"input"`
	ContextWindow           int64                `cbor:"contextWindow"`
	MaxTokens               int64                `cbor:"maxTokens"`
	Cost                    ModelCost            `cbor:"cost"`
	SupportedThinkingLevels []ThinkingLevel      `cbor:"supportedThinkingLevels"`
	Authenticated           bool                 `cbor:"authenticated"`
}

func (m *ModelMetadata) Validate() error {
	if err := requireID("provider", m.Provider); err != nil {
		return err
	}
	if err := requireID("id", m.ID); err != nil {
		return err
	}
	if err := requireNonEmpty("name", m.Name); err != nil {
		return err
	}
	if err := requireID("api", m.API); err != nil {
		return err
	}
	for _, modality := range m.Input {
		if modality != InputText && modality != InputImage {
			return invalidf("unknown input modality %q", string(modality))
		}
	}
	if err := requirePositive("contextWindow", m.ContextWindow); err != nil {
		return err
	}
	if err := requirePositive("maxTokens", m.MaxTokens); err != nil {
		return err
	}
	if err := m.Cost.Validate(); err != nil {
		return err
	}
	if len(m.SupportedThinkingLevels) == 0 {
		return invalidf("supportedThinkingLevels must not be empty")
	}
	for _, level := range m.SupportedThinkingLevels {
		if err := level.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// Content is a block inside a transcript item: TextContent, ThinkingContent,
// ImageContent, or ToolCallContent. Which of those are legal depends on the
// item's role, enforced when the item is validated.
type Content interface {
	ContentType() string
	content()
}

// TextContent is plain text.
type TextContent struct {
	Type string `cbor:"type"`
	Text string `cbor:"text"`
}

func (*TextContent) ContentType() string { return "text" }
func (c *TextContent) Validate() error {
	if c == nil {
		return nilMember(c)
	}
	return requireLiteral("type", c.Type, "text")
}

// ThinkingContent is reasoning text.
type ThinkingContent struct {
	Type     string `cbor:"type"`
	Thinking string `cbor:"thinking"`
	Redacted *bool  `cbor:"redacted,omitempty"`
}

func (*ThinkingContent) ContentType() string { return "thinking" }
func (c *ThinkingContent) Validate() error {
	if c == nil {
		return nilMember(c)
	}
	return requireLiteral("type", c.Type, "thinking")
}

// ImageContent is a base64 image payload.
type ImageContent struct {
	Type     string `cbor:"type"`
	Data     string `cbor:"data"`
	MimeType string `cbor:"mimeType"`
}

func (*ImageContent) ContentType() string { return "image" }
func (c *ImageContent) Validate() error {
	if c == nil {
		return nilMember(c)
	}
	if err := requireLiteral("type", c.Type, "image"); err != nil {
		return err
	}
	return requireNonEmpty("mimeType", c.MimeType)
}

// ToolCallContent is a model's request to run a tool.
type ToolCallContent struct {
	Type       string `cbor:"type"`
	ToolCallID string `cbor:"toolCallId"`
	ToolName   string `cbor:"toolName"`
	Input      any    `cbor:"input"`
}

func (*ToolCallContent) ContentType() string { return "toolCall" }
func (c *ToolCallContent) Validate() error {
	if c == nil {
		return nilMember(c)
	}
	if err := requireLiteral("type", c.Type, "toolCall"); err != nil {
		return err
	}
	if err := requireID("toolCallId", c.ToolCallID); err != nil {
		return err
	}
	if err := requireID("toolName", c.ToolName); err != nil {
		return err
	}
	return requireJSONValue("input", c.Input)
}

// UsageCost totals the cost of one assistant turn.
type UsageCost struct {
	Input      float64 `cbor:"input"`
	Output     float64 `cbor:"output"`
	CacheRead  float64 `cbor:"cacheRead"`
	CacheWrite float64 `cbor:"cacheWrite"`
	Total      float64 `cbor:"total"`
}

func (c *UsageCost) Validate() error {
	for _, f := range []struct {
		name  string
		value float64
	}{
		{"input", c.Input}, {"output", c.Output}, {"cacheRead", c.CacheRead},
		{"cacheWrite", c.CacheWrite}, {"total", c.Total},
	} {
		if err := requireNonNegativeFloat(f.name, f.value); err != nil {
			return err
		}
	}
	return nil
}

// Usage is token accounting for one turn.
type Usage struct {
	Input       int64     `cbor:"input"`
	Output      int64     `cbor:"output"`
	CacheRead   int64     `cbor:"cacheRead"`
	CacheWrite  int64     `cbor:"cacheWrite"`
	Reasoning   *int64    `cbor:"reasoning,omitempty"`
	TotalTokens int64     `cbor:"totalTokens"`
	Cost        UsageCost `cbor:"cost"`
}

func (u *Usage) Validate() error {
	for _, f := range []struct {
		name  string
		value int64
	}{
		{"input", u.Input}, {"output", u.Output}, {"cacheRead", u.CacheRead},
		{"cacheWrite", u.CacheWrite}, {"totalTokens", u.TotalTokens},
	} {
		if err := requireNonNegative(f.name, f.value); err != nil {
			return err
		}
	}
	if u.Reasoning != nil {
		if err := requireNonNegative("reasoning", *u.Reasoning); err != nil {
			return err
		}
	}
	return u.Cost.Validate()
}

// ProtocolErrorCode enumerates the failures a peer can report.
type ProtocolErrorCode string

const (
	ErrorVersion        ProtocolErrorCode = "version"
	ErrorBusy           ProtocolErrorCode = "busy"
	ErrorSessionLocked  ProtocolErrorCode = "session_locked"
	ErrorNotFound       ProtocolErrorCode = "not_found"
	ErrorInvalidRequest ProtocolErrorCode = "invalid_request"
	ErrorNotImplemented ProtocolErrorCode = "not_implemented"
	ErrorInternal       ProtocolErrorCode = "internal_error"
)

var protocolErrorCodes = []ProtocolErrorCode{
	ErrorVersion, ErrorBusy, ErrorSessionLocked, ErrorNotFound, ErrorInvalidRequest,
	ErrorNotImplemented, ErrorInternal,
}

// ProtocolError is a structured failure carried in a response or hello_error.
type ProtocolError struct {
	Code    ProtocolErrorCode `cbor:"code"`
	Message string            `cbor:"message"`
	Details *any              `cbor:"details,omitempty"`
}

func (e *ProtocolError) Validate() error {
	if !slices.Contains(protocolErrorCodes, e.Code) {
		return invalidf("unknown protocol error code %q", string(e.Code))
	}
	if e.Details == nil {
		return nil
	}
	return requireJSONValue("details", *e.Details)
}

// Error lets a ProtocolError be returned as a Go error.
func (e *ProtocolError) Error() string {
	return string(e.Code) + ": " + e.Message
}

// Sealing markers. See the note in messages.go.
func (*TextContent) content()     {}
func (*ThinkingContent) content() {}
func (*ImageContent) content()    {}
func (*ToolCallContent) content() {}
