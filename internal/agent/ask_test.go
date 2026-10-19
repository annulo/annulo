package agent

import (
	"strings"
	"testing"
)

// 事件的样子照 claude -p --output-format stream-json --verbose 实际输出的（2026-10 的 Claude Code）
func TestParseClaudeSearch(t *testing.T) {
	out := strings.Join([]string{
		`{"type":"system","subtype":"init"}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"WebSearch","input":{"query":"china climbing holds"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","content":"..."}]},"tool_use_result":{"query":"china climbing holds","results":[{"tool_use_id":"x","content":[{"title":"A","url":"https://a.com/1"},{"title":"B","url":"https://b.com"}]}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"WebFetch","input":{"url":"https://a.com/1","prompt":"x"}}]}}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"See [Moonhill](https://a.com/1) and https://c.com/x.","usage":{"input_tokens":4,"cache_creation_input_tokens":10,"cache_read_input_tokens":20,"output_tokens":6}}`,
	}, "\n")
	a, err := parseClaudeSearch([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	if a.Text == "" || a.Tokens != 40 || len(a.Searches) != 2 || a.Searches[0].Queries[0] != "china climbing holds" || len(a.Searches[0].Sources) != 2 || a.Searches[1].Type != "open_page" {
		t.Fatalf("%+v", a)
	}
	src := SourcesInText(a.Text, nil)
	if len(src) != 2 || src[0].URL != "https://a.com/1" || src[0].Title != "Moonhill" || src[1].URL != "https://c.com/x" {
		t.Fatalf("%+v", src)
	}
	if _, err := parseClaudeSearch([]byte(`{"type":"result","is_error":true,"result":"Not logged in"}`)); err == nil || !strings.Contains(err.Error(), "Not logged in") {
		t.Fatal(err)
	}
}

// 事件的样子照 codex --search exec --json 实际输出的：回答是最后一条 agent_message
func TestParseCodexSearch(t *testing.T) {
	out := strings.Join([]string{
		`{"type":"thread.started","thread_id":"t"}`,
		`{"type":"item.completed","item":{"id":"item_0","type":"error","message":"Codex is ignoring 1 unrecognized configuration setting."}}`,
		`{"type":"item.completed","item":{"id":"item_2","type":"agent_message","text":"I'll look it up."}}`,
		`{"type":"item.completed","item":{"type":"web_search","query":"q ...","action":{"type":"search","queries":["q1","q2"]},"results":[{"type":"text_result","url":"https://a.com"}]}}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"Answer: [AOTI](https://aoti.com/)"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":50,"output_tokens":7}}`,
	}, "\n")
	a, err := parseCodexSearch([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	if a.Text != "Answer: [AOTI](https://aoti.com/)" || a.Tokens != 107 || len(a.Searches) != 1 || strings.Join(a.Searches[0].Queries, ",") != "q1,q2" || a.Searches[0].Sources[0] != "https://a.com" {
		t.Fatalf("%+v", a)
	}
	if _, err := parseCodexSearch([]byte(`{"type":"turn.failed","error":{"message":"usage limit"}}`)); err == nil || !strings.Contains(err.Error(), "usage limit") {
		t.Fatal(err)
	}
}

// 接口给的引用注释在前，正文里的链接补上，按网址去重
func TestSourcesInText(t *testing.T) {
	s := SourcesInText("see [X](https://x.com/a). and https://y.com,", []WebSource{{URL: "https://x.com/a"}})
	if len(s) != 2 || s[0].Title != "X" || s[1].URL != "https://y.com" {
		t.Fatalf("%+v", s)
	}
}
