package server

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/annulo/annulo/internal/i18n"
	"github.com/annulo/annulo/internal/inbox"
	"github.com/annulo/annulo/internal/localfn"
	"github.com/annulo/annulo/internal/plugin"
)

// 收件箱（docs/inbox.md）：项目在 inboxes/<id>.json 里声明来源、处理函数和通知。
// 推来源（webhook）的地址是 /_inbox/<令牌>：令牌随机生成，记在 <数据目录>/inbox-tokens.json（不进项目的 git，
// 每台电脑、每个部署各是各的），一个令牌对应一个项目的一个收件箱的一个来源。不用登录，靠令牌（和签名）认。
// 收到的事件存在 <数据目录>/workspaces/<项目>/inbox/，先发收到时的通知，再交给 handler；失败的每分钟重试，最多 inbox.MaxAttempts 次。
// 本机版的地址只有这台电脑上的程序调得到，外面的要等云端代收（没做）或者用服务端部署。

const inboxURL = "/_inbox/"

// maxInboxBody：一个请求最大多少
const maxInboxBody = 5 << 20

type inboxState struct {
	mu     sync.Mutex
	tokens map[string]inboxTokenRef // 令牌 → 来源
	stores map[string]*inbox.FileStore
	procs  map[string]*sync.Mutex // 项目/收件箱：同一时间只有一个在处理
}

type inboxTokenRef struct {
	Project string `json:"project"`
	Inbox   string `json:"inbox"`
	Source  string `json:"source"`
}

func (s *Server) inboxTokensFile() string { return filepath.Join(s.cfg.Dir, "inbox-tokens.json") }

// loadInboxTokens：调用方持有 s.inbox.mu
func (s *Server) loadInboxTokens() {
	if s.inbox.tokens != nil {
		return
	}
	s.inbox.tokens = map[string]inboxTokenRef{}
	if b, err := os.ReadFile(s.inboxTokensFile()); err == nil {
		json.Unmarshal(b, &s.inbox.tokens)
	}
}

// inboxToken 是一个来源的令牌，没有就生成一个记下来。
func (s *Server) inboxToken(pid, ib, src string) string {
	st := &s.inbox
	st.mu.Lock()
	defer st.mu.Unlock()
	s.loadInboxTokens()
	want := inboxTokenRef{pid, ib, src}
	for tok, ref := range st.tokens {
		if ref == want {
			return tok
		}
	}
	b := make([]byte, 20)
	rand.Read(b)
	tok := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))
	st.tokens[tok] = want
	if b, err := json.MarshalIndent(st.tokens, "", "  "); err == nil {
		os.WriteFile(s.inboxTokensFile(), b, 0o600)
	}
	return tok
}

func (s *Server) inboxLookup(tok string) (inboxTokenRef, bool) {
	st := &s.inbox
	st.mu.Lock()
	defer st.mu.Unlock()
	s.loadInboxTokens()
	ref, ok := st.tokens[tok]
	return ref, ok
}

func (s *Server) inboxStore(pid string) *inbox.FileStore {
	st := &s.inbox
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.stores == nil {
		st.stores = map[string]*inbox.FileStore{}
	}
	if st.stores[pid] == nil {
		st.stores[pid] = inbox.NewFileStore(filepath.Join(s.cfg.Dir, "workspaces", pid, "inbox"))
	}
	return st.stores[pid]
}

func (s *Server) inboxProc(pid, ib string) *sync.Mutex {
	st := &s.inbox
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.procs == nil {
		st.procs = map[string]*sync.Mutex{}
	}
	k := pid + "\x00" + ib
	if st.procs[k] == nil {
		st.procs[k] = &sync.Mutex{}
	}
	return st.procs[k]
}

func (s *Server) inboxEnv() inbox.Env { return inbox.Env{Secret: s.secrets.Get} }

// parseInboxes 读 root/inboxes/*.json（和插件的），文件名是 id。
func parseInboxes(root string) ([]inbox.Def, error) {
	files, err := readJSONDir(root, inbox.Dir)
	if err != nil {
		return nil, err
	}
	defs := make([]inbox.Def, 0, len(files))
	var errs []error
	for _, f := range files {
		d, err := inbox.Parse(plugin.Name(f.Plugin, f.Key), f.Rel, f.Data)
		if err != nil {
			errs = append(errs, err) // 一个写错了不连累别的
			continue
		}
		defs = append(defs, d)
	}
	return defs, errors.Join(errs...)
}

func findInbox(defs []inbox.Def, id string) *inbox.Def {
	for i := range defs {
		if defs[i].ID == id {
			return &defs[i]
		}
	}
	return nil
}

// inboxProjectDir：项目在这台电脑 / 这个部署上的目录（定时任务能跑的那些项目）。
func (s *Server) inboxProjectDir(pid string) string {
	for _, sp := range s.scheduleProjects() {
		if sp.ID == pid {
			return sp.Dir
		}
	}
	return ""
}

func inboxFail(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("content-type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	b, _ := json.Marshal(map[string]any{"ok": false, "error": msg})
	w.Write(b)
}

// handleInbox：/_inbox/<令牌>，推来源（webhook）收请求。
func (s *Server) handleInbox(w http.ResponseWriter, r *http.Request) {
	tok := strings.Trim(strings.TrimPrefix(r.URL.Path, inboxURL), "/")
	ref, ok := s.inboxLookup(tok)
	if !ok {
		inboxFail(w, http.StatusNotFound, "unknown inbox")
		return
	}
	dir := s.inboxProjectDir(ref.Project)
	if dir == "" {
		inboxFail(w, http.StatusNotFound, "project not available")
		return
	}
	defs, _ := parseInboxes(dir)
	def := findInbox(defs, ref.Inbox)
	if def == nil || def.Source(ref.Source) == nil {
		inboxFail(w, http.StatusNotFound, "inbox source removed")
		return
	}
	src := def.Source(ref.Source)
	push, ok := inbox.TypeOf(src.Type).(inbox.PushSource)
	if !ok {
		inboxFail(w, http.StatusMethodNotAllowed, "this source doesn't receive requests")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxInboxBody+1))
	if err != nil {
		inboxFail(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(body) > maxInboxBody {
		inboxFail(w, http.StatusRequestEntityTooLarge, "body too large")
		return
	}
	ev, resp, err := push.Handle(r, body, src.Raw, s.inboxEnv())
	if errors.Is(err, inbox.ErrUnauthorized) {
		log.Printf("收件箱 %s/%s/%s：%v", ref.Project, ref.Inbox, ref.Source, err)
		inboxFail(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if err != nil {
		log.Printf("收件箱 %s/%s/%s 收不了：%v", ref.Project, ref.Inbox, ref.Source, err)
		inboxFail(w, http.StatusBadRequest, err.Error())
		return
	}
	if resp != nil {
		if resp.ContentType != "" {
			w.Header().Set("content-type", resp.ContentType)
		}
		w.WriteHeader(resp.Status)
		w.Write(resp.Body)
		return
	}
	ev.Inbox, ev.Source = def.ID, inbox.SourceRef{ID: src.ID, Type: src.Type}
	dup, err := s.inboxStore(ref.Project).Put(ev)
	if err != nil {
		log.Printf("收件箱 %s/%s 存不下：%v", ref.Project, def.ID, err)
		inboxFail(w, http.StatusInternalServerError, "store failed")
		return
	}
	writeJSON(w, map[string]any{"ok": true, "id": ev.ID, "duplicate": dup})
	if !dup {
		go s.inboxReceived(ref.Project, *def, *ev)
	}
}

// inboxReceived：新收到一条，先发收到时的通知，再交给 handler。
func (s *Server) inboxReceived(pid string, def inbox.Def, ev inbox.Event) {
	store := s.inboxStore(pid)
	var errs []string
	for _, n := range def.Notify {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		err := inbox.SendNotify(ctx, n, ev, s.inboxEnv())
		cancel()
		if err != nil {
			log.Printf("收件箱 %s/%s 的通知没发出去：%v", pid, def.ID, err)
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		if cur, err := store.Get(def.ID, ev.ID); err == nil {
			cur.NotifyError = strings.Join(errs, "\n")
			store.Update(cur)
		}
	}
	s.processInbox(pid, def)
}

// processInbox 把一个收件箱里等处理的逐条交给 handler。同一个收件箱同一时间只有一个在处理。
func (s *Server) processInbox(pid string, def inbox.Def) {
	mu := s.inboxProc(pid, def.ID)
	mu.Lock()
	defer mu.Unlock()
	store := s.inboxStore(pid)
	pending, err := store.Pending(def.ID, 50)
	if err != nil || len(pending) == 0 {
		return
	}
	host, err := s.projectHost(pid)
	if err != nil {
		log.Printf("收件箱 %s/%s：%v", pid, def.ID, err)
		return
	}
	for _, ev := range pending {
		start := time.Now()
		host.RunID = fmt.Sprintf("i%d", start.UnixNano())
		ctx, cancel := context.WithTimeout(context.Background(), localRunTimeout)
		_, err := s.runFn(ctx, host, def.Handler, inbox.EventData(ev), func(localfn.Event) {})
		cancel()
		ev.Attempts++
		if err == nil {
			now := time.Now()
			ev.Status, ev.Error, ev.HandledAt = inbox.Done, "", &now
		} else {
			ev.Error = err.Error()
			if ev.Attempts >= inbox.MaxAttempts {
				ev.Status = inbox.Failed
			}
			log.Printf("收件箱 %s/%s 处理 %s 失败（第 %d 次）：%v", pid, def.ID, ev.ID, ev.Attempts, err)
		}
		if err := store.Update(ev); err != nil {
			log.Printf("收件箱 %s/%s 记不下 %s 的结果：%v", pid, def.ID, ev.ID, err)
		}
	}
}

// runInboxes 每分钟把没处理完的重试一遍、清掉过期的。
func (s *Server) runInboxes(ctx context.Context) {
	select {
	case <-time.After(30 * time.Second): // 刚启动时等一会儿：MCP 在连、业务表在补
	case <-ctx.Done():
		return
	}
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		for _, sp := range s.scheduleProjects() {
			defs, _ := parseInboxes(sp.Dir)
			for _, d := range defs {
				s.processInbox(sp.ID, d)
				s.inboxStore(sp.ID).Prune(d.ID, d.Keep())
			}
		}
		select {
		case <-t.C:
		case <-ctx.Done():
			return
		}
	}
}

// ---- 管理页：local/inboxes ----

type inboxSourceInfo struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Mode string `json:"mode"`           // push | pull
	Path string `json:"path,omitempty"` // 推来源的地址（不带域名，界面按当前地址拼）
	// CloudURL：云端代收的公网地址（本机版连着 creght 时，inboxrelay.go）
	CloudURL string `json:"cloud_url,omitempty"`
	Auth     string `json:"auth,omitempty"`
}

type inboxInfo struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	Handler  string            `json:"handler"`
	Sources  []inboxSourceInfo `json:"sources"`
	Notify   int               `json:"notify"`
	KeepDays int               `json:"keep_days"`
	Today    int               `json:"today"`
	Pending  int               `json:"pending"`
	Failed   int               `json:"failed"`
	LastAt   *time.Time        `json:"last_at,omitempty"`
	// NotifyError：最近一条的通知没发出去
	NotifyError string `json:"notify_error,omitempty"`
}

func (s *Server) inboxList(ctx context.Context) map[string]any {
	pid, dir := s.ws.ProjectID, s.ws.Dir
	relay := s.relayInfo(ctx, pid, dir) // 先同步，下面才拿得到云端地址
	defs, err := parseInboxes(dir)
	store := s.inboxStore(pid)
	y, m, dd := time.Now().Date()
	day := time.Date(y, m, dd, 0, 0, 0, 0, time.Local)
	list := []inboxInfo{}
	for _, d := range defs {
		in := inboxInfo{ID: d.ID, Name: d.Name, Handler: d.Handler, Notify: len(d.Notify), KeepDays: int(d.Keep().Hours() / 24), Sources: []inboxSourceInfo{}}
		if in.Name == "" {
			in.Name = d.ID
		}
		for _, src := range d.Sources {
			si := inboxSourceInfo{ID: src.ID, Type: src.Type, Mode: "pull"}
			if _, ok := inbox.TypeOf(src.Type).(inbox.PushSource); ok {
				si.Mode, si.Path = "push", inboxURL+s.inboxToken(pid, d.ID, src.ID)
				si.CloudURL = s.relayURL(pid, d.ID, src.ID)
			}
			var a struct {
				Auth struct{ Kind string } `json:"auth"`
			}
			json.Unmarshal(src.Raw, &a)
			si.Auth = a.Auth.Kind
			in.Sources = append(in.Sources, si)
		}
		evs, _ := store.List(d.ID, 0)
		for i, e := range evs {
			if i == 0 {
				t := e.ReceivedAt
				in.LastAt, in.NotifyError = &t, e.NotifyError
			}
			if !e.ReceivedAt.Before(day) {
				in.Today++
			}
			switch e.Status {
			case inbox.Pending:
				in.Pending++
			case inbox.Failed:
				in.Failed++
			}
		}
		list = append(list, in)
	}
	return map[string]any{"list": list, "error": errString(err), "dir": inbox.Dir + "/", "server": s.cfg.Server, "types": inbox.TypeNames(), "relay": relay}
}

// GET  local/inboxes                               当前项目的收件箱、地址、今天收了几条、有几条失败
// GET  local/inboxes/<id>/events                   最近收到的（原文和处理结果），最多 50 条
// POST local/inboxes/<id>/events/<事件>/retry      重新处理一条
// POST local/inboxes/<id>/test  { source }         造一条测试事件，走和真的一样的通知和处理
func (s *Server) apiInboxes(w http.ResponseWriter, r *http.Request, rest string) {
	if !s.ready.Load() {
		fail(w, http.StatusConflict, i18n.New("还没有项目", "No project yet"))
		return
	}
	if rest == "" && r.Method == http.MethodGet {
		writeJSON(w, s.inboxList(r.Context()))
		return
	}
	pid, dir := s.ws.ProjectID, s.ws.Dir
	id, action := splitNameAction(dir, rest)
	defs, _ := parseInboxes(dir)
	def := findInbox(defs, id)
	if def == nil {
		fail(w, http.StatusNotFound, i18n.Errorf("没有这个收件箱：%s", "No such inbox: %s", id))
		return
	}
	store := s.inboxStore(pid)
	switch {
	case action == "events" && r.Method == http.MethodGet:
		evs, err := store.List(def.ID, 50)
		if err != nil {
			fail(w, http.StatusInternalServerError, err)
			return
		}
		if evs == nil {
			evs = []inbox.Event{}
		}
		writeJSON(w, map[string]any{"list": evs})
	case strings.HasPrefix(action, "events/") && strings.HasSuffix(action, "/retry") && r.Method == http.MethodPost:
		eid := strings.TrimSuffix(strings.TrimPrefix(action, "events/"), "/retry")
		ev, err := store.Get(def.ID, eid)
		if err != nil {
			fail(w, http.StatusNotFound, i18n.Errorf("没有这条：%s", "No such event: %s", eid))
			return
		}
		ev.Status, ev.Attempts, ev.Error = inbox.Pending, 0, ""
		if err := store.Update(ev); err != nil {
			fail(w, http.StatusInternalServerError, err)
			return
		}
		s.processInbox(pid, *def)
		ev, _ = store.Get(def.ID, eid)
		writeJSON(w, map[string]any{"event": ev})
	case action == "test" && r.Method == http.MethodPost:
		var in struct {
			Source string `json:"source"`
		}
		if err := readJSON(r, &in); err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		src := def.Source(in.Source)
		if src == nil && len(def.Sources) > 0 && in.Source == "" {
			src = &def.Sources[0]
		}
		if src == nil {
			fail(w, http.StatusNotFound, i18n.Errorf("没有这个来源：%s", "No such source: %s", in.Source))
			return
		}
		payload, err := inbox.TestEvent(src.Type)
		if err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		now := time.Now()
		ev := inbox.Event{Inbox: def.ID, Source: inbox.SourceRef{ID: src.ID, Type: src.Type}, ReceivedAt: now, DedupeKey: fmt.Sprintf("test:%d", now.UnixNano()), Payload: payload, Test: true}
		if _, err := store.Put(&ev); err != nil {
			fail(w, http.StatusInternalServerError, err)
			return
		}
		s.inboxReceived(pid, *def, ev) // 测试要等结果：同步跑完
		ev, _ = store.Get(def.ID, ev.ID)
		writeJSON(w, map[string]any{"event": ev})
	default:
		fail(w, http.StatusNotFound, i18n.Errorf("没有这个操作：%s", "No such action: %s", action))
	}
}
