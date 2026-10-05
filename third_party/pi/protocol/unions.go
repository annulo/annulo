package protocol

import "github.com/sky-valley/pi/internal/jsonstrict"

// Union decoding. pi discriminates each union with a literal property and lets
// TypeBox's Check walk every arm; Go dispatches on the same property and
// decodes exactly one arm. Rejecting an unknown tag here is what keeps an
// unknown message shape from reaching code that assumes it was validated.

func init() {
	registerUnion(decodeContent)
	registerUnion(decodeTranscriptItem)
	registerUnion(decodeTranscriptProgress)
	registerUnion(decodeCommand)
	registerUnion(decodeCommandResult)
	registerUnion(decodeServerEvent)
	registerUnion(decodeClientMessage)
	registerUnion(decodeServerMessage)
	registerUnion(decodeRpcTarget) // v8, protocol.go
}

func decodeContent(value any) (Content, error) {
	tag, _, err := jsonstrict.Discriminant(value, "type")
	if err != nil {
		return nil, err
	}
	switch tag {
	case "text":
		return decodeMember[*TextContent](value)
	case "thinking":
		return decodeMember[*ThinkingContent](value)
	case "image":
		return decodeMember[*ImageContent](value)
	case "toolCall":
		return decodeMember[*ToolCallContent](value)
	default:
		return nil, invalidf("unknown content type %q", tag)
	}
}

func decodeTranscriptItem(value any) (TranscriptItem, error) {
	tag, _, err := jsonstrict.Discriminant(value, "role")
	if err != nil {
		return nil, err
	}
	switch tag {
	case "user":
		return decodeMember[*UserTranscriptItem](value)
	case "assistant":
		return decodeMember[*AssistantTranscriptItem](value)
	case "tool":
		return decodeMember[*ToolTranscriptItem](value)
	default:
		return nil, invalidf("unknown transcript item role %q", tag)
	}
}

func decodeTranscriptProgress(value any) (TranscriptProgress, error) {
	tag, _, err := jsonstrict.Discriminant(value, "type")
	if err != nil {
		return nil, err
	}
	switch tag {
	case "item_started":
		return decodeMember[*ItemStartedProgress](value)
	case "assistant_delta":
		return decodeMember[*AssistantDeltaProgress](value)
	case "item_updated":
		return decodeMember[*ItemUpdatedProgress](value)
	case "item_finished":
		return decodeMember[*ItemFinishedProgress](value)
	default:
		return nil, invalidf("unknown transcript progress type %q", tag)
	}
}

func decodeCommand(value any) (Command, error) {
	tag, _, err := jsonstrict.Discriminant(value, "command")
	if err != nil {
		return nil, err
	}
	switch tag {
	case "list":
		return decodeMember[*ListCommand](value)
	case "create":
		return decodeMember[*CreateCommand](value)
	case "attach":
		return decodeMember[*AttachCommand](value)
	case "detach":
		return decodeMember[*DetachCommand](value)
	case "prompt":
		return decodeMember[*PromptCommand](value)
	case "steer":
		return decodeMember[*SteerCommand](value)
	case "abort":
		return decodeMember[*AbortCommand](value)
	case "set_model":
		return decodeMember[*SetModelCommand](value)
	case "set_thinking":
		return decodeMember[*SetThinkingCommand](value)
	default:
		return nil, invalidf("unknown command %q", tag)
	}
}

func decodeCommandResult(value any) (CommandResult, error) {
	tag, _, err := jsonstrict.Discriminant(value, "command")
	if err != nil {
		return nil, err
	}
	switch tag {
	case "list":
		return decodeMember[*ListResult](value)
	case "detach":
		return decodeMember[*DetachResult](value)
	case "create", "attach", "prompt", "steer", "abort", "set_model", "set_thinking":
		return decodeMember[*SessionResult](value)
	default:
		return nil, invalidf("unknown command result %q", tag)
	}
}

func decodeServerEvent(value any) (ServerEvent, error) {
	tag, _, err := jsonstrict.Discriminant(value, "type")
	if err != nil {
		return nil, err
	}
	switch tag {
	case "server_snapshot":
		return decodeMember[*ServerSnapshotEvent](value)
	case "session_snapshot":
		return decodeMember[*SessionSnapshotEvent](value)
	case "session_progress":
		return decodeMember[*SessionProgressEvent](value)
	case "session_removed":
		return decodeMember[*SessionRemovedEvent](value)
	default:
		return nil, invalidf("unknown server event %q", tag)
	}
}

func decodeClientMessage(value any) (ClientMessage, error) {
	tag, _, err := jsonstrict.Discriminant(value, "type")
	if err != nil {
		return nil, err
	}
	switch tag {
	case "hello":
		return decodeMember[*ClientHello](value)
	case "request":
		return decodeMember[*RequestEnvelope](value)
	default:
		return nil, invalidf("unknown client message type %q", tag)
	}
}

func decodeServerMessage(value any) (ServerMessage, error) {
	tag, _, err := jsonstrict.Discriminant(value, "type")
	if err != nil {
		return nil, err
	}
	switch tag {
	case "hello":
		return decodeMember[*ServerHello](value)
	case "hello_error":
		return decodeMember[*ServerHelloError](value)
	case "response":
		return decodeMember[*ResponseEnvelope](value)
	case "event":
		return decodeMember[*EventEnvelope](value)
	default:
		return nil, invalidf("unknown server message type %q", tag)
	}
}

// ParseClientMessage validates an already-decoded value as a client message.
func ParseClientMessage(value any) (ClientMessage, error) {
	message, err := decodeClientMessage(value)
	if err != nil {
		return nil, &ValidationError{Msg: "Invalid client protocol message: " + err.Error()}
	}
	return message, nil
}

// ParseServerMessage validates an already-decoded value as a server message.
func ParseServerMessage(value any) (ServerMessage, error) {
	message, err := decodeServerMessage(value)
	if err != nil {
		return nil, &ValidationError{Msg: "Invalid server protocol message: " + err.Error()}
	}
	return message, nil
}
