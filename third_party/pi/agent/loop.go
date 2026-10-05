package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/sky-valley/pi/ai"
)

// StreamFn streams an assistant response. Defaults to ai.StreamSimple. Per the
// stream contract it must encode failures in the returned stream, not panic.
//
// The loop passes a normalized transcript: the system prompt and tool
// declarations are carried by the transcript's system messages (pi StreamFn,
// upstream 9e05370b2).
type StreamFn func(ctx context.Context, model *ai.Model, req ai.TranscriptContext, opts *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream

// streamSimpleTranscript is the default StreamFn: ai.StreamSimple over the
// transcript. Normalizing a context that carries no prompt or tools is the
// identity, so the provider receives exactly the transcript the loop built.
func streamSimpleTranscript(ctx context.Context, model *ai.Model, req ai.TranscriptContext, opts *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
	return ai.StreamSimple(ctx, model, ai.Context{Messages: req.Messages}, opts)
}

// emitPanic is the value used to unwind the loop when an event listener returns
// an error. pi rejects the run when a subscriber throws (agent.ts:553-555); the
// rejection propagates out of runAgentLoop and is caught by runWithLifecycle.
// Go has no exceptions, so we mirror that unwind with a typed panic that is
// recovered at the run boundary (in agent.go) or in the low-level goroutine.
type emitPanic struct{ err error }

// mustEmit emits an event and unwinds the loop (via panic) if the sink returns
// an error, matching pi's "await emit(...)" throwing on a rejected listener.
func mustEmit(emit EventSink, e AgentEvent) {
	if err := emit(e); err != nil {
		panic(emitPanic{err: err})
	}
}

// AgentLoop starts an agent loop with new prompt messages, returning a stream of
// AgentEvents whose final result is the list of new messages produced.
func AgentLoop(ctx context.Context, prompts []AgentMessage, agentCtx AgentContext, config AgentLoopConfig, streamFn StreamFn) *ai.EventStream[AgentEvent, []AgentMessage] {
	stream := newAgentStream()
	go func() {
		// The low-level loop has no failure-turn synthesis (that lives in the
		// Agent wrapper, matching pi). Recover so a listener error / panic ends
		// the stream instead of crashing the goroutine; pi rejects the returned
		// promise in the equivalent case.
		defer func() { _ = recover() }()
		messages := runAgentLoop(ctx, prompts, agentCtx, config, func(e AgentEvent) error {
			stream.Push(e)
			return nil
		}, streamFn)
		stream.End(messages)
	}()
	return stream
}

// AgentLoopContinue continues from the current context without a new message.
// The last message must convert to a user or tool-result message.
func AgentLoopContinue(ctx context.Context, agentCtx AgentContext, config AgentLoopConfig, streamFn StreamFn) (*ai.EventStream[AgentEvent, []AgentMessage], error) {
	if len(agentCtx.Messages) == 0 {
		return nil, errors.New("Cannot continue: no messages in context")
	}
	if agentCtx.Messages[len(agentCtx.Messages)-1].MessageRole() == ai.RoleAssistant {
		return nil, errors.New("Cannot continue from message role: assistant")
	}
	stream := newAgentStream()
	go func() {
		defer func() { _ = recover() }()
		messages := runAgentLoopContinue(ctx, agentCtx, config, func(e AgentEvent) error {
			stream.Push(e)
			return nil
		}, streamFn)
		stream.End(messages)
	}()
	return stream, nil
}

func newAgentStream() *ai.EventStream[AgentEvent, []AgentMessage] {
	return ai.NewEventStream(
		func(e AgentEvent) bool { return e.Type == EvAgentEnd },
		func(e AgentEvent) []AgentMessage {
			if e.Type == EvAgentEnd {
				return e.Messages
			}
			return nil
		},
	)
}

func runAgentLoop(ctx context.Context, prompts []AgentMessage, agentCtx AgentContext, config AgentLoopConfig, emit EventSink, streamFn StreamFn) []AgentMessage {
	initialMessages := declareToolChanges(agentCtx, prompts)
	newMessages := append([]AgentMessage(nil), initialMessages...)
	current := agentCtx
	current.Messages = append(append([]AgentMessage(nil), agentCtx.Messages...), initialMessages...)

	mustEmit(emit, AgentEvent{Type: EvAgentStart})
	mustEmit(emit, AgentEvent{Type: EvTurnStart})
	for _, m := range initialMessages {
		mustEmit(emit, AgentEvent{Type: EvMessageStart, Message: m})
		mustEmit(emit, AgentEvent{Type: EvMessageEnd, Message: m})
	}

	runLoop(ctx, &current, &newMessages, config, emit, streamFn)
	return newMessages
}

func runAgentLoopContinue(ctx context.Context, agentCtx AgentContext, config AgentLoopConfig, emit EventSink, streamFn StreamFn) []AgentMessage {
	newMessages := []AgentMessage{}
	current := agentCtx

	mustEmit(emit, AgentEvent{Type: EvAgentStart})
	mustEmit(emit, AgentEvent{Type: EvTurnStart})

	runLoop(ctx, &current, &newMessages, config, emit, streamFn)
	return newMessages
}

func aborted(ctx context.Context) bool {
	return ctx != nil && ctx.Err() != nil
}

func runLoop(ctx context.Context, current *AgentContext, newMessages *[]AgentMessage, config AgentLoopConfig, emit EventSink, streamFn StreamFn) {
	// lastCompletedTurn is nil until a turn has finished; it doubles as pi's
	// firstTurn flag and as the context handed to PrepareNextTurn at the head of
	// the NEXT iteration (agent-loop.ts:166,173).
	var lastCompletedTurn *AgentTurnContext
	// explicitContinuation is a FinishTurn TurnContinue not yet satisfied by a
	// naturally scheduled request (agent-loop.ts:173).
	explicitContinuation := false
	var pending []AgentMessage
	if config.GetSteeringMessages != nil {
		pending = config.GetSteeringMessages()
	}

	for { // outer loop: follow-up messages
		hasMoreToolCalls := true

		for hasMoreToolCalls || len(pending) > 0 {
			var prepared []AgentMessage
			if lastCompletedTurn != nil {
				// Preparation runs here — immediately before the next provider
				// request — not right after the previous turn_end, so a preparation
				// that rewrites the context (compaction) also covers requests made
				// after tool calls (agent-loop.ts:176-195, upstream 56700d42e).
				if config.PrepareNextTurn != nil {
					if snap := config.PrepareNextTurn(*lastCompletedTurn); snap != nil {
						prepared = snap.applyTo(current, &config)
					}
				}
				// Preparation can be long-running (for example, compaction). Pick up
				// steering queued while it ran. Only poll again if the earlier poll
				// returned nothing; otherwise one-at-a-time mode would deliver two
				// messages in this turn.
				if len(pending) == 0 && config.GetSteeringMessages != nil {
					pending = config.GetSteeringMessages()
				}
				mustEmit(emit, AgentEvent{Type: EvTurnStart})
			}

			// Process prepared and queued messages before the next assistant
			// response, declaring any tool loadout change with them.
			for _, m := range declareToolChanges(*current, slices.Concat(prepared, pending)) {
				mustEmit(emit, AgentEvent{Type: EvMessageStart, Message: m})
				mustEmit(emit, AgentEvent{Type: EvMessageEnd, Message: m})
				current.Messages = append(current.Messages, m)
				*newMessages = append(*newMessages, m)
			}
			pending = nil

			if config.PrepareRequest != nil {
				thinkingLevel := config.Reasoning
				if thinkingLevel == "" {
					thinkingLevel = ThinkOff
				}
				if update := config.PrepareRequest(ctx, PrepareRequestContext{Context: current, Model: config.Model, ThinkingLevel: thinkingLevel}); update != nil {
					update.applyTo(current, &config)
				}
			}

			message := streamAssistantResponse(ctx, current, config, emit, streamFn)
			*newMessages = append(*newMessages, message)

			if message.StopReason == ai.StopError || message.StopReason == ai.StopAborted {
				// FinishTurn still sees the failed turn, but its decision is
				// ignored: error and aborted responses are hard exits.
				lastCompletedTurn = &AgentTurnContext{
					Message:     message,
					ToolResults: []ai.ToolResultMessage{},
					Context:     current,
					NewMessages: *newMessages,
				}
				if config.FinishTurn != nil {
					config.FinishTurn(ctx, *lastCompletedTurn)
				}
				// pi emits toolResults: [] here (agent-loop.ts:252), never null.
				mustEmit(emit, AgentEvent{Type: EvTurnEnd, Message: message, ToolResults: []ai.ToolResultMessage{}})
				mustEmit(emit, AgentEvent{Type: EvAgentEnd, Messages: *newMessages})
				return
			}

			toolCalls := filterToolCalls(message)
			// Non-nil so a no-tool turn_end carries [] like pi (agent-loop.ts:260).
			toolResults := []ai.ToolResultMessage{}
			hasMoreToolCalls = false
			if len(toolCalls) > 0 {
				// A "length" stop means the output was cut off by the token limit, so
				// every tool call in the message may carry truncated arguments. Fail
				// them all instead of executing potentially borked calls.
				var batch executedBatch
				if message.StopReason == ai.StopLength {
					batch = failToolCallsFromTruncatedMessage(toolCalls, emit)
				} else {
					batch = executeToolCalls(ctx, current, message, config, emit)
				}
				toolResults = append(toolResults, batch.messages...)
				hasMoreToolCalls = !batch.terminate
				for _, r := range toolResults {
					current.Messages = append(current.Messages, r)
					*newMessages = append(*newMessages, r)
				}
			}

			// Nothing appends to newMessages between here and the PrepareNextTurn
			// call at the head of the next iteration, so this snapshot stays
			// current where pi shares one growing array (agent-loop.ts:279).
			lastCompletedTurn = &AgentTurnContext{
				Message:     message,
				ToolResults: toolResults,
				Context:     current,
				NewMessages: *newMessages,
			}
			var decision AgentTurnDecision
			if config.FinishTurn != nil {
				decision = config.FinishTurn(ctx, *lastCompletedTurn)
			}
			mustEmit(emit, AgentEvent{Type: EvTurnEnd, Message: message, ToolResults: toolResults})

			if decision == TurnEnd {
				mustEmit(emit, AgentEvent{Type: EvAgentEnd, Messages: *newMessages})
				return
			}

			explicitContinuation = decision == TurnContinue
			if config.GetSteeringMessages != nil {
				pending = config.GetSteeringMessages()
			} else {
				pending = nil
			}
			if hasMoreToolCalls || len(pending) > 0 {
				explicitContinuation = false
			}
		}

		var followUps []AgentMessage
		if config.GetFollowUpMessages != nil {
			followUps = config.GetFollowUpMessages()
		}
		if len(followUps) > 0 {
			explicitContinuation = false
			pending = followUps
			continue
		}
		// No natural request was selected, so fulfill the continuation decision
		// with one context-only turn.
		if explicitContinuation {
			explicitContinuation = false
			continue
		}
		break
	}

	mustEmit(emit, AgentEvent{Type: EvAgentEnd, Messages: *newMessages})
}

func filterToolCalls(m *ai.AssistantMessage) []ai.ToolCall {
	var out []ai.ToolCall
	for _, c := range m.Content {
		if tc, ok := c.(ai.ToolCall); ok {
			out = append(out, tc)
		}
	}
	return out
}

func streamAssistantResponse(ctx context.Context, agentCtx *AgentContext, config AgentLoopConfig, emit EventSink, streamFn StreamFn) *ai.AssistantMessage {
	messages := agentCtx.Messages
	if config.TransformContext != nil {
		messages = config.TransformContext(ctx, messages)
	}

	var llmMessages []ai.Message
	if config.ConvertToLlm != nil {
		llmMessages = config.ConvertToLlm(messages)
	} else {
		llmMessages = defaultConvertToLlm(messages)
	}

	// The transcript carries the prompt and the tool declarations; the context
	// holds nothing else.
	llmCtx := ai.NormalizeContext(ai.Context{Messages: llmMessages})

	fn := streamFn
	if fn == nil {
		fn = streamSimpleTranscript
	}

	apiKey := config.APIKey
	if config.GetApiKey != nil {
		if k := config.GetApiKey(config.Model.Provider); k != "" {
			apiKey = k
		}
	}

	// pi spreads the whole config into the stream options (agent-loop.ts:304-308;
	// AgentLoopConfig extends SimpleStreamOptions), so every StreamOptions field
	// must be forwarded here.
	opts := &ai.SimpleStreamOptions{
		StreamOptions: ai.StreamOptions{
			ProviderRequestOptions: ai.ProviderRequestOptions{
				APIKey:          apiKey,
				OnPayload:       config.OnPayload,
				OnResponse:      config.OnResponse,
				MaxRetryDelayMs: config.MaxRetryDelayMs,
				MaxRetries:      config.MaxRetries,
				TimeoutMs:       config.TimeoutMs,
				HTTPClient:      config.HTTPClient,
				Headers:         config.Headers,
			},
			OnProviderStreamEvent:     config.OnProviderStreamEvent,
			Transport:                 config.Transport,
			SessionID:                 config.SessionID,
			WebSocketConnectTimeoutMs: config.WebSocketConnectTimeoutMs,
			Temperature:               config.Temperature,
			MaxTokens:                 config.MaxTokens,
			CacheRetention:            config.CacheRetention,
			Metadata:                  config.Metadata,
		},
		ThinkingBudgets: config.ThinkingBudgets,
	}
	if config.Reasoning != "" && config.Reasoning != "off" {
		opts.Reasoning = ai.ThinkingLevel(config.Reasoning)
	}

	response := fn(ctx, config.Model, llmCtx, opts)

	var partial *ai.AssistantMessage
	addedPartial := false

	for event := range response.Events() {
		switch event.Type {
		case ai.EventStart:
			partial = event.Partial
			agentCtx.Messages = append(agentCtx.Messages, partial)
			addedPartial = true
			mustEmit(emit, AgentEvent{Type: EvMessageStart, Message: partial.Clone()})

		case ai.EventTextStart, ai.EventTextDelta, ai.EventTextEnd,
			ai.EventThinkingStart, ai.EventThinkingDelta, ai.EventThinkingEnd,
			ai.EventToolCallStart, ai.EventToolCallDelta, ai.EventToolCallEnd:
			if partial != nil {
				partial = event.Partial
				agentCtx.Messages[len(agentCtx.Messages)-1] = partial
				ev := event
				mustEmit(emit, AgentEvent{Type: EvMessageUpdate, AssistantMessageEvent: &ev, Message: partial.Clone()})
			}

		case ai.EventDone, ai.EventError:
			final := response.Result()
			if addedPartial {
				agentCtx.Messages[len(agentCtx.Messages)-1] = final
			} else {
				agentCtx.Messages = append(agentCtx.Messages, final)
				mustEmit(emit, AgentEvent{Type: EvMessageStart, Message: final.Clone()})
			}
			mustEmit(emit, AgentEvent{Type: EvMessageEnd, Message: final})
			return final
		}
	}

	final := response.Result()
	if addedPartial {
		agentCtx.Messages[len(agentCtx.Messages)-1] = final
	} else {
		agentCtx.Messages = append(agentCtx.Messages, final)
		mustEmit(emit, AgentEvent{Type: EvMessageStart, Message: final.Clone()})
	}
	mustEmit(emit, AgentEvent{Type: EvMessageEnd, Message: final})
	return final
}

// declareToolChanges declares tool loadout changes to the model
// (agent-loop.ts declareToolChanges, upstream 9e05370b2).
//
// agentCtx.Tools is what the runtime can execute; the transcript's system
// messages declare what the model may call. Before each request the difference
// becomes ToolsAdded and ToolsRemoved on a system message. When a pending
// system message exists, its tool fields are treated as intent and replaced
// with the delta between the committed transcript and the executable set, so
// replay always yields exactly agentCtx.Tools. Otherwise a new system message
// is inserted before the first non-system pending message. When nothing
// changes, the caller's slice is returned as is.
func declareToolChanges(agentCtx AgentContext, pending []AgentMessage) []AgentMessage {
	systemIndex := -1
	var system ai.SystemMessage
	for i := len(pending) - 1; i >= 0; i-- {
		if m, ok := systemMessageOf(pending[i]); ok {
			systemIndex, system = i, m
			break
		}
	}

	baseline := pending
	if systemIndex >= 0 {
		baseline = slices.Clone(pending)
		baseline[systemIndex] = system.WithToolChanges(ai.ToolStateChanges{})
	}
	executable := make([]ai.Tool, len(agentCtx.Tools))
	for i, tool := range agentCtx.Tools {
		executable[i] = ai.ToToolDeclaration(tool.asAITool())
	}
	changes := ai.GetToolStateChanges(
		ai.GetCurrentTools(slices.Concat(agentCtx.Messages, baseline)),
		executable,
	)
	unchanged := len(changes.ToolsAdded) == 0 && len(changes.ToolsRemoved) == 0

	if systemIndex >= 0 {
		// Keep the caller's messages when the pending one already declares no
		// tool changes.
		if unchanged && len(system.ToolsAdded) == 0 && len(system.ToolsRemoved) == 0 {
			return pending
		}
		baseline[systemIndex] = system.WithToolChanges(changes)
		return baseline
	}
	if unchanged {
		return pending
	}
	update := ai.NewSystemText("", nowMillis()).WithToolChanges(changes)
	index := slices.IndexFunc(pending, func(m AgentMessage) bool { return m.MessageRole() != ai.RoleSystem })
	if index == -1 {
		index = len(pending)
	}
	return slices.Concat(pending[:index], []AgentMessage{update}, pending[index:])
}

// systemMessageOf reads a system message held by value or by pointer.
func systemMessageOf(m AgentMessage) (ai.SystemMessage, bool) {
	switch v := m.(type) {
	case ai.SystemMessage:
		return v, true
	case *ai.SystemMessage:
		if v != nil {
			return *v, true
		}
	}
	return ai.SystemMessage{}, false
}

func defaultConvertToLlm(messages []AgentMessage) []ai.Message {
	var out []ai.Message
	for _, m := range messages {
		switch m.MessageRole() {
		case ai.RoleSystem, ai.RoleUser, ai.RoleAssistant, ai.RoleToolResult:
			out = append(out, m)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Tool execution
// ---------------------------------------------------------------------------

type executedBatch struct {
	messages  []ai.ToolResultMessage
	terminate bool
}

func findTool(tools []AgentTool, name string) (AgentTool, bool) {
	for _, t := range tools {
		if t.Name == name {
			return t, true
		}
	}
	return AgentTool{}, false
}

// failToolCallsFromTruncatedMessage fails all tool calls from an assistant
// message that was truncated by the output token limit. Streamed tool-call
// arguments are finalized with a best-effort JSON salvage parser, so a
// truncated message can yield tool calls whose arguments parse and validate but
// are silently incomplete. None of them are safe to execute; report each as an
// error so the model can re-issue them.
func failToolCallsFromTruncatedMessage(toolCalls []ai.ToolCall, emit EventSink) executedBatch {
	messages := make([]ai.ToolResultMessage, 0, len(toolCalls))
	for _, tc := range toolCalls {
		mustEmit(emit, AgentEvent{Type: EvToolExecutionStart, ToolCallID: tc.ID, ToolName: tc.Name, Args: tc.Arguments})
		fo := finalizedOutcome{
			toolCall: tc,
			// %s, not %q: pi interpolates the name into a template literal
			// unescaped, so a name containing " or \ must pass through raw.
			result: errorToolResult(fmt.Sprintf(
				`Tool call "%s" was not executed: the response hit the output token limit, so its arguments may be truncated. Re-issue the tool call with complete arguments.`,
				tc.Name,
			)),
			isError: true,
		}
		emitToolExecutionEnd(fo, emit)
		trm := createToolResultMessage(fo)
		emitToolResultMessage(trm, emit)
		messages = append(messages, trm)
	}
	return executedBatch{messages: messages, terminate: false}
}

func executeToolCalls(ctx context.Context, current *AgentContext, msg *ai.AssistantMessage, config AgentLoopConfig, emit EventSink) executedBatch {
	toolCalls := filterToolCalls(msg)
	hasSequential := false
	for _, tc := range toolCalls {
		if t, ok := findTool(current.Tools, tc.Name); ok && t.ExecutionMode == ToolSequential {
			hasSequential = true
			break
		}
	}
	if config.ToolExecution == ToolSequential || hasSequential {
		return executeToolCallsSequential(ctx, current, msg, toolCalls, config, emit)
	}
	return executeToolCallsParallel(ctx, current, msg, toolCalls, config, emit)
}

type finalizedOutcome struct {
	toolCall ai.ToolCall
	result   AgentToolResult
	isError  bool
}

func shouldTerminateBatch(calls []finalizedOutcome) bool {
	if len(calls) == 0 {
		return false
	}
	for _, c := range calls {
		if !c.result.Terminate {
			return false
		}
	}
	return true
}

func executeToolCallsSequential(ctx context.Context, current *AgentContext, msg *ai.AssistantMessage, toolCalls []ai.ToolCall, config AgentLoopConfig, emit EventSink) executedBatch {
	var finalized []finalizedOutcome
	var messages []ai.ToolResultMessage

	for _, tc := range toolCalls {
		mustEmit(emit, AgentEvent{Type: EvToolExecutionStart, ToolCallID: tc.ID, ToolName: tc.Name, Args: tc.Arguments})

		prep := prepareToolCall(ctx, current, msg, tc, config)
		var fo finalizedOutcome
		if prep.immediate != nil {
			fo = finalizedOutcome{toolCall: tc, result: prep.immediate.result, isError: prep.immediate.isError}
		} else {
			executed := executePreparedToolCall(ctx, *prep.prepared, emit)
			fo = finalizeExecutedToolCall(ctx, current, msg, *prep.prepared, executed, config)
		}

		emitToolExecutionEnd(fo, emit)
		trm := createToolResultMessage(fo)
		emitToolResultMessage(trm, emit)
		finalized = append(finalized, fo)
		messages = append(messages, trm)

		if aborted(ctx) {
			break
		}
	}

	return executedBatch{messages: messages, terminate: shouldTerminateBatch(finalized)}
}

func executeToolCallsParallel(ctx context.Context, current *AgentContext, msg *ai.AssistantMessage, toolCalls []ai.ToolCall, config AgentLoopConfig, emit EventSink) executedBatch {
	// pi runs the parallel tool batch as `Promise.all`: tool `execute` bodies
	// are concurrently *scheduled*, but JS never interleaves the synchronous
	// bodies of hooks (afterToolCall) or the shared-context mutations between
	// them. We preserve real parallelism for tool execution while serializing
	// everything that touches shared state — event emission, hook invocation,
	// and result/context mutation — under a single mutex. This keeps the emit
	// order and tool-result ordering identical to pi and is race-free.
	var serialMu sync.Mutex
	safeEmit := func(e AgentEvent) error {
		serialMu.Lock()
		defer serialMu.Unlock()
		return emit(e)
	}

	type slot struct {
		immediate *finalizedOutcome
		toolCall  ai.ToolCall
		thunk     func() finalizedOutcome
	}
	slots := make([]slot, 0, len(toolCalls))

	for _, tc := range toolCalls {
		mustEmit(safeEmit, AgentEvent{Type: EvToolExecutionStart, ToolCallID: tc.ID, ToolName: tc.Name, Args: tc.Arguments})

		// prepareToolCall runs the BeforeToolCall hook; the prepare loop is
		// already sequential (matches pi), so Before hooks never interleave.
		prep := prepareToolCall(ctx, current, msg, tc, config)
		if prep.immediate != nil {
			fo := finalizedOutcome{toolCall: tc, result: prep.immediate.result, isError: prep.immediate.isError}
			emitToolExecutionEnd(fo, safeEmit)
			slots = append(slots, slot{immediate: &fo})
			if aborted(ctx) {
				break
			}
			continue
		}
		prepared := *prep.prepared
		slots = append(slots, slot{toolCall: prepared.toolCall, thunk: func() finalizedOutcome {
			// Tool execution runs in parallel, OUTSIDE the lock (pi's Promise.all).
			executed := executePreparedToolCall(ctx, prepared, safeEmit)
			// Finalization runs the AfterToolCall hook and reads/writes shared
			// context; serialize it so hook bodies cannot interleave (pi
			// single-thread). The critical section is func-scoped with a deferred
			// unlock so a PANICKING listener (or hook) cannot leak the mutex and
			// deadlock the other tool goroutines at wg.Wait. A listener ERROR
			// (non-panic) still propagates as emitPanic AFTER the unlock.
			var fo finalizedOutcome
			var emitErr error
			func() {
				serialMu.Lock()
				defer serialMu.Unlock()
				fo = finalizeExecutedToolCall(ctx, current, msg, prepared, executed, config)
				emitErr = emit(AgentEvent{
					Type:       EvToolExecutionEnd,
					ToolCallID: fo.toolCall.ID,
					ToolName:   fo.toolCall.Name,
					Result:     fo.result,
					IsError:    fo.isError,
				})
			}()
			if emitErr != nil {
				panic(emitPanic{err: emitErr})
			}
			return fo
		}})
		if aborted(ctx) {
			break
		}
	}

	ordered := make([]finalizedOutcome, len(slots))
	var wg sync.WaitGroup
	var panicOnce sync.Once
	var panicVal any
	// pi afda4d620 (#8936): preparation is sequential but execution is deferred,
	// so an abort raised while a LATER call is being prepared must still stop the
	// earlier, already-prepared ones. pi re-checks the signal at the head of every
	// deferred entry, and JS runs all of those checks inside ONE uninterruptible
	// turn (`finalizedCalls.map(entry => entry())`), so an externally-raised abort
	// is all-or-nothing for the batch and can never land between two checks.
	//
	// We reproduce that by deciding ONCE here, on the loop goroutine, before any
	// tool body starts. Checking inside each goroutine instead would let the
	// scheduler split the batch: a user abort landing just after the batch starts
	// would skip whichever calls had not been scheduled yet, while pi runs them
	// all. An aborted call yields "Operation aborted" and skips execute AND
	// finalize, so AfterToolCall never runs for it; deciding here also keeps those
	// end events in slot order, as pi's synchronous map does.
	batchAborted := aborted(ctx)
	for i, s := range slots {
		if s.immediate != nil {
			ordered[i] = *s.immediate
			continue
		}
		if batchAborted {
			fo := finalizedOutcome{toolCall: s.toolCall, result: errorToolResult("Operation aborted"), isError: true}
			// Emit exactly as a thunk would, but never unwind straight out of this
			// loop: a listener error here must still let the already-spawned tool
			// goroutines be joined at wg.Wait below, so it is routed through the
			// same panicOnce the goroutines use and re-raised after the join.
			func() {
				defer func() {
					if r := recover(); r != nil {
						panicOnce.Do(func() { panicVal = r })
					}
				}()
				emitToolExecutionEnd(fo, safeEmit)
			}()
			ordered[i] = fo
			continue
		}
		wg.Add(1)
		go func(i int, thunk func() finalizedOutcome) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					panicOnce.Do(func() { panicVal = r })
				}
			}()
			ordered[i] = thunk()
		}(i, s.thunk)
	}
	wg.Wait()
	// Re-raise any listener-error/panic from a tool goroutine on the loop
	// goroutine so it unwinds to the run boundary (matches pi rejecting the run).
	if panicVal != nil {
		panic(panicVal)
	}

	var messages []ai.ToolResultMessage
	for _, fo := range ordered {
		trm := createToolResultMessage(fo)
		emitToolResultMessage(trm, emit)
		messages = append(messages, trm)
	}

	return executedBatch{messages: messages, terminate: shouldTerminateBatch(ordered)}
}

type immediateOutcome struct {
	result  AgentToolResult
	isError bool
}

type preparedToolCall struct {
	toolCall ai.ToolCall
	tool     AgentTool
	args     map[string]any
}

type prepareResult struct {
	immediate *immediateOutcome
	prepared  *preparedToolCall
}

func prepareToolCall(ctx context.Context, current *AgentContext, msg *ai.AssistantMessage, tc ai.ToolCall, config AgentLoopConfig) (res prepareResult) {
	tool, ok := findTool(current.Tools, tc.Name)
	if !ok {
		return prepareResult{immediate: &immediateOutcome{result: errorToolResult("Tool " + tc.Name + " not found"), isError: true}}
	}

	// pi wraps prepareArguments/validate/beforeToolCall in try/catch; a throw
	// (panic) becomes an immediate error tool result, not a run failure
	// (agent-loop.ts:578-625). An emitPanic must still unwind to the run boundary.
	defer func() {
		if r := recover(); r != nil {
			if ep, ok := r.(emitPanic); ok {
				panic(ep)
			}
			res = prepareResult{immediate: &immediateOutcome{result: errorToolResult(panicMessage(r)), isError: true}}
		}
	}()

	prepared := tc
	if tool.PrepareArguments != nil {
		if newArgs := tool.PrepareArguments(tc.Arguments); newArgs != nil {
			prepared = ai.ToolCall{ID: tc.ID, Name: tc.Name, Arguments: newArgs, ThoughtSignature: tc.ThoughtSignature}
		}
	}

	validated, err := ai.ValidateToolArguments(tool.asAITool(), prepared)
	if err != nil {
		return prepareResult{immediate: &immediateOutcome{result: errorToolResult(err.Error()), isError: true}}
	}

	if config.BeforeToolCall != nil {
		before := config.BeforeToolCall(ctx, BeforeToolCallContext{
			AssistantMessage: msg,
			ToolCall:         tc,
			Args:             validated,
			Context:          current,
		})
		if aborted(ctx) {
			return prepareResult{immediate: &immediateOutcome{result: errorToolResult("Operation aborted"), isError: true}}
		}
		if before != nil && before.Block {
			reason := before.Reason
			if reason == "" {
				reason = "Tool execution was blocked"
			}
			result := errorToolResult(reason)
			// pi 1eb988cfe: a blocked call can opt into the batch
			// early-termination rule. pi guards on `terminate === true`, which
			// a plain bool reproduces exactly -- absent and false both leave
			// the field unset.
			result.Terminate = before.Terminate
			return prepareResult{immediate: &immediateOutcome{result: result, isError: true}}
		}
	}
	if aborted(ctx) {
		return prepareResult{immediate: &immediateOutcome{result: errorToolResult("Operation aborted"), isError: true}}
	}
	return prepareResult{prepared: &preparedToolCall{toolCall: tc, tool: tool, args: validated}}
}

func executePreparedToolCall(ctx context.Context, prepared preparedToolCall, emit EventSink) immediateOutcome {
	// pi buffers tool_execution_update emit promises and awaits them only after
	// execute settles (agent-loop.ts:633-654): a listener error never interrupts
	// the tool mid-flight; it surfaces afterwards and rejects the run. We mirror
	// that by recording the first onUpdate emit error and re-raising it (as
	// emitPanic) once Execute has finished.
	var updateMu sync.Mutex
	var updateEmitErr error
	// pi daab056a (#5573): the onUpdate callback is scoped to the current
	// execute() invocation. Updates fired after execute settles (e.g. from a
	// goroutine the tool spawned) are dropped instead of emitting stale
	// tool_execution_update events. acceptingUpdates is guarded by updateMu so
	// the check and the post-settlement flip are race-free.
	acceptingUpdates := true
	onUpdate := func(partial AgentToolResult) {
		updateMu.Lock()
		if !acceptingUpdates {
			updateMu.Unlock()
			return
		}
		updateMu.Unlock()
		err := emit(AgentEvent{
			Type:          EvToolExecutionUpdate,
			ToolCallID:    prepared.toolCall.ID,
			ToolName:      prepared.toolCall.Name,
			Args:          prepared.toolCall.Arguments,
			PartialResult: partial,
		})
		if err != nil {
			updateMu.Lock()
			if updateEmitErr == nil {
				updateEmitErr = err
			}
			updateMu.Unlock()
		}
	}

	// pi wraps execute in try/catch (agent-loop.ts:635-663): a throwing tool
	// yields an error tool result (text = the thrown error's message, matching
	// createErrorToolResult) and the loop CONTINUES. An emitPanic (listener
	// error) must still unwind to the run boundary, like the recovers in
	// prepareToolCall/finalizeExecutedToolCall.
	outcome := func() (out immediateOutcome) {
		defer func() {
			if r := recover(); r != nil {
				if ep, ok := r.(emitPanic); ok {
					panic(ep)
				}
				out = immediateOutcome{result: errorToolResult(panicMessage(r)), isError: true}
			}
		}()
		result, err := prepared.tool.Execute(ctx, prepared.toolCall.ID, prepared.args, onUpdate)
		if err != nil {
			return immediateOutcome{result: errorToolResult(err.Error()), isError: true}
		}
		return immediateOutcome{result: result, isError: false}
	}()

	// pi daab056a (#5573): stop accepting updates the moment execute() settles
	// (success, error, or panic-recovered above), so late callbacks are dropped.
	updateMu.Lock()
	acceptingUpdates = false
	updateMu.Unlock()

	// pi's `await Promise.all(updateEvents)` runs in both the try and catch
	// paths, so a listener rejection wins over the tool outcome either way.
	updateMu.Lock()
	emitErr := updateEmitErr
	updateMu.Unlock()
	if emitErr != nil {
		panic(emitPanic{err: emitErr})
	}
	return outcome
}

func finalizeExecutedToolCall(ctx context.Context, current *AgentContext, msg *ai.AssistantMessage, prepared preparedToolCall, executed immediateOutcome, config AgentLoopConfig) finalizedOutcome {
	result := executed.result
	isError := executed.isError

	if config.AfterToolCall != nil {
		// pi wraps afterToolCall in try/catch; a throw (panic) becomes an error
		// tool result, not a run failure (agent-loop.ts:676-701).
		func() {
			defer func() {
				if r := recover(); r != nil {
					if ep, ok := r.(emitPanic); ok {
						panic(ep)
					}
					result = errorToolResult(panicMessage(r))
					isError = true
				}
			}()
			after := config.AfterToolCall(ctx, AfterToolCallContext{
				AssistantMessage: msg,
				ToolCall:         prepared.toolCall,
				Args:             prepared.args,
				Result:           result,
				IsError:          isError,
				Context:          current,
			})
			// pi rebuilds `result = {...result, content, details, terminate}`; the
			// spread preserves fields the after-hook does not override. Go mutates
			// `result` in place, which preserves them the same way.
			if after != nil {
				if after.HasContent {
					result.Content = after.Content
				}
				if after.HasDetails {
					result.Details = after.Details
				}
				if after.Terminate != nil {
					result.Terminate = *after.Terminate
				}
				if after.IsError != nil {
					isError = *after.IsError
				}
			}
		}()
	}

	return finalizedOutcome{toolCall: prepared.toolCall, result: result, isError: isError}
}

func errorToolResult(message string) AgentToolResult {
	return AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: message}}, Details: map[string]any{}}
}

func emitToolExecutionEnd(fo finalizedOutcome, emit EventSink) {
	mustEmit(emit, AgentEvent{
		Type:       EvToolExecutionEnd,
		ToolCallID: fo.toolCall.ID,
		ToolName:   fo.toolCall.Name,
		Result:     fo.result,
		IsError:    fo.isError,
	})
}

func createToolResultMessage(fo finalizedOutcome) ai.ToolResultMessage {
	return ai.ToolResultMessage{
		ToolCallID: fo.toolCall.ID,
		ToolName:   fo.toolCall.Name,
		Content:    fo.result.Content,
		Details:    fo.result.Details,
		IsError:    fo.isError,
		Timestamp:  nowMillis(),
	}
}

func emitToolResultMessage(trm ai.ToolResultMessage, emit EventSink) {
	mustEmit(emit, AgentEvent{Type: EvMessageStart, Message: trm})
	mustEmit(emit, AgentEvent{Type: EvMessageEnd, Message: trm})
}
