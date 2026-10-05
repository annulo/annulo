package ai

// DiagnosticErrorInfo is the redacted error attached to a Diagnostic. It mirrors
// pi's DiagnosticErrorInfo (utils/diagnostics.ts). Only Message is required.
type DiagnosticErrorInfo struct {
	Name    string `json:"name,omitempty"`
	Message string `json:"message"`
	Stack   string `json:"stack,omitempty"`
	// Code is a string or number (pi: string | number). Kept as any so both
	// shapes round-trip; omitted when nil.
	Code any `json:"code,omitempty"`
}

// Diagnostic is a provider/runtime diagnostic attached to an AssistantMessage.
// It is the Go analogue of pi's AssistantMessageDiagnostic (utils/diagnostics.ts):
// {type, timestamp, error?, details?}.
type Diagnostic struct {
	Type      string               `json:"type"`
	Timestamp int64                `json:"timestamp"`
	Error     *DiagnosticErrorInfo `json:"error,omitempty"`
	// Details is pi's details object, in its key order: an OrderedObject
	// writes its members in that order, as JSON.stringify writes pi's (a
	// map[string]any would sort them), and its numbers as JSON.stringify
	// writes them. nil is pi's undefined, which is omitted; an empty, non-nil
	// object is written {}, as pi writes an empty details object.
	Details OrderedObject `json:"details,omitzero"`
}
