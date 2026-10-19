package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/annulo/annulo/internal/config"
	"github.com/annulo/annulo/internal/creght"
	"github.com/annulo/annulo/internal/inbox"
	"github.com/annulo/annulo/internal/localdb"
)

// 离线项目 B 声明一个收件箱：webhook 收进来 → 发收到时的通知 → handler 写进 B 的表；同样的请求再来一次不重复处理
func TestInboxWebhook(t *testing.T) {
	var notified atomic.Value
	bot := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var v map[string]any
		json.NewDecoder(r.Body).Decode(&v)
		notified.Store(v)
	}))
	defer bot.Close()

	root := t.TempDir()
	a := filepath.Join(root, "backends", "pa")
	writeProject(t, a, "pa/sa", "https://creght.cn", nil)
	b := filepath.Join(root, "backends", "local-b")
	for name, body := range map[string]string{
		"local/leads.ts":      "export function receive(ev: any, ctx: any) { if (ev.payload.json.fail) throw new Error('坏了'); ctx.db.insert('leads', { name: ev.payload.json.name, source: ev.source.id }); return { ok: true } }",
		"tables/leads.json":   `{"name":"询盘","desc":"","json_schema":{"type":"object"}}`,
		"inboxes/leads.json":  `{"name":"询盘","sources":[{"id":"form","type":"webhook"}],"handler":"leads.receive","notify":[{"when":"received","to":"secret:BOT","text":"新询盘：{{payload.json.name}}","body":{"msg_type":"text","content":{"text":"{{text}}"}}}]}`,
		"inboxes/broken.json": `{"sources":[{"id":"x","type":"mailbox"}],"handler":"a.b"}`,
	} {
		p := filepath.Join(b, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(body), 0o644)
	}
	creght.WriteOffline(b, creght.OfflineProject{ProjectID: "local-b", Name: "B 店"})

	cfg := &config.Config{Dir: root}
	s := newTestServer(cfg, &creght.Workspace{Dir: b, ProjectID: "local-b", Offline: true}, nil)
	s.ready.Store(true)
	s.secrets = cfg.LoadSecrets()
	s.secrets.Set("BOT", bot.URL)
	t.Cleanup(func() { localdb.Close(s.offlineDBPath("local-b")) })

	list := s.inboxList(context.Background())
	infos := list["list"].([]inboxInfo)
	if len(infos) != 1 || infos[0].ID != "leads" || len(infos[0].Sources) != 1 || !strings.HasPrefix(infos[0].Sources[0].Path, inboxURL) {
		t.Fatalf("收件箱列表不对：%+v", list)
	}
	if e, _ := list["error"].(string); !strings.Contains(e, "mailbox") {
		t.Fatalf("写错的收件箱要报出来：%q", e)
	}
	path := infos[0].Sources[0].Path

	post := func(body string) map[string]any {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		s.handleInbox(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("收不了：%d %s", w.Code, w.Body)
		}
		var v map[string]any
		json.Unmarshal(w.Body.Bytes(), &v)
		return v
	}
	first := post(`{"name":"Alice"}`)
	if first["duplicate"] != false {
		t.Fatalf("第一次不该算重复：%v", first)
	}
	if again := post(`{"name":"Alice"}`); again["duplicate"] != true {
		t.Fatalf("同样的请求要去重：%v", again)
	}

	store := s.inboxStore("local-b")
	var ev inbox.Event
	for i := 0; i < 100; i++ {
		ev, _ = store.Get("leads", first["id"].(string))
		if ev.Status == inbox.Done {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if ev.Status != inbox.Done || ev.Attempts != 1 {
		t.Fatalf("没处理完：%+v", ev)
	}
	got, _ := notified.Load().(map[string]any)
	if c, _ := got["content"].(map[string]any); c == nil || c["text"] != "新询盘：Alice" {
		t.Fatalf("通知不对：%v", got)
	}
	db, err := localdb.Open(s.offlineDBPath("local-b"))
	if err != nil {
		t.Fatal(err)
	}
	recs, _ := db.ListRecords(context.Background(), "", "leads", creght.RecordQuery{})
	if len(recs) != 1 || !strings.Contains(string(recs[0].Body), `"name":"Alice"`) {
		t.Fatalf("handler 没写表：%v", recs)
	}

	// handler 抛错：留着等重试，次数到了标成失败
	bad := post(`{"name":"Bob","fail":true}`)
	id := bad["id"].(string)
	for i := 0; i < 100; i++ {
		if ev, _ = store.Get("leads", id); ev.Attempts > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if ev.Status != inbox.Pending || !strings.Contains(ev.Error, "坏了") {
		t.Fatalf("第一次失败要留着重试：%+v", ev)
	}
	def := findInbox(mustInboxes(t, b), "leads")
	for i := 1; i < inbox.MaxAttempts; i++ {
		s.processInbox("local-b", *def)
	}
	if ev, _ = store.Get("leads", id); ev.Status != inbox.Failed || ev.Attempts != inbox.MaxAttempts {
		t.Fatalf("次数到了要标成失败：%+v", ev)
	}
	if infos := s.inboxList(context.Background())["list"].([]inboxInfo); infos[0].Failed != 1 || infos[0].Today != 2 {
		t.Fatalf("统计不对：%+v", infos[0])
	}

	// 地址不对
	w := httptest.NewRecorder()
	s.handleInbox(w, httptest.NewRequest(http.MethodPost, inboxURL+"nope", strings.NewReader("{}")))
	if w.Code != http.StatusNotFound {
		t.Fatalf("不认识的令牌要 404：%d", w.Code)
	}
	if !publicPath(path) {
		t.Fatal("收件箱地址要不用登录就能调")
	}
}

func mustInboxes(t *testing.T, dir string) []inbox.Def {
	t.Helper()
	defs, _ := parseInboxes(dir)
	return defs
}
