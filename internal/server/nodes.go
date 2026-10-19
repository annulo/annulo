package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/annulo/annulo/internal/i18n"
	"github.com/annulo/annulo/internal/localfn"
	"github.com/annulo/annulo/internal/relay"
)

// 节点（docs/server-and-multi-project.md「浏览器：节点」）：用户电脑上的 Annulo 主动连到服务端部署的 Annulo，
// 把浏览器借给服务器上的本机函数用。连接和中转是同一套协议（internal/relay：Client 在电脑上，Hub 在服务器上）。
//
// 服务器这头（cfg.Server）：
//   - 设置 → 节点 里管理员生成令牌（存哈希，/data/nodes.json），填进电脑上的 Annulo；
//   - /_shuttle/api/nodes/connect 接受连接（令牌认证）；
//   - ctx.browser 在服务器上是 remoteBrowser：每个调用转给节点，节点用它自己的浏览器（独立 profile 或用户的 Chrome）执行。
//
// 电脑这头（本机版）：设置 → 节点 填服务器地址和令牌、打开开关，就连上去；能力调用在 nodeCap 里执行。

const nodesConnectPath = "/_shuttle/api/nodes/connect"

// ---- 服务器：令牌 ----

type nodeToken struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Hash      string `json:"hash,omitempty"`
	CreatedAt string `json:"created_at"`
}

type nodeStore struct {
	path string
	mu   sync.Mutex
	data struct {
		Tokens  []nodeToken `json:"tokens"`
		Default string      `json:"default,omitempty"` // 默认用哪个节点（令牌 id）；空 = 最早连上的
	}
}

func openNodeStore(dir string) *nodeStore {
	s := &nodeStore{path: filepath.Join(dir, "nodes.json")}
	if b, err := os.ReadFile(s.path); err == nil {
		json.Unmarshal(b, &s.data)
	}
	return s
}

// 调用方持有 s.mu
func (s *nodeStore) save() error {
	b, _ := json.MarshalIndent(s.data, "", "  ")
	return os.WriteFile(s.path, b, 0o600)
}

func sha(tok string) string {
	h := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(h[:])
}

func (s *nodeStore) auth(tok string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := sha(tok)
	for _, t := range s.data.Tokens {
		if subtle.ConstantTimeCompare([]byte(t.Hash), []byte(h)) == 1 {
			return t.ID, true
		}
	}
	return "", false
}

func (s *nodeStore) create(name string) (nodeToken, string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 40 {
		return nodeToken{}, "", i18n.New("给节点起个名字（最多 40 个字），比如「办公室的 Mac mini」", "Name the node (at most 40 characters), e.g. \"Office Mac mini\"")
	}
	b := make([]byte, 24)
	rand.Read(b)
	tok := "annulo_node_" + hex.EncodeToString(b)
	id := make([]byte, 6)
	rand.Read(id)
	t := nodeToken{ID: "n" + hex.EncodeToString(id), Name: name, Hash: sha(tok), CreatedAt: time.Now().UTC().Format(time.RFC3339)}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Tokens = append(s.data.Tokens, t)
	return t, tok, s.save()
}

func (s *nodeStore) remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.data.Tokens, func(t nodeToken) bool { return t.ID == id })
	if i < 0 {
		return i18n.New("没有这个节点", "No such node")
	}
	s.data.Tokens = slices.Delete(s.data.Tokens, i, i+1)
	if s.data.Default == id {
		s.data.Default = ""
	}
	return s.save()
}

func (s *nodeStore) list() ([]nodeToken, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]nodeToken, 0, len(s.data.Tokens))
	for _, t := range s.data.Tokens {
		t.Hash = ""
		out = append(out, t)
	}
	return out, s.data.Default
}

func (s *nodeStore) setDefault(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Default = id
	return s.save()
}

// apiNodes：设置 → 节点（服务器上、管理员）。GET 列表；POST tokens {name} 生成令牌（只返回这一次）；
// DELETE tokens/<id> 删令牌（连着的断开）；PUT default {id} 默认用哪个。
func (s *Server) apiNodes(w http.ResponseWriter, r *http.Request, rest string) {
	if !s.cfg.Server {
		fail(w, http.StatusNotFound, i18n.New("节点只在服务端部署时用：本机版在 设置 → 节点 里连到服务器", "Nodes are for server deployments; on this computer, connect to a server in Settings → Nodes"))
		return
	}
	switch {
	case rest == "" && r.Method == http.MethodGet:
		tokens, def := s.nodeTokens.list()
		online := map[string]relay.Node{}
		for _, n := range s.nodeHub.Nodes() {
			online[n.ID] = n
		}
		list := []map[string]any{}
		for _, t := range tokens {
			item := map[string]any{"id": t.ID, "name": t.Name, "created_at": t.CreatedAt, "online": false, "default": t.ID == def}
			if n, ok := online[t.ID]; ok {
				item["online"], item["machine_name"], item["capabilities"], item["connected_at"] = true, n.MachineName, n.Capabilities, n.ConnectedAt
			}
			list = append(list, item)
		}
		writeJSON(w, map[string]any{"nodes": list, "server_url": s.publicBase(r)})
	case rest == "tokens" && r.Method == http.MethodPost:
		var in struct{ Name string }
		if err := readJSON(r, &in); err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		t, tok, err := s.nodeTokens.create(in.Name)
		if err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, map[string]any{"id": t.ID, "token": tok, "server_url": s.publicBase(r)})
	case strings.HasPrefix(rest, "tokens/") && r.Method == http.MethodDelete:
		id := strings.TrimPrefix(rest, "tokens/")
		if err := s.nodeTokens.remove(id); err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		s.nodeHub.Disconnect(id)
		writeJSON(w, map[string]any{"ok": true})
	case rest == "default" && r.Method == http.MethodPut:
		var in struct{ ID string }
		if err := readJSON(r, &in); err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		if err := s.nodeTokens.setDefault(in.ID); err != nil {
			fail(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	default:
		fail(w, http.StatusNotFound, i18n.Errorf("没有这个接口：nodes/%s", "No such endpoint: nodes/%s", rest))
	}
}

// ---- 服务器：远程浏览器 ----

var errNoBrowserNode = i18n.New("服务器上没有浏览器：在你电脑上的 Annulo 里打开 设置 → 节点，连到这台服务器（令牌在服务器的 设置 → 节点 里生成）",
	"There's no browser on the server: in Annulo on your computer, open Settings → Nodes and connect to this server (create a token in the server's Settings → Nodes)")

// remoteBrowser 是服务器上的 ctx.browser：每次打开、每个操作都转给一个节点。
type remoteBrowser struct {
	s       *Server
	project string
}

func (b remoteBrowser) node() (string, error) {
	_, def := b.s.nodeTokens.list()
	id, err := b.s.nodeHub.Pick("browser", def)
	if errors.Is(err, relay.ErrNoNode) {
		return "", errNoBrowserNode
	}
	return id, err
}

func (b remoteBrowser) call(ctx context.Context, node, op string, args any, out any) error {
	raw, err := b.s.nodeHub.Call(ctx, node, "browser", op, args)
	if errors.Is(err, relay.ErrNoNode) {
		return errNoBrowserNode
	}
	if err != nil {
		return err
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

func (b remoteBrowser) Open(ctx context.Context, o localfn.BrowserOptions) (localfn.BrowserPage, error) {
	node, err := b.node()
	if err != nil {
		return nil, err
	}
	var out struct{ Page string }
	if err := b.call(ctx, node, "open", map[string]any{"project": b.project, "options": o}, &out); err != nil {
		return nil, err
	}
	return &remotePage{b: b, node: node, id: out.Page}, nil
}

func (b remoteBrowser) ProfileID(profile string) (string, error) {
	node, err := b.node()
	if err != nil {
		return "", nil // 没有节点：当作没有这个 profile
	}
	var out struct{ ID string }
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err = b.call(ctx, node, "profile_id", map[string]any{"project": b.project, "profile": profile}, &out)
	return out.ID, err
}

// UserBrowsers：ctx.browser.user()，节点那台电脑上连着插件的 Chrome profile
func (b remoteBrowser) UserBrowsers() any {
	node, err := b.node()
	if err != nil {
		return []any{}
	}
	var out []any
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if b.call(ctx, node, "user", nil, &out) != nil || out == nil {
		return []any{}
	}
	return out
}

type remotePage struct {
	b    remoteBrowser
	node string
	id   string
}

func ms(d time.Duration) int64 { return d.Milliseconds() }

func (p *remotePage) do(ctx context.Context, op string, args map[string]any, out any) error {
	if args == nil {
		args = map[string]any{}
	}
	args["page"] = p.id
	return p.b.call(ctx, p.node, op, args, out)
}

func (p *remotePage) Goto(ctx context.Context, u, wait string, timeout time.Duration) error {
	return p.do(ctx, "goto", map[string]any{"url": u, "wait": wait, "timeout_ms": ms(timeout)}, nil)
}
func (p *remotePage) WaitFor(ctx context.Context, sel string, visible bool, timeout time.Duration) error {
	return p.do(ctx, "wait_for", map[string]any{"sel": sel, "visible": visible, "timeout_ms": ms(timeout)}, nil)
}
func (p *remotePage) Exists(ctx context.Context, sel string) (bool, error) {
	var ok bool
	err := p.do(ctx, "exists", map[string]any{"sel": sel}, &ok)
	return ok, err
}
func (p *remotePage) Click(ctx context.Context, sel, text string, timeout time.Duration) error {
	return p.do(ctx, "click", map[string]any{"sel": sel, "text": text, "timeout_ms": ms(timeout)}, nil)
}
func (p *remotePage) Type(ctx context.Context, sel, text string, clear bool, timeout time.Duration) error {
	return p.do(ctx, "type", map[string]any{"sel": sel, "text": text, "clear": clear, "timeout_ms": ms(timeout)}, nil)
}
func (p *remotePage) Press(ctx context.Context, key string) error {
	return p.do(ctx, "press", map[string]any{"key": key}, nil)
}

// Upload：文件在服务器上，内容一起发给节点，节点写到临时目录再上传（一次连接最多几十 MB，大视频传不了）
func (p *remotePage) Upload(ctx context.Context, sel string, files []string, timeout time.Duration) error {
	var list []map[string]string
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		if len(b) > 40<<20 {
			return i18n.Errorf("%s 太大（%d MB）：经节点上传最多 40 MB", "%s is too large (%d MB): uploads through a node are limited to 40 MB", filepath.Base(f), len(b)>>20)
		}
		list = append(list, map[string]string{"name": filepath.Base(f), "data": base64.StdEncoding.EncodeToString(b)})
	}
	return p.do(ctx, "upload", map[string]any{"sel": sel, "files": list, "timeout_ms": ms(timeout)}, nil)
}
func (p *remotePage) Eval(ctx context.Context, expr string) (any, error) {
	var v any
	err := p.do(ctx, "eval", map[string]any{"expr": expr}, &v)
	return v, err
}
func (p *remotePage) Text(ctx context.Context, sel string) (string, error) {
	var v string
	err := p.do(ctx, "text", map[string]any{"sel": sel}, &v)
	return v, err
}
func (p *remotePage) HTML(ctx context.Context, sel string) (string, error) {
	var v string
	err := p.do(ctx, "html", map[string]any{"sel": sel}, &v)
	return v, err
}
func (p *remotePage) URL(ctx context.Context) (string, error) {
	var v string
	err := p.do(ctx, "url", nil, &v)
	return v, err
}
func (p *remotePage) Listen(pattern string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	p.do(ctx, "listen", map[string]any{"pattern": pattern}, nil)
}
func (p *remotePage) Responses(ctx context.Context, pattern string, min int, timeout time.Duration) ([]map[string]any, error) {
	var v []map[string]any
	err := p.do(ctx, "responses", map[string]any{"pattern": pattern, "min": min, "timeout_ms": ms(timeout)}, &v)
	return v, err
}
func (p *remotePage) Screenshot(ctx context.Context, sel string, full bool) ([]byte, error) {
	var v []byte // 节点那边 []byte 编成 base64，这里解回来
	err := p.do(ctx, "screenshot", map[string]any{"sel": sel, "full": full}, &v)
	return v, err
}
func (p *remotePage) SetContent(ctx context.Context, html string) error {
	return p.do(ctx, "set_content", map[string]any{"html": html}, nil)
}
func (p *remotePage) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return p.do(ctx, "close", nil, nil)
}

// browserFor 是本机函数的 ctx.browser：服务器上转给节点，本机版用这台电脑的浏览器。
func (s *Server) browserFor(run *localRun, project string) localfn.BrowserHost {
	if s.cfg.Server {
		return remoteBrowser{s: s, project: project}
	}
	return browserHost{m: s.browser, run: run, project: project}
}

// ---- 电脑：作为节点连到服务器 ----

// NodeConfig 是本机版连服务器的设置（config.json 的 node）。
func (s *Server) newNodeClient() *relay.Client {
	m := machine(s.cfg.Dir)
	return relay.New(relay.Host{
		Name: "节点",
		URL: func() string {
			return nodeConnectURL(s.cfg.Node.URL)
		},
		Token:        func() string { return s.cfg.Node.Token },
		MachineID:    fmt.Sprint(m["id"]),
		MachineName:  fmt.Sprint(m["name"]),
		Enabled:      func() bool { return s.cfg.Node.Enabled && s.cfg.Node.URL != "" && s.cfg.Node.Token != "" },
		Capabilities: []string{"browser", "run"}, // run：服务器派函数来跑（noderun.go）
		Cap:          s.nodeCap,
		Closed:       s.closeNodePages,
	})
}

// nodeConnectURL：https://annulo.example.com → wss://annulo.example.com/_annulo/api/nodes/connect
func nodeConnectURL(server string) string {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(server), "/"))
	if err != nil || u.Host == "" {
		return ""
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	default:
		u.Scheme = "ws"
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/_annulo/api/nodes/connect"
	return u.String()
}

// 服务器经这台电脑打开的浏览器：页面 id → 页面
var nodePages = struct {
	sync.Mutex
	m   map[string]localfn.BrowserPage
	seq int
}{m: map[string]localfn.BrowserPage{}}

// nodeRuns：服务器开的浏览器（页面 id）→ 登记的本机运行 id
var nodeRuns sync.Map

// endNodeRun：服务器关了这个浏览器，右上角的那一项也去掉
func (s *Server) endNodeRun(page string) {
	v, ok := nodeRuns.LoadAndDelete(page)
	if !ok {
		return
	}
	s.local.mu.Lock()
	run := s.local.runs[v.(string)]
	delete(s.local.runs, v.(string))
	s.local.mu.Unlock()
	if run != nil {
		run.push(map[string]any{"type": "result"}, true)
	}
}

// closeNodePages：连接断了，服务器那边的函数已经失败：把它开的浏览器都关掉
func (s *Server) closeNodePages() {
	nodePages.Lock()
	pages := nodePages.m
	nodePages.m = map[string]localfn.BrowserPage{}
	nodePages.Unlock()
	for id, p := range pages {
		p.Close()
		s.endNodeRun(id)
	}
}

func dur(msv int64) time.Duration { return time.Duration(msv) * time.Millisecond }

// nodeCap 执行服务器派来的能力调用（现在只有 browser）。浏览器按服务器的项目分 profile（~/.annulo/browser/<项目>/<名字>），
// 用户自己的 Chrome（@user）照常经插件；没允许过的网站不弹窗问（没人在电脑前看着），直接报错。
func (s *Server) nodeCap(ctx context.Context, call *relay.Call) (any, error) {
	capability, op, raw := call.Cap, call.Op, call.Args
	if capability == "run" {
		return s.nodeRunFn(ctx, call)
	}
	if capability != "browser" {
		return nil, i18n.Errorf("这台电脑没有这个能力：%s", "This computer doesn't have this capability: %s", capability)
	}
	var in struct {
		Project string                 `json:"project"`
		Options localfn.BrowserOptions `json:"options"`
		Profile string                 `json:"profile"`
		Page    string                 `json:"page"`
		URL     string                 `json:"url"`
		Wait    string                 `json:"wait"`
		Sel     string                 `json:"sel"`
		Text    string                 `json:"text"`
		Key     string                 `json:"key"`
		Expr    string                 `json:"expr"`
		HTML    string                 `json:"html"`
		Pattern string                 `json:"pattern"`
		Visible bool                   `json:"visible"`
		Clear   bool                   `json:"clear"`
		Full    bool                   `json:"full"`
		Min     int                    `json:"min"`
		Timeout int64                  `json:"timeout_ms"`
		Files   []struct{ Name, Data string }
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
	}
	if in.Project != "" && !validProjectID(in.Project) {
		return nil, i18n.Errorf("项目 id 不对：%q", "Invalid project id: %q", in.Project)
	}
	host := browserHost{m: s.browser, project: in.Project}
	switch op {
	case "open":
		// 登记成本机的一次运行：右上角的浏览器列表里看得到（「服务器的浏览器」）、能打开看、能停
		run := &localRun{id: fmt.Sprintf("n%d", time.Now().UnixNano()), started: time.Now(), fn: i18n.T("服务器的浏览器", "Server's browser"), wake: make(chan struct{})}
		host.run = run
		p, err := host.Open(context.Background(), in.Options) // 浏览器跟着服务器的函数走，不跟这一次调用的 ctx
		if err != nil {
			return nil, err
		}
		nodePages.Lock()
		nodePages.seq++
		id := fmt.Sprintf("p%d", nodePages.seq)
		nodePages.m[id] = p
		nodePages.Unlock()
		run.cancel = func() { // 用户在右上角点停：关掉这个浏览器，服务器那边的下一个操作会报「已经关了」
			nodePages.Lock()
			delete(nodePages.m, id)
			nodePages.Unlock()
			p.Close()
		}
		s.local.mu.Lock()
		if s.local.runs == nil {
			s.local.runs = map[string]*localRun{}
		}
		s.local.runs[run.id] = run
		s.local.mu.Unlock()
		nodeRuns.Store(id, run.id)
		return map[string]string{"page": id}, nil
	case "profile_id":
		id, err := host.ProfileID(in.Profile)
		return map[string]string{"id": id}, err
	case "user":
		return host.UserBrowsers(), nil
	}
	nodePages.Lock()
	p := nodePages.m[in.Page]
	if op == "close" {
		delete(nodePages.m, in.Page)
	}
	nodePages.Unlock()
	if op == "close" {
		s.endNodeRun(in.Page)
	}
	if p == nil {
		return nil, i18n.New("这个浏览器已经关了", "This browser has already been closed")
	}
	switch op {
	case "goto":
		return nil, p.Goto(ctx, in.URL, in.Wait, dur(in.Timeout))
	case "wait_for":
		return nil, p.WaitFor(ctx, in.Sel, in.Visible, dur(in.Timeout))
	case "exists":
		return p.Exists(ctx, in.Sel)
	case "click":
		return nil, p.Click(ctx, in.Sel, in.Text, dur(in.Timeout))
	case "type":
		return nil, p.Type(ctx, in.Sel, in.Text, in.Clear, dur(in.Timeout))
	case "press":
		return nil, p.Press(ctx, in.Key)
	case "upload":
		dir, err := os.MkdirTemp("", "annulo-node-upload-")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(dir)
		var paths []string
		for _, f := range in.Files {
			b, err := base64.StdEncoding.DecodeString(f.Data)
			if err != nil {
				return nil, err
			}
			path := filepath.Join(dir, filepath.Base(f.Name))
			if err := os.WriteFile(path, b, 0o600); err != nil {
				return nil, err
			}
			paths = append(paths, path)
		}
		return nil, p.Upload(ctx, in.Sel, paths, dur(in.Timeout))
	case "eval":
		return p.Eval(ctx, in.Expr)
	case "text":
		return p.Text(ctx, in.Sel)
	case "html":
		return p.HTML(ctx, in.Sel)
	case "url":
		return p.URL(ctx)
	case "listen":
		p.Listen(in.Pattern)
		return nil, nil
	case "responses":
		return p.Responses(ctx, in.Pattern, in.Min, dur(in.Timeout))
	case "screenshot":
		return p.Screenshot(ctx, in.Sel, in.Full)
	case "set_content":
		return nil, p.SetContent(ctx, in.HTML)
	case "close":
		return nil, p.Close()
	}
	return nil, i18n.Errorf("不认识的浏览器操作：%s", "Unknown browser operation: %s", op)
}

// apiNodeSettings：本机版 设置 → 节点。GET 现在的设置和连接状态；PUT { url, token?, enabled } 保存（token 不给就留原来的）。
func (s *Server) apiNodeSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPut {
		var in struct {
			URL     string  `json:"url"`
			Token   *string `json:"token"`
			Enabled bool    `json:"enabled"`
		}
		if err := readJSON(r, &in); err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		in.URL = strings.TrimRight(strings.TrimSpace(in.URL), "/")
		if in.URL != "" && nodeConnectURL(in.URL) == "" {
			fail(w, http.StatusBadRequest, i18n.New("服务器地址不对，写成 https://annulo.example.com 这样", "Invalid server address; use something like https://annulo.example.com"))
			return
		}
		s.cfg.Node.URL, s.cfg.Node.Enabled = in.URL, in.Enabled
		if in.Token != nil {
			s.cfg.Node.Token = strings.TrimSpace(*in.Token)
		}
		if err := s.cfg.Save(); err != nil {
			fail(w, http.StatusInternalServerError, err)
			return
		}
		s.nodeClient.Reconnect()
		if in.Enabled { // 等一下连接结果，界面马上能显示连上没有
			for i := 0; i < 30; i++ {
				if online, _ := s.nodeClient.Status(); online {
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
		}
	}
	online, lastErr := s.nodeClient.Status()
	writeJSON(w, map[string]any{"url": s.cfg.Node.URL, "enabled": s.cfg.Node.Enabled, "has_token": s.cfg.Node.Token != "", "online": online, "error": lastErr})
}
