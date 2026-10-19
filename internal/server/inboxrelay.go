package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/annulo/annulo/internal/creght"
	"github.com/annulo/annulo/internal/i18n"
	"github.com/annulo/annulo/internal/inbox"
)

// 收件箱的云端代收（docs/inbox.md）：本机版的 /_inbox/ 地址外面到不了，连着 creght 时由 creght 替它收。
//   - 同步：项目里每个 webhook 来源在 creght 上注册一个公网地址（PUT inbox_relay/source，配置整份替换）；
//     配置没变不重复注册，项目里删了的来源在云端也删掉。注册过的记在 <数据目录>/inbox-relay.json。
//   - 收到时的通知由云端发（电脑关着也能发）：通知目标是密钥里存的机器人地址，同步时把地址本身带上去。
//   - 拉取：每 45 秒、或者平台经中转推「有新事件」时，拉下来 → 验签名、按来源的规则去重 → 存进本机事件表 → 回报云端 → 交给 handler。
//     签名在这里验（密钥不上云）；拉下来存好就回报，处理的成败在本机记、本机重试。
// 服务端部署不用代收：服务器自己有公网地址。

const relayPullEvery = 45 * time.Second

type relayRef struct {
	Project string `json:"project"`
	Inbox   string `json:"inbox"`
	Source  string `json:"source"`
	URL     string `json:"url"`
	Hash    string `json:"hash"` // 上次注册的配置
}

type relayState struct {
	mu     sync.Mutex
	loaded bool
	refs   map[string]relayRef // 项目/收件箱/来源 → 云端地址
	errs   map[string]error    // 项目 → 最近一次同步、拉取的错误（额度被拒是 *creght.QuotaError）
	pulls  map[string]*sync.Mutex
}

func relayKey(pid, ib, src string) string { return pid + "\x00" + ib + "\x00" + src }

func (s *Server) relayFile() string { return filepath.Join(s.cfg.Dir, "inbox-relay.json") }

// relayLoad：调用方持有 s.relay.mu
func (s *Server) relayLoad() {
	st := &s.inboxRelay
	if st.loaded {
		return
	}
	st.loaded, st.refs, st.errs, st.pulls = true, map[string]relayRef{}, map[string]error{}, map[string]*sync.Mutex{}
	var list []relayRef
	if b, err := os.ReadFile(s.relayFile()); err == nil && json.Unmarshal(b, &list) == nil {
		for _, r := range list {
			st.refs[relayKey(r.Project, r.Inbox, r.Source)] = r
		}
	}
}

func (s *Server) relaySave() {
	list := make([]relayRef, 0, len(s.inboxRelay.refs))
	for _, r := range s.inboxRelay.refs {
		list = append(list, r)
	}
	if b, err := json.MarshalIndent(list, "", "  "); err == nil {
		os.WriteFile(s.relayFile(), b, 0o600)
	}
}

func (s *Server) relaySetErr(pid string, err error) {
	st := &s.inboxRelay
	st.mu.Lock()
	defer st.mu.Unlock()
	s.relayLoad()
	if err == nil {
		delete(st.errs, pid)
	} else {
		st.errs[pid] = err
	}
}

// relayURL 是一个来源的云端地址（没注册过是空）。
func (s *Server) relayURL(pid, ib, src string) string {
	st := &s.inboxRelay
	st.mu.Lock()
	defer st.mu.Unlock()
	s.relayLoad()
	return st.refs[relayKey(pid, ib, src)].URL
}

func (s *Server) relayErr(pid string) error {
	st := &s.inboxRelay
	st.mu.Lock()
	defer st.mu.Unlock()
	s.relayLoad()
	return st.errs[pid]
}

// relayOn：这台电脑要不要走代收。
func (s *Server) relayOn(ctx context.Context) bool {
	return !s.cfg.Server && s.cloudAvailable(ctx)
}

// relaySourceIn 是一个来源要注册到云端的配置；通知里没配好密钥的跳过（本机管理页照样会标出来）。
func (s *Server) relaySourceIn(pid string, d inbox.Def, src inbox.SourceDef) creght.RelaySourceIn {
	in := creght.RelaySourceIn{Project: pid, Inbox: d.ID, Source: src.ID, Name: d.Name}
	if q := inbox.EchoQuery(src.Raw); q != "" {
		in.Verify = &struct {
			EchoQuery string `json:"echo_query,omitempty"`
		}{q}
	}
	for _, n := range d.Notify {
		to, ok := s.secrets.Get(strings.TrimPrefix(n.To, "secret:"))
		if !ok || strings.TrimSpace(to) == "" {
			continue
		}
		in.Notify = append(in.Notify, creght.RelayNotify{To: strings.TrimSpace(to), Text: n.Text, Body: n.Body})
	}
	return in
}

// syncRelay 把一个项目的 webhook 来源同步到云端：新的、改了的注册，删了的删掉。
func (s *Server) syncRelay(ctx context.Context, pid string, defs []inbox.Def) error {
	c := creght.NewClient(s.loginHost())
	want := map[string]bool{}
	var firstErr error
	for _, d := range defs {
		for _, src := range d.Sources {
			if _, ok := inbox.TypeOf(src.Type).(inbox.PushSource); !ok {
				continue
			}
			k := relayKey(pid, d.ID, src.ID)
			want[k] = true
			in := s.relaySourceIn(pid, d, src)
			b, _ := json.Marshal(in)
			h := sha256.Sum256(b)
			hash := hex.EncodeToString(h[:8])
			s.inboxRelay.mu.Lock()
			s.relayLoad()
			cur := s.inboxRelay.refs[k]
			s.inboxRelay.mu.Unlock()
			if cur.URL != "" && cur.Hash == hash {
				continue
			}
			out, err := c.PutRelaySource(ctx, in)
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			s.inboxRelay.mu.Lock()
			s.inboxRelay.refs[k] = relayRef{Project: pid, Inbox: d.ID, Source: src.ID, URL: out.URL, Hash: hash}
			s.relaySave()
			s.inboxRelay.mu.Unlock()
		}
	}
	// 项目里删了的来源：云端也删，地址作废
	s.inboxRelay.mu.Lock()
	var stale []relayRef
	for k, r := range s.inboxRelay.refs {
		if r.Project == pid && !want[k] {
			stale = append(stale, r)
		}
	}
	s.inboxRelay.mu.Unlock()
	for _, r := range stale {
		if err := c.DeleteRelaySource(ctx, r.Project, r.Inbox, r.Source); err != nil {
			var se *creght.StatusError
			if !errors.As(err, &se) || se.Status != 404 {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
		}
		s.inboxRelay.mu.Lock()
		delete(s.inboxRelay.refs, relayKey(r.Project, r.Inbox, r.Source))
		s.relaySave()
		s.inboxRelay.mu.Unlock()
	}
	return firstErr
}

func (s *Server) relayPullLock(pid string) *sync.Mutex {
	st := &s.inboxRelay
	st.mu.Lock()
	defer st.mu.Unlock()
	s.relayLoad()
	if st.pulls[pid] == nil {
		st.pulls[pid] = &sync.Mutex{}
	}
	return st.pulls[pid]
}

// pullRelay 把一个项目在云端排着的事件拉下来，存进本机、回报，再交给 handler。
func (s *Server) pullRelay(ctx context.Context, pid string, defs []inbox.Def) error {
	mu := s.relayPullLock(pid)
	mu.Lock()
	defer mu.Unlock()
	c := creght.NewClient(s.loginHost())
	store := s.inboxStore(pid)
	touched := map[string]bool{}
	defer func() {
		for _, d := range defs {
			if touched[d.ID] {
				go s.processInbox(pid, d)
			}
		}
	}()
	for range 20 { // 一次最多 2000 条，剩下的下一轮
		evs, more, err := c.PullRelayEvents(ctx, pid, 100)
		if err != nil {
			return err
		}
		if len(evs) == 0 {
			return nil
		}
		acks := make([]creght.RelayAck, 0, len(evs))
		for _, re := range evs {
			acks = append(acks, s.storeRelayed(pid, defs, re, store, touched))
		}
		if err := c.AckRelayEvents(ctx, acks); err != nil {
			return err
		}
		if !more {
			return nil
		}
	}
	return nil
}

// storeRelayed 存一条云端拉下来的，返回要回报的结果。存好了算成功（处理的成败本机记）；验不过、收件箱删了算失败。
func (s *Server) storeRelayed(pid string, defs []inbox.Def, re creght.RelayEvent, store inbox.Store, touched map[string]bool) creght.RelayAck {
	def := findInbox(defs, re.Inbox)
	if def == nil || def.Source(re.Source.ID) == nil {
		return creght.RelayAck{ID: re.ID, Error: "inbox or source removed"}
	}
	src := def.Source(re.Source.ID)
	key, err := inbox.CheckRelayed(src.Raw, re.Payload, s.inboxEnv())
	if err != nil {
		log.Printf("收件箱 %s/%s 云端代收的一条没通过：%v", pid, def.ID, err)
		return creght.RelayAck{ID: re.ID, Error: err.Error()}
	}
	if key == "" {
		key = re.DedupeKey
	}
	if key == "" {
		key = "relay:" + re.ID
	}
	ev := inbox.Event{Inbox: def.ID, Source: inbox.SourceRef{ID: src.ID, Type: src.Type}, ReceivedAt: re.ReceivedAt, DedupeKey: key,
		Payload: re.Payload, NotifyError: re.NotifyError, Via: "cloud"}
	if _, err := store.Put(&ev); err != nil {
		log.Printf("收件箱 %s/%s 存不下云端拉下来的一条：%v", pid, def.ID, err)
		return creght.RelayAck{ID: re.ID, Error: err.Error()}
	}
	touched[def.ID] = true
	return creght.RelayAck{ID: re.ID, OK: true}
}

// relayProject 同步一个项目的来源、拉一遍事件，结果记成这个项目的状态（管理页显示）。
func (s *Server) relayProject(ctx context.Context, pid, dir string) {
	defs, _ := parseInboxes(dir)
	hasPush := false
	for _, d := range defs {
		for _, src := range d.Sources {
			if _, ok := inbox.TypeOf(src.Type).(inbox.PushSource); ok {
				hasPush = true
			}
		}
	}
	if !hasPush && !s.relayHasRefs(pid) {
		return
	}
	err := s.syncRelay(ctx, pid, defs)
	if perr := s.pullRelay(ctx, pid, defs); err == nil {
		err = perr
	}
	if err != nil && !errors.Is(err, creght.ErrPreview) {
		log.Printf("收件箱 %s 云端代收：%v", pid, err)
	}
	s.relaySetErr(pid, err)
}

func (s *Server) relayHasRefs(pid string) bool {
	st := &s.inboxRelay
	st.mu.Lock()
	defer st.mu.Unlock()
	s.relayLoad()
	for _, r := range st.refs {
		if r.Project == pid {
			return true
		}
	}
	return false
}

// pullRelayProject：平台推了「有新事件」。
func (s *Server) pullRelayProject(pid string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if !s.relayOn(ctx) {
		return
	}
	if dir := s.inboxProjectDir(pid); dir != "" {
		s.relayProject(ctx, pid, dir)
	}
}

func (s *Server) runInboxRelay(ctx context.Context) {
	if s.cfg.Server {
		return
	}
	select {
	case <-time.After(40 * time.Second):
	case <-ctx.Done():
		return
	}
	t := time.NewTicker(relayPullEvery)
	defer t.Stop()
	for {
		func() {
			c, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()
			if !s.relayOn(c) {
				return
			}
			for _, sp := range s.scheduleProjects() {
				s.relayProject(c, sp.ID, sp.Dir)
			}
		}()
		select {
		case <-t.C:
		case <-ctx.Done():
			return
		}
	}
}

// relayInfo 是管理页上代收的状态：on（这台电脑在走代收）、error、quota（额度被拒时的详情，给额度提示）。
func (s *Server) relayInfo(ctx context.Context, pid, dir string) map[string]any {
	if !s.relayOn(ctx) {
		return map[string]any{"on": false}
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	defs, _ := parseInboxes(dir)
	if err := s.syncRelay(ctx, pid, defs); err != nil {
		s.relaySetErr(pid, err)
	}
	out := map[string]any{"on": true}
	if err := s.relayErr(pid); err != nil {
		var qe *creght.QuotaError
		if errors.As(err, &qe) {
			out["quota"] = quotaView(qe)
		} else {
			out["error"] = i18n.Errorf("云端代收出错了：%v", "Cloud relay error: %v", err).Error()
		}
	}
	return out
}

// quotaView 是给界面「额度提示」的。
func quotaView(e *creght.QuotaError) map[string]any {
	return map[string]any{"quota": e.Quota, "reason": e.Reason, "message": e.Error(), "subscribe_url": e.SubscribeURL}
}
