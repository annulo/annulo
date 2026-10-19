package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/annulo/annulo/internal/i18n"
	"github.com/annulo/annulo/internal/localfn"
	"github.com/annulo/annulo/internal/relay"
)

// 函数在节点上跑（docs/server-and-multi-project.md「浏览器：节点」）：服务端部署时，本机函数文件里 export const node = [...] 列的
// 函数整个派给节点（用户电脑上的 Annulo）跑。不复用手机的 remote：那里面很多函数只是要密钥、连接、模型，服务器上就有，不用绕到电脑上。
//
//   - 服务器把函数所在文件打成一份 JS（localfn.Bundle）发过去，节点不用有项目副本；
//   - 节点上的 ctx：浏览器、fetch 用节点自己的（用户的登录态、出口 IP）；表、密钥、模型、MCP、上传、连接回调服务器（数据只在服务器一份）；
//     不给 ctx.exec（节点只借出浏览器和网络）；
//   - 节点把这次运行登记成本机的一次运行（「服务器：<函数>」），右上角的浏览器列表里看得到、能停。
//
// 没声明 node 的函数照旧在服务器上跑，用 ctx.browser 时每个操作转给节点（nodes.go 的 remoteBrowser）。

// runFn 跑一次本机函数：服务器上声明了 node 的派给节点，其余在这里跑。
func (s *Server) runFn(ctx context.Context, host localfn.Host, fn string, input any, emit func(localfn.Event)) (any, error) {
	if s.cfg.Server && s.nodeHub != nil {
		if ok, _ := localfn.IsNode(host.WorkDir, fn); ok {
			return s.runOnNode(ctx, host, fn, input, emit)
		}
	}
	return localfn.Run(ctx, host, fn, input, emit)
}

func (s *Server) runOnNode(ctx context.Context, host localfn.Host, fn string, input any, emit func(localfn.Event)) (any, error) {
	code, err := localfn.Bundle(host.WorkDir, fn)
	if err != nil {
		return nil, err
	}
	_, def := s.nodeTokens.list()
	node, err := s.nodeHub.Pick("run", def)
	if errors.Is(err, relay.ErrNoNode) {
		return nil, i18n.Errorf("%s 要在电脑上跑（声明了 node），但没有连着的电脑：在你电脑上的 Annulo 里打开 设置 → 节点，连到这台服务器",
			"%s has to run on a computer (it's declared node), but no computer is connected: in Annulo on your computer, open Settings → Nodes and connect to this server", fn)
	}
	if err != nil {
		return nil, err
	}
	project, _ := host.Workspace["project_id"].(string)
	emit(localfn.Event{Type: "log", Data: map[string]any{"level": "info", "message": i18n.T("在节点上跑", "Running on a node")}})
	raw, err := s.nodeHub.CallWith(ctx, node, "run", "fn", map[string]any{
		"project": project, "fn": fn, "code": code, "input": input, "workspace": host.Workspace, "chat_id": host.ChatID,
	}, relay.CallOpts{
		Handle: func(ctx context.Context, op string, args json.RawMessage) (any, error) {
			return nodeCallback(ctx, host, op, args)
		},
		Progress: func(d json.RawMessage) {
			var ev localfn.Event
			if json.Unmarshal(d, &ev) == nil && ev.Type != "" {
				emit(ev)
			}
		},
	})
	if errors.Is(err, relay.ErrNoNode) {
		return nil, i18n.New("节点断开了", "The node disconnected")
	}
	if err != nil {
		return nil, err
	}
	var v any
	if len(raw) > 0 {
		json.Unmarshal(raw, &v)
	}
	return v, nil
}

// nodeCallback：节点上的函数回调服务器（读写表、密钥、模型、MCP、上传、连接），用服务器这个项目的 host 做。
// nodeFileChunk：节点取本机文件一次取多少（base64 后要在通道单条消息 32MB 以内）
const nodeFileChunk = 8 << 20

func nodeCallback(ctx context.Context, h localfn.Host, op string, raw json.RawMessage) (any, error) {
	var in struct {
		Table, ID, Name, Provider, Account, Server, Tool, System, Prompt, ContentType, Data string
		Offset                                                                              int64
		Size                                                                                int
		Query                                                                               localfn.Query
		Req                                                                                 map[string]any
		Filter                                                                              []localfn.Cond
		Row                                                                                 map[string]any
		Args                                                                                map[string]any
		Ask                                                                                 localfn.LLMAsk
		Tools                                                                               []localfn.AgentTool
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
	}
	missing := i18n.Errorf("服务器上没有 %s", "The server doesn't provide %s", op)
	switch op {
	case "db.query":
		list, next, total, err := h.DB.Query(ctx, in.Table, in.Query)
		return map[string]any{"list": list, "next": next, "total": total}, err
	case "db.aggregate":
		list, truncated, err := h.DB.Aggregate(ctx, in.Table, in.Req, in.Filter)
		return map[string]any{"list": list, "truncated": truncated}, err
	case "db.get":
		return h.DB.Get(ctx, in.Table, in.ID)
	case "db.insert":
		return h.DB.Insert(ctx, in.Table, in.Row)
	case "db.update":
		return h.DB.Update(ctx, in.Table, in.ID, in.Row)
	case "db.delete":
		return nil, h.DB.Delete(ctx, in.Table, in.ID)
	case "secret":
		if h.Secret == nil {
			return map[string]any{"ok": false}, nil
		}
		v, ok := h.Secret(in.Name)
		return map[string]any{"value": v, "ok": ok}, nil
	case "file.read":
		// 节点取项目的本机文件（local:<name>，b.upload 用）：一次一段，节点按 offset 接着取，直到 eof
		if h.FilesDir == "" || !localFileRe.MatchString(in.Name) {
			return nil, i18n.Errorf("本机文件不对：%s", "Invalid local file: %s", in.Name)
		}
		f, err := os.Open(filepath.Join(h.FilesDir, in.Name))
		if err != nil {
			return nil, i18n.Errorf("服务器上没有这个本机文件：%s", "The server doesn't have this local file: %s", in.Name)
		}
		defer f.Close()
		size := min(max(in.Size, 1), nodeFileChunk)
		buf := make([]byte, size)
		n, err := f.ReadAt(buf, in.Offset)
		if err != nil && err != io.EOF {
			return nil, err
		}
		return map[string]any{"data": base64.StdEncoding.EncodeToString(buf[:n]), "eof": n < size || err == io.EOF}, nil
	case "upload":
		if h.Upload == nil {
			return nil, missing
		}
		b, err := base64.StdEncoding.DecodeString(in.Data)
		if err != nil {
			return nil, err
		}
		return h.Upload(ctx, in.Name, in.ContentType, b)
	case "llm":
		if h.LLM == nil {
			return nil, missing
		}
		return h.LLM(ctx, in.System, in.Prompt)
	case "agent.tools":
		if h.AgentTools == nil {
			return nil, missing
		}
		rep, err := h.AgentTools(ctx, in.System, in.Prompt, in.Tools)
		if errors.Is(err, localfn.ErrNoToolCalls) {
			return map[string]any{"unsupported": true}, nil
		}
		return rep, err
	case "agent.llm":
		f := h.AgentLLM
		if f == nil {
			f = h.LLM
		}
		if f == nil {
			return nil, missing
		}
		return f(ctx, in.System, in.Prompt)
	case "browser.llm":
		f := h.BrowserLLM
		if f == nil {
			f = h.LLM
		}
		if f == nil {
			return nil, missing
		}
		return f(ctx, in.System, in.Prompt)
	case "llm.ask":
		if h.LLMAsk == nil {
			return nil, missing
		}
		return h.LLMAsk(ctx, in.Ask)
	case "llm.providers":
		if h.LLMProviders == nil {
			return []any{}, nil
		}
		return h.LLMProviders(ctx), nil
	case "mcp":
		if h.MCP == nil {
			return nil, missing
		}
		return h.MCP(ctx, in.Server, in.Tool, in.Args)
	case "mcp.servers":
		if h.MCPServers == nil {
			return []any{}, nil
		}
		return h.MCPServers(), nil
	case "agent.current":
		if h.AgentCurrent == nil {
			return localfn.AgentModel{}, nil
		}
		return h.AgentCurrent(), nil
	case "oauth":
		if h.OAuth == nil {
			return nil, missing
		}
		return h.OAuth(ctx, in.Provider, in.Account)
	case "oauth.accounts":
		if h.OAuthAccounts == nil {
			return []string{}, nil
		}
		return h.OAuthAccounts(in.Provider)
	}
	return nil, i18n.Errorf("不认识的回调：%s", "Unknown callback: %s", op)
}

// ---- 节点这头 ----

// nodeRunFn 执行服务器派来的函数：登记成本机的一次运行（右上角看得到、能停），ctx 里服务器的能力经 call.Request 回调。
func (s *Server) nodeRunFn(ctx context.Context, call *relay.Call) (any, error) {
	var in struct {
		Project   string         `json:"project"`
		Fn        string         `json:"fn"`
		Code      string         `json:"code"`
		Input     any            `json:"input"`
		Workspace map[string]any `json:"workspace"`
		ChatID    string         `json:"chat_id"`
	}
	if err := json.Unmarshal(call.Args, &in); err != nil {
		return nil, err
	}
	if in.Project != "" && !validProjectID(in.Project) {
		return nil, i18n.Errorf("项目 id 不对：%q", "Invalid project id: %q", in.Project)
	}
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	run := &localRun{id: fmt.Sprintf("n%d", time.Now().UnixNano()), started: time.Now(), fn: i18n.T("服务器：", "Server: ") + in.Fn, cancel: cancel, wake: make(chan struct{})}
	s.local.mu.Lock()
	if s.local.runs == nil {
		s.local.runs = map[string]*localRun{}
	}
	s.local.runs[run.id] = run
	s.local.mu.Unlock()
	defer func() {
		run.push(map[string]any{"type": "result"}, true)
		time.AfterFunc(time.Minute, func() {
			s.local.mu.Lock()
			delete(s.local.runs, run.id)
			s.local.mu.Unlock()
		})
	}()

	req := func(op string, args any, out any) error {
		raw, err := call.Request(rctx, op, args)
		if err != nil {
			return err
		}
		if out != nil && len(raw) > 0 {
			return json.Unmarshal(raw, out)
		}
		return nil
	}
	logs := filepath.Join(s.cfg.Dir, "logs", "local", "node-"+in.Project)
	h := localfn.Host{
		LogDir:    logs,
		RunID:     run.id,
		Workspace: in.Workspace,
		ChatID:    in.ChatID,
		DB:        nodeDB{req},
		Secret: func(name string) (string, bool) {
			var out struct {
				Value string
				OK    bool `json:"ok"`
			}
			if req("secret", map[string]any{"name": name}, &out) != nil {
				return "", false
			}
			return out.Value, out.OK
		},
		LocalFile: nodeLocalFile(req),
		Fetch:     s.fetchWithAssets, // 用这台电脑的网络（出口 IP）
		Download:  s.downloadWithAssets,
		Upload: func(ctx context.Context, name, ctype string, b []byte) (map[string]any, error) {
			var out map[string]any
			err := req("upload", map[string]any{"name": name, "contenttype": ctype, "data": base64.StdEncoding.EncodeToString(b)}, &out)
			return out, err
		},
		LLM: func(ctx context.Context, system, prompt string) (string, error) {
			var out string
			err := req("llm", map[string]any{"system": system, "prompt": prompt}, &out)
			return out, err
		},
		BrowserLLM: func(ctx context.Context, system, prompt string) (string, error) {
			var out string
			err := req("browser.llm", map[string]any{"system": system, "prompt": prompt}, &out)
			return out, err
		},
		AgentTools: func(ctx context.Context, system, prompt string, tools []localfn.AgentTool) (*localfn.AgentReply, error) {
			var out struct {
				localfn.AgentReply
				Unsupported bool `json:"unsupported"`
			}
			if err := req("agent.tools", map[string]any{"system": system, "prompt": prompt, "tools": tools}, &out); err != nil {
				return nil, err
			}
			if out.Unsupported {
				return nil, localfn.ErrNoToolCalls
			}
			return &out.AgentReply, nil
		},
		AgentLLM: func(ctx context.Context, system, prompt string) (string, error) {
			var out string
			err := req("agent.llm", map[string]any{"system": system, "prompt": prompt}, &out)
			return out, err
		},
		LLMAsk: func(ctx context.Context, q localfn.LLMAsk) (*localfn.LLMAnswer, error) {
			var out localfn.LLMAnswer
			err := req("llm.ask", map[string]any{"ask": q}, &out)
			return &out, err
		},
		LLMProviders: func(ctx context.Context) []map[string]any {
			var out []map[string]any
			req("llm.providers", nil, &out)
			return out
		},
		MCP: func(ctx context.Context, server, tool string, args map[string]any) (any, error) {
			var out any
			err := req("mcp", map[string]any{"server": server, "tool": tool, "args": args}, &out)
			return out, err
		},
		MCPServers: func() []localfn.MCPServer {
			var out []localfn.MCPServer
			req("mcp.servers", nil, &out)
			return out
		},
		AgentCurrent: func() localfn.AgentModel {
			var out localfn.AgentModel
			req("agent.current", nil, &out)
			return out
		},
		OAuth: func(ctx context.Context, provider, account string) (string, error) {
			var out string
			err := req("oauth", map[string]any{"provider": provider, "account": account}, &out)
			return out, err
		},
		OAuthAccounts: func(provider string) ([]string, error) {
			var out []string
			err := req("oauth.accounts", map[string]any{"provider": provider}, &out)
			return out, err
		},
		Browser:     browserHost{m: s.browser, run: run, project: in.Project}, // 这台电脑的浏览器，挂在这次运行上（右上角能看到）
		ActCache:    filepath.Join(s.cfg.Dir, "cache", "browser-act.json"),
		SnapshotDir: filepath.Join(s.cfg.Dir, "snapshots", "node-"+in.Project),
		NoExec:      true,
	}
	os.MkdirAll(logs, 0o700)
	log.Printf("节点：跑服务器派来的 %s", in.Fn)
	return localfn.RunCode(rctx, h, in.Fn, in.Code, in.Input, func(e localfn.Event) {
		run.push(map[string]any{"type": e.Type, "data": e.Data}, false)
		if e.Type == "progress" || e.Type == "log" {
			call.Progress(e)
		}
	})
}

// nodeLocalFile 是节点上的 Host.LocalFile：按段（nodeFileChunk）向服务器要项目的本机文件，写进 dst（服务器那边是 nodeCallback 的 file.read）
func nodeLocalFile(req func(op string, args any, out any) error) func(ctx context.Context, name, dst string) error {
	return func(ctx context.Context, name, dst string) error {
		f, err := os.Create(dst)
		if err != nil {
			return err
		}
		defer f.Close()
		for off := int64(0); ; {
			var out struct {
				Data string
				EOF  bool `json:"eof"`
			}
			if err := req("file.read", map[string]any{"name": name, "offset": off, "size": nodeFileChunk}, &out); err != nil {
				return err
			}
			b, err := base64.StdEncoding.DecodeString(out.Data)
			if err != nil {
				return err
			}
			if _, err := f.Write(b); err != nil {
				return err
			}
			off += int64(len(b))
			if out.EOF || len(b) == 0 {
				return nil
			}
		}
	}
}

// nodeDB 是节点上的 ctx.db：每个操作回调服务器。
type nodeDB struct {
	req func(op string, args any, out any) error
}

func (d nodeDB) Query(_ context.Context, table string, q localfn.Query) ([]map[string]any, string, int64, error) {
	var out struct {
		List  []map[string]any
		Next  string
		Total int64
	}
	err := d.req("db.query", map[string]any{"table": table, "query": q}, &out)
	return out.List, out.Next, out.Total, err
}

func (d nodeDB) Aggregate(_ context.Context, table string, r map[string]any, filter []localfn.Cond) ([]map[string]any, bool, error) {
	var out struct {
		List      []map[string]any
		Truncated bool
	}
	err := d.req("db.aggregate", map[string]any{"table": table, "req": r, "filter": filter}, &out)
	return out.List, out.Truncated, err
}

func (d nodeDB) Get(_ context.Context, table, id string) (map[string]any, error) {
	var out map[string]any
	err := d.req("db.get", map[string]any{"table": table, "id": id}, &out)
	return out, err
}

func (d nodeDB) Insert(_ context.Context, table string, data map[string]any) (map[string]any, error) {
	var out map[string]any
	err := d.req("db.insert", map[string]any{"table": table, "row": data}, &out)
	return out, err
}

func (d nodeDB) Update(_ context.Context, table, id string, data map[string]any) (map[string]any, error) {
	var out map[string]any
	err := d.req("db.update", map[string]any{"table": table, "id": id, "row": data}, &out)
	return out, err
}

func (d nodeDB) Delete(_ context.Context, table, id string) error {
	return d.req("db.delete", map[string]any{"table": table, "id": id}, nil)
}
