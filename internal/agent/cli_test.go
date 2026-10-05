package agent

import (
	"strings"
	"testing"
)

func parse(engine string, lines ...string) ([]Event, cliResult) {
	var evs []Event
	var res cliResult
	p := newCLIParser(engine, func(e Event) { evs = append(evs, e) }, &res)
	for _, l := range lines {
		p.line([]byte(l))
	}
	p.finish()
	return evs, res
}

func types(evs []Event) string {
	var s []string
	for _, e := range evs {
		s = append(s, e.Type+":"+e.Tool)
	}
	return strings.Join(s, " ")
}

func TestClaudeParser(t *testing.T) {
	evs, res := parse(EngineClaude,
		`{"type":"system","subtype":"init","session_id":"s1","model":"m"}`,
		`{"type":"stream_event","event":{"type":"message_start","message":{"id":"m1"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"看看"}}}`,
		`{"type":"assistant","message":{"id":"m1","content":[{"type":"text","text":"看看"}]}}`,
		`{"type":"assistant","message":{"id":"m1","content":[{"type":"tool_use","id":"t1","name":"mcp__shuttle__db_query","input":{"table":"leads"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"[]"}]}]}}`,
		`{"type":"assistant","message":{"id":"m2","content":[{"type":"text","text":"没有询盘"}]}}`,
		`{"type":"result","session_id":"s1","is_error":false,"usage":{"input_tokens":3,"output_tokens":5}}`,
	)
	if got := types(evs); got != "turn_start: text: tool_start:db_query tool_end: turn_end: turn_start: text: turn_end:" {
		t.Fatal(got)
	}
	if res.session != "s1" || res.usage.Output != 5 {
		t.Fatal(res)
	}
}

func TestCodexParser(t *testing.T) {
	evs, res := parse(EngineCodex,
		`{"type":"thread.started","thread_id":"th"}`,
		`{"type":"item.completed","item":{"id":"e","type":"error","message":"config warning"}}`,
		`{"type":"item.started","item":{"id":"i1","type":"command_execution","command":"/bin/zsh -lc 'cat a.txt'","status":"in_progress"}}`,
		`{"type":"item.completed","item":{"id":"i1","type":"command_execution","command":"/bin/zsh -lc 'cat a.txt'","aggregated_output":"x","exit_code":1,"status":"completed"}}`,
		`{"type":"item.completed","item":{"id":"i2","type":"agent_message","text":"好了"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":10,"cached_input_tokens":4,"output_tokens":2}}`,
	)
	if got := types(evs); got != "turn_start: tool_start:bash tool_end:bash turn_end: turn_start: text: context: turn_end:" {
		t.Fatal(got)
	}
	if evs[1].Args["command"] != "cat a.txt" || !evs[2].IsError {
		t.Fatal(evs[1], evs[2])
	}
	if res.session != "th" || res.usage.Input != 6 || res.usage.CacheRead != 4 {
		t.Fatal(res)
	}
}
