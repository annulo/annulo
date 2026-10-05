package mcphub

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestCall(t *testing.T) {
	ctx := context.Background()
	srv := mcp.NewServer(&mcp.Implementation{Name: "fake"}, nil)
	type in struct {
		N int `json:"n"`
	}
	mcp.AddTool(srv, &mcp.Tool{Name: "json"}, func(_ context.Context, _ *mcp.CallToolRequest, a in) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: `{"n":` + string(rune('0'+a.N)) + `}`}}}, nil, nil
	})
	mcp.AddTool(srv, &mcp.Tool{Name: "text"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "hello"}}}, nil, nil
	})
	mcp.AddTool(srv, &mcp.Tool{Name: "fail"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "没权限"}}}, nil, nil
	})
	mcp.AddTool(srv, &mcp.Tool{Name: "off"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{}, nil, nil
	})
	st, ct := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	sess, err := mcp.NewClient(&mcp.Implementation{Name: "c"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	lt, err := sess.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}

	h := New(t.TempDir())
	h.servers["s"] = &server{name: "s", status: "connected", session: sess, tools: lt.Tools, cfg: ServerConfig{ExcludeTools: []string{"off"}}}
	h.servers["auth"] = &server{name: "auth", status: "needs_auth"}

	if v, err := h.Call(ctx, "s", "json", map[string]any{"n": 3}); err != nil || v.(map[string]any)["n"] != float64(3) {
		t.Fatalf("JSON 文本应该解析成对象：%#v %v", v, err)
	}
	if v, err := h.Call(ctx, "s", "text", nil); err != nil || v != "hello" {
		t.Fatalf("普通文本原样返回：%#v %v", v, err)
	}
	for _, c := range []struct{ server, tool, want string }{
		{"s", "fail", "没权限"},
		{"s", "off", "关掉了"},
		{"s", "nope", "没有工具"},
		{"auth", "x", "还没授权"},
		{"none", "x", "没有叫 none"},
	} {
		if _, err := h.Call(ctx, c.server, c.tool, nil); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s/%s 应该报「%s」，得到 %v", c.server, c.tool, c.want, err)
		}
	}

	// 连上之后 server 新加的工具：Call 时重新拉列表，不用重连
	mcp.AddTool(srv, &mcp.Tool{Name: "later"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "new"}}}, nil, nil
	})
	if v, err := h.Call(ctx, "s", "later", nil); err != nil || v != "new" {
		t.Fatalf("新加的工具应该能调：%#v %v", v, err)
	}
}
