package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/annulo/annulo/internal/i18n"

	"github.com/annulo/annulo/internal/agent"
	"github.com/annulo/annulo/internal/creght"
	"github.com/annulo/annulo/internal/localfn"
	"github.com/annulo/annulo/internal/plugin"
	"github.com/annulo/annulo/internal/version"
)

// 本机函数（运营后台项目 local/*.ts，见 internal/localfn）：
//
//	GET  local/functions             列出能调的函数
//	POST local/run {fn, input}       执行，SSE 推 progress / log，最后 result 或 error
//	POST local/runs/<id>/abort       停止
//
// 执行和浏览器连接无关：关掉、刷新页面都不会停，要停走 abort。

// 一次最多跑多久：社媒发视频（下载几个 GB、传给平台、等平台处理）要几十分钟
const localRunTimeout = 60 * time.Minute

type localRun struct {
	id     string
	fn     string
	cancel context.CancelFunc

	mu     sync.Mutex
	events []map[string]any
	done   bool
	wake   chan struct{} // 有新事件就关掉换一个，等着的连接醒来
}

func (r *localRun) push(ev map[string]any, final bool) {
	r.mu.Lock()
	r.events = append(r.events, ev)
	r.done = r.done || final
	close(r.wake)
	r.wake = make(chan struct{})
	r.mu.Unlock()
}

type localRuns struct {
	mu   sync.Mutex
	runs map[string]*localRun
}

func (s *Server) localHost() localfn.Host {
	return localfn.Host{
		LogDir:        filepath.Join(s.cfg.Dir, "logs", "local", s.ws.ProjectID),
		WorkDir:       s.ws.Dir,
		Workspace:     workspaceInfo(map[string]any{"project_id": s.ws.ProjectID, "site_id": s.ws.SiteID, "api_host": s.ws.APIHost, "shuttle_projects": s.shuttleProjects(), "shuttle_api": version.API, "logs_dir": filepath.Join(s.cfg.Dir, "logs", "local", s.ws.ProjectID), "machine": machine(s.cfg.Dir), "offline": s.ws.Offline}),
		DB:            localDB{s.cur()},
		Secret:        s.secrets.Get,
		Fetch:         s.fetchWithAssets,
		Download:      s.downloadWithAssets,
		LLM:           s.agent.Complete,
		LLMProviders:  s.llmProviders,
		LLMFetch:      s.llmFetch,
		MCP:           s.mcp.Call,
		MCPServers:    s.mcpServers,
		AgentCurrent:  s.agentCurrent,
		OAuth:         s.conns.Token,
		OAuthAccounts: s.conns.Accounts,
		Browser:       browserHost{s.browser},
		// 按项目分开：修复时助手只看这个项目的
		SnapshotDir: filepath.Join(s.cfg.Dir, "snapshots", s.ws.ProjectID),
		FilesDir:    s.localFilesDir(), // b.upload 的 local:<name>（localfiles.go）
	}
}

// agentCurrent 是 ctx.agent.current()：复用助手自己的判断（agent.LLM），和点了任务后实际跑的是同一个模型
func (s *Server) agentCurrent() localfn.AgentModel {
	m, err := s.agent.LLM()
	out := localfn.AgentModel{ID: m.ID, Name: m.Label(), Provider: m.Provider, Model: m.Model, Ready: err == nil}
	if err != nil {
		out.Error = err.Error()
	}
	return out
}

// mcpServers 是 ctx.mcp.servers()：只给名字和状态，授权地址、工具列表不给本机函数
func (s *Server) mcpServers() []localfn.MCPServer {
	out := []localfn.MCPServer{}
	for _, st := range s.mcp.Status() {
		out = append(out, localfn.MCPServer{Name: st.Name, Status: st.Status})
	}
	return out
}

var secretUseRe = regexp.MustCompile(`secrets\.get\(\s*['"]([A-Z][A-Z0-9_]+)['"]`)

// secretNamesInUse 是本机函数里用到的密钥名（扫 local/ 和插件 local/ 的源码），设置页据此提示「还缺哪个」。
func (s *Server) secretNamesInUse() []string {
	out := []string{} // 没有也是空数组：设置 → 密钥直接 .filter
	if !s.ready.Load() {
		return out
	}
	seen := map[string]bool{}
	for _, id := range plugin.Sources(s.ws.Dir) {
		dir := filepath.Join(plugin.Root(s.ws.Dir, id), localfn.Dir)
		ents, _ := os.ReadDir(dir)
		for _, e := range ents {
			b, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				continue
			}
			for _, m := range secretUseRe.FindAllStringSubmatch(string(b), -1) {
				if !seen[m[1]] {
					seen[m[1]] = true
					out = append(out, m[1])
				}
			}
		}
	}
	return out
}

func (s *Server) apiLocalFunctions(w http.ResponseWriter, r *http.Request) {
	fns, err := localfn.List(s.ws.Dir)
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]any{"list": fns})
}

func (s *Server) apiLocalRun(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Fn     string `json:"fn"`
		Input  any    `json:"input"`
		ChatID string `json:"chat_id"` // 助手在对话里用 annulo run 跑的：哪段对话（ctx.chat_id）
	}
	b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err := json.Unmarshal(b, &in); err != nil || in.Fn == "" {
		fail(w, http.StatusBadRequest, i18n.New("要给出 fn（比如 geo.check）和 input", "fn (e.g. geo.check) and input are required"))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), localRunTimeout)
	run := &localRun{id: fmt.Sprintf("r%d", time.Now().UnixNano()), fn: in.Fn, cancel: cancel, wake: make(chan struct{})}
	s.local.mu.Lock()
	if s.local.runs == nil {
		s.local.runs = map[string]*localRun{}
	}
	s.local.runs[run.id] = run
	s.local.mu.Unlock()

	host := s.localHost()
	host.RunID = run.id
	if agent.ValidChatID(in.ChatID) {
		host.ChatID = in.ChatID
	}
	go func() {
		defer cancel()
		start := time.Now()
		res, err := localfn.Run(ctx, host, in.Fn, in.Input, func(e localfn.Event) {
			run.push(map[string]any{"type": e.Type, "data": e.Data}, false)
		})
		ms := time.Since(start).Milliseconds()
		if err != nil {
			data := map[string]any{"message": err.Error(), "ms": ms}
			var fe *localfn.Error
			if errors.As(err, &fe) && fe.Stack != "" {
				data["stack"] = fe.Stack
			}
			log.Printf("本机函数 %s 失败（%dms）：%v", in.Fn, ms, err)
			run.push(map[string]any{"type": "error", "data": data}, true)
		} else {
			log.Printf("本机函数 %s 完成（%dms）", in.Fn, ms)
			run.push(map[string]any{"type": "result", "data": map[string]any{"value": res, "ms": ms}}, true)
		}
		// 跑完留一会儿，断线的页面还能取到结果
		time.AfterFunc(10*time.Minute, func() {
			s.local.mu.Lock()
			delete(s.local.runs, run.id)
			s.local.mu.Unlock()
		})
	}()
	s.streamLocalRun(w, r, run)
}

// streamLocalRun 从头推这次运行的事件（SSE），直到结束或者浏览器断开。
func (s *Server) streamLocalRun(w http.ResponseWriter, r *http.Request, run *localRun) {
	flusher, _ := w.(http.Flusher)
	w.Header().Set("content-type", "text/event-stream")
	w.Header().Set("cache-control", "no-cache")
	w.Header().Set("x-shuttle-run", run.id)
	w.Header().Set("x-annulo-run", run.id)
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(fmt.Sprintf("data: %s\n\n", mustJSON(map[string]any{"type": "start", "data": map[string]any{"id": run.id, "fn": run.fn}}))))
	sent := 0
	for {
		run.mu.Lock()
		evs, done, wake := run.events[sent:], run.done, run.wake
		run.mu.Unlock()
		for _, ev := range evs {
			w.Write([]byte("data: " + mustJSON(ev) + "\n\n"))
		}
		sent += len(evs)
		if flusher != nil {
			flusher.Flush()
		}
		if done {
			return
		}
		select {
		case <-wake:
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) apiLocalAbort(w http.ResponseWriter, id string) {
	s.local.mu.Lock()
	run := s.local.runs[id]
	s.local.mu.Unlock()
	if run == nil {
		fail(w, http.StatusNotFound, i18n.New("没有这次运行（可能早就结束了）", "No such run (it may have finished long ago)"))
		return
	}
	run.cancel()
	writeJSON(w, map[string]any{"ok": true})
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		b, _ = json.Marshal(map[string]any{"type": "error", "data": map[string]any{"message": i18n.T("结果不能转成 JSON：", "The result can't be converted to JSON: ") + err.Error()}})
	}
	return string(b)
}

// localDB 是本机函数的 ctx.db：只能碰业务表，语义和 creght Func 的 ctx.db 一样。
type localDB struct{ p *projectDB }

func (d localDB) check(table string) error {
	if !d.p.isTable(table) {
		return d.p.tableErr(i18n.Errorf("不支持的表：%s（运营后台的业务表才能读写；新表先在 %s/<表>.json 里声明）", "Unsupported table: %s (only the back office's data tables can be read or written; declare new tables in %s/<table>.json first)", table, WorkspaceTablesDir))
	}
	return nil
}

// same 比较字段值：JSON 里数字都是 float64，函数传进来的可能是 int64，按 JSON 形式比。
func same(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	if string(x) == string(y) {
		return true
	}
	return false
}

func (d localDB) Query(ctx context.Context, table string, q localfn.Query) ([]map[string]any, string, int64, error) {
	if err := d.check(table); err != nil {
		return nil, "", 0, err
	}
	if id, found, conflict, err := queryRecordID(q); err != nil {
		return nil, "", 0, err
	} else if found {
		out := []map[string]any{}
		if conflict || id == "" || q.Cursor != nil && id <= *q.Cursor {
			return out, "", 0, nil
		}
		r, err := d.Get(ctx, table, id)
		if err != nil {
			return nil, "", 0, err
		}
		if r == nil || !matchesQuery(r, q) {
			return out, "", 0, nil
		}
		if q.Offset == 0 {
			out = append(out, r)
		}
		return out, "", 1, nil
	}
	rq := creght.RecordQuery{Where: q.Where, Filter: recordFilter(q.Filter), OrderBy: bodyOrder(q.OrderBy), Limit: q.Limit, Offset: q.Offset}
	if rq.Limit <= 0 {
		rq.Limit = 1000
	}
	if q.Cursor != nil {
		rq.Cursor, rq.OrderBy = *q.Cursor, "id asc"
	}
	rows, next, total, err := d.p.queryPage(ctx, table, rq)
	if err != nil {
		return nil, "", 0, err
	}
	out := make([]map[string]any, len(rows))
	for i, r := range rows {
		out[i] = r
	}
	return out, next, total, nil
}

func (d localDB) Aggregate(ctx context.Context, table string, req map[string]any, filter []localfn.Cond) ([]map[string]any, bool, error) {
	if err := d.check(table); err != nil {
		return nil, false, err
	}
	tid, err := d.p.tableID(ctx, table)
	if err != nil {
		return nil, false, err
	}
	if f := recordFilter(filter); f != nil {
		req["filter"] = f
	}
	return d.p.c.AggregateRecords(ctx, d.p.projectID, tid, req)
}

// recordFilter 把本机函数的条件换成平台的格式（field → fieldId，op → operator）。
func recordFilter(cs []localfn.Cond) *creght.RecordFilter {
	if len(cs) == 0 {
		return nil
	}
	f := &creght.RecordFilter{}
	for _, c := range cs {
		f.Conditions = append(f.Conditions, creght.RecordCondition{FieldID: c.Field, Operator: c.Op, Value: c.Value})
	}
	return f
}

// bodyOrder：本机函数写 "date desc"，平台要 "body.date desc"；id、created_at、updated_at 是系统列，原样。
func bodyOrder(order string) string {
	f := strings.Fields(order)
	if len(f) == 0 {
		return ""
	}
	switch k := f[0]; {
	case k == "id" || k == "created_at" || k == "updated_at" || strings.HasPrefix(k, "body."):
	default:
		f[0] = "body." + k
	}
	return strings.Join(f, " ")
}

func (d localDB) Get(ctx context.Context, table, id string) (map[string]any, error) {
	if err := d.check(table); err != nil {
		return nil, err
	}
	if id == "" {
		return nil, nil
	}
	tid, err := d.p.tableID(ctx, table)
	if err != nil {
		return nil, err
	}
	rec, err := d.p.c.GetRecord(ctx, d.p.projectID, tid, id)
	if err != nil {
		// 不存在返回 nil（和以前一样），别的错误照常抛
		if e := strings.ToLower(err.Error()); strings.Contains(e, "not found") || strings.Contains(e, "不存在") || strings.Contains(e, "404") {
			return nil, nil
		}
		return nil, err
	}
	if rec == nil || rec.ID == "" { // 不存在时平台返回空记录，不报错
		return nil, nil
	}
	rows := rowsOf([]creght.Record{*rec})
	if len(rows) == 0 {
		return nil, nil
	}
	return rows[0], nil
}

func (d localDB) Insert(ctx context.Context, table string, data map[string]any) (map[string]any, error) {
	if err := d.check(table); err != nil {
		return nil, err
	}
	return d.p.create(ctx, table, data)
}

func (d localDB) Update(ctx context.Context, table, id string, data map[string]any) (map[string]any, error) {
	if err := d.check(table); err != nil {
		return nil, err
	}
	return d.p.patch(ctx, table, id, data)
}

func (d localDB) Delete(ctx context.Context, table, id string) error {
	if err := d.check(table); err != nil {
		return err
	}
	return d.p.delete(ctx, table, id)
}

// fetchWithAssets / downloadWithAssets：本机存的上传文件（离线项目，offline.go）直接读文件，其余照出网规则走 localFetch / localDownload。
func (s *Server) fetchWithAssets(ctx context.Context, r localfn.FetchRequest) (*localfn.FetchResponse, error) {
	if p := s.localAssetFile(r.URL); p != "" && (r.Method == "" || r.Method == http.MethodGet) {
		b, err := os.ReadFile(p)
		if err != nil {
			return &localfn.FetchResponse{Status: http.StatusNotFound, StatusText: "Not Found", URL: r.URL, Headers: map[string]string{}}, nil
		}
		ct := mime.TypeByExtension(filepath.Ext(p))
		if ct == "" {
			ct = http.DetectContentType(b)
		}
		return &localfn.FetchResponse{Status: 200, StatusText: "OK", URL: r.URL, Headers: map[string]string{"content-type": ct}, Body: b}, nil
	}
	return localFetch(ctx, r)
}

func (s *Server) downloadWithAssets(ctx context.Context, rawURL, dst string) (string, error) {
	if p := s.localAssetFile(rawURL); p != "" {
		b, err := os.ReadFile(p)
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(dst, b, 0o600); err != nil {
			return "", err
		}
		return mime.TypeByExtension(filepath.Ext(p)), nil
	}
	return localDownload(ctx, rawURL, dst)
}

// localFetch 是本机函数的 fetch：和页面的 shuttleFetch 同一套出网规则（不许访问本机、内网），
// 单次 90 秒、响应最多 10MB。
func localFetch(ctx context.Context, r localfn.FetchRequest) (*localfn.FetchResponse, error) {
	u, err := url.Parse(r.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, i18n.Errorf("只能请求 http / https 地址：%q", "Only http / https URLs can be requested: %q", r.URL)
	}
	if err := checkTarget(u.Hostname()); err != nil {
		return nil, err
	}
	if r.Stream {
		return localFetchStream(ctx, r)
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	var body io.Reader
	if r.Body != "" {
		body = strings.NewReader(r.Body)
	}
	req, err := http.NewRequestWithContext(ctx, r.Method, r.URL, body)
	if err != nil {
		return nil, err
	}
	for k, v := range r.Headers {
		req.Header.Set(k, v)
	}
	if req.Header.Get("user-agent") == "" {
		req.Header.Set("user-agent", "Shuttle-local-function")
	}
	start := time.Now()
	var ttfb time.Duration
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{GotFirstResponseByte: func() { ttfb = time.Since(start) }}))
	resp, err := fetchClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	// 读到一半超时（慢站点）不算失败：已经读到的照样返回，标 truncated
	b, rerr := io.ReadAll(io.LimitReader(resp.Body, 10<<20+1))
	truncated := rerr != nil || len(b) > 10<<20
	if len(b) > 10<<20 {
		b = b[:10<<20]
	}
	h := map[string]string{}
	for k := range resp.Header {
		h[strings.ToLower(k)] = resp.Header.Get(k)
	}
	return &localfn.FetchResponse{Status: resp.StatusCode, StatusText: http.StatusText(resp.StatusCode), URL: resp.Request.URL.String(), Headers: h, Body: b,
		TTFBMs: ttfb.Milliseconds(), TotalMs: time.Since(start).Milliseconds(), Truncated: truncated}, nil
}

// 流式 fetch 的超时：等响应头最多 fetchHeaderWait，之后连续 fetchIdleWait 没收到数据才断。
// 不限总时长（整次运行有 localRunTimeout）：联网搜索这类 SSE 接口要一两分钟才说完，但一直在发数据。
var (
	fetchHeaderWait = 2 * time.Minute
	fetchIdleWait   = time.Minute
)

// localFetchStream 是带 Stream 的 localFetch：收到响应头就返回，响应体交给函数边读边用。
func localFetchStream(ctx context.Context, r localfn.FetchRequest) (*localfn.FetchResponse, error) {
	ctx, cancel := context.WithCancel(ctx)
	idle := &idleBody{d: fetchIdleWait, cancel: cancel}
	idle.timer = time.AfterFunc(fetchHeaderWait, idle.expire)
	var body io.Reader
	if r.Body != "" {
		body = strings.NewReader(r.Body)
	}
	req, err := http.NewRequestWithContext(ctx, r.Method, r.URL, body)
	if err != nil {
		idle.Close()
		return nil, err
	}
	for k, v := range r.Headers {
		req.Header.Set(k, v)
	}
	if req.Header.Get("user-agent") == "" {
		req.Header.Set("user-agent", "Shuttle-local-function")
	}
	start := time.Now()
	resp, err := newFetchClient(0).Do(req)
	if err != nil {
		idle.Close()
		if idle.expired.Load() {
			return nil, i18n.Errorf("等了 %d 秒没有响应", "No response after %d seconds", int(fetchHeaderWait.Seconds()))
		}
		return nil, err
	}
	ttfb := time.Since(start)
	idle.rc = resp.Body
	idle.timer.Reset(fetchIdleWait)
	h := map[string]string{}
	for k := range resp.Header {
		h[strings.ToLower(k)] = resp.Header.Get(k)
	}
	return &localfn.FetchResponse{Status: resp.StatusCode, StatusText: http.StatusText(resp.StatusCode), URL: resp.Request.URL.String(), Headers: h,
		TTFBMs: ttfb.Milliseconds(), Stream: idle}, nil
}

// idleBody 是流式响应体：每读到数据就重新计时，连续 d 没数据就断开请求。
type idleBody struct {
	rc      io.ReadCloser
	d       time.Duration
	timer   *time.Timer
	cancel  context.CancelFunc
	expired atomic.Bool
}

func (b *idleBody) expire() {
	b.expired.Store(true)
	b.cancel()
}

func (b *idleBody) Read(p []byte) (int, error) {
	n, err := b.rc.Read(p)
	if n > 0 {
		b.timer.Reset(b.d)
	}
	if err != nil && err != io.EOF && b.expired.Load() {
		err = i18n.Errorf("连续 %d 秒没收到数据，断开了", "No data for %d seconds, gave up", int(b.d.Seconds()))
	}
	return n, err
}

func (b *idleBody) Close() error {
	b.timer.Stop()
	b.cancel()
	if b.rc != nil {
		return b.rc.Close()
	}
	return nil
}

// maxDownload 是 b.upload 下载一个网址的上限（视频）
const maxDownload = 2 << 30

// localDownload 把网址流式存进 dst：出网规则和 localFetch 一样，最多 2GB、30 分钟。
func localDownload(ctx context.Context, rawURL, dst string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", i18n.Errorf("只能下载 http / https 地址：%q", "Only http / https URLs can be downloaded: %q", rawURL)
	}
	if err := checkTarget(u.Hostname()); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("user-agent", "Shuttle-local-function")
	resp, err := fetchClient().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", i18n.Errorf("返回 %d", "Got %d", resp.StatusCode)
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	n, err := io.Copy(f, io.LimitReader(resp.Body, maxDownload+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	if n > maxDownload {
		return "", i18n.New("文件超过 2GB", "The file is larger than 2GB")
	}
	return resp.Header.Get("content-type"), nil
}

var shuttleProjectsCache struct {
	sync.Mutex
	ids []string
	at  time.Time
}

// shuttleProjects 是账号下所有 Shuttle 项目（运营后台）的 creght 项目 id，缓存 5 分钟。
// workspaceInfo 是本机函数的 ctx.workspace：shuttle_projects / shuttle_api 同时用新名字 annulo_projects / annulo_api 给一份（internal/brand）。
func workspaceInfo(m map[string]any) map[string]any {
	m["annulo_projects"], m["annulo_api"] = m["shuttle_projects"], m["shuttle_api"]
	return m
}

// 给本机函数 ctx.workspace.shuttle_projects：列站点、加渠道时把它们排除掉（运营后台不是要运营的网站）。
func (s *Server) shuttleProjects() []string {
	c := &shuttleProjectsCache
	c.Lock()
	defer c.Unlock()
	if time.Since(c.at) < 5*time.Minute && c.ids != nil {
		return c.ids
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	list, err := s.listBackends(ctx)
	if err != nil {
		return c.ids // 查不到就用上一次的（可能是空）
	}
	ids := make([]string, 0, len(list))
	for _, b := range list {
		ids = append(ids, b.ProjectID)
	}
	c.ids, c.at = ids, time.Now()
	return ids
}
