package agent

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	piagent "github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"

	"github.com/annulo/annulo/internal/version"
)

// 给外部 agent（Claude Code / Codex）的 MCP 接口：把 Shuttle 给 pi 的工具（request_user_input、db_query、page_errors、
// 已连接的 MCP 工具）原样开放出去，它们用自己的 MCP 客户端调。只认这个进程随机生成的 token，跑外部 agent 时经环境变量 / 配置给它。

// MCPHandler 是 /_shuttle/mcp 的处理器。工具每个 MCP 会话开始时取当时的那一份（SetTools 换了，下一轮新会话就是新的）。
func (a *Agent) MCPHandler() http.Handler {
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		srv := mcp.NewServer(&mcp.Implementation{Name: "shuttle", Version: version.Version}, nil)
		a.mu.Lock()
		tools := append([]piagent.AgentTool{a.requestUserInputTool()}, a.tools...)
		a.mu.Unlock()
		for _, t := range tools {
			addMCPTool(srv, t)
		}
		return srv
	}, nil)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(got), []byte(a.mcpToken)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func newMCPToken() string {
	b := make([]byte, 24)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func addMCPTool(srv *mcp.Server, t piagent.AgentTool) {
	var schema any = map[string]any{"type": "object"}
	if t.Parameters != nil {
		if b, err := json.Marshal(t.Parameters); err == nil {
			var m map[string]any
			if json.Unmarshal(b, &m) == nil && m != nil {
				if m["type"] == nil {
					m["type"] = "object"
				}
				schema = m
			}
		}
	}
	exec := t.Execute
	srv.AddTool(&mcp.Tool{Name: t.Name, Description: t.Description, InputSchema: schema}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := map[string]any{}
		if len(req.Params.Arguments) > 0 {
			if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
				return errResult("invalid arguments: " + err.Error()), nil
			}
		}
		if t.PrepareArguments != nil {
			args = t.PrepareArguments(args)
		}
		res, err := exec(ctx, fmt.Sprintf("mcp-%p", req), args, nil)
		if err != nil {
			return errResult(err.Error()), nil
		}
		out := &mcp.CallToolResult{}
		for _, c := range res.Content {
			switch v := c.(type) {
			case ai.TextContent:
				out.Content = append(out.Content, &mcp.TextContent{Text: v.Text})
			case ai.ImageContent:
				if b, err := base64.StdEncoding.DecodeString(v.Data); err == nil {
					out.Content = append(out.Content, &mcp.ImageContent{Data: b, MIMEType: v.MimeType})
				}
			}
		}
		if len(out.Content) == 0 {
			out.Content = []mcp.Content{&mcp.TextContent{Text: "ok"}}
		}
		return out, nil
	})
}

func errResult(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: msg}}}
}
