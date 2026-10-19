package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/annulo/annulo/internal/config"
	"github.com/annulo/annulo/internal/creght"
	"github.com/annulo/annulo/internal/inbox"
	"github.com/annulo/annulo/internal/localdb"
)

// 本机版连着 creght：webhook 来源注册到云端（通知带上机器人地址本身）→ 拉下云端收的 → 本机验签名、存、回报 → handler 处理；
// 项目里删了来源，云端也删
func TestInboxRelay(t *testing.T) {
	sig := func(body string) string {
		m := hmac.New(sha256.New, []byte("k"))
		m.Write([]byte(body))
		return "sha256=" + hex.EncodeToString(m.Sum(nil))
	}
	good, bad := `{"name":"Alice"}`, `{"name":"Mallory"}`
	var mu sync.Mutex
	var put map[string]any
	var acks []map[string]any
	deleted := ""
	pulled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("content-type", "application/json")
		switch {
		case r.URL.Path == "/api/p/self":
			io.WriteString(w, `{"id":"u1","username":"m"}`)
		case r.URL.Path == "/api/u/annulo/entitlements":
			io.WriteString(w, `{"plan":{"id":11,"name":"Pro"},"status":"active","quotas":{}}`)
		case r.URL.Path == "/api/u/inbox_relay/source" && r.Method == http.MethodPut:
			json.NewDecoder(r.Body).Decode(&put)
			io.WriteString(w, `{"id":"1","url":"https://creght.example/api/inbox/TOKEN"}`)
		case r.URL.Path == "/api/u/inbox_relay/source" && r.Method == http.MethodDelete:
			deleted = r.URL.RawQuery
			io.WriteString(w, `{"deleted":1}`)
		case r.URL.Path == "/api/u/inbox_relay/events":
			if pulled {
				io.WriteString(w, `{"list":[],"more":false}`)
				return
			}
			pulled = true
			ev := func(id, body string) map[string]any {
				var j any
				json.Unmarshal([]byte(body), &j)
				return map[string]any{"id": id, "project": "local-r", "inbox": "leads", "source": map[string]string{"id": "form", "type": "webhook"},
					"received_at": time.Now(), "dedupe_key": "sha256:" + id,
					"payload": map[string]any{"method": "POST", "headers": map[string]string{"X-Sig": sig(good)}, "query": map[string]string{}, "body": body, "json": j}}
			}
			json.NewEncoder(w).Encode(map[string]any{"list": []any{ev("1", good), ev("2", bad)}, "more": false})
		case r.URL.Path == "/api/u/inbox_relay/events/ack":
			var in struct{ Results []map[string]any }
			json.NewDecoder(r.Body).Decode(&in)
			acks = append(acks, in.Results...)
			io.WriteString(w, `{"acked":2}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	root := t.TempDir()
	t.Setenv("ANNULO_DIR", root)
	creght.StoreToken(srv.URL, "tok", time.Now().Add(time.Hour))
	dir := filepath.Join(root, "backends", "local-r")
	for name, body := range map[string]string{
		"local/leads.ts":     "export function receive(ev: any, ctx: any) { ctx.db.insert('leads', { name: ev.payload.json.name }) }",
		"tables/leads.json":  `{"name":"询盘","desc":"","json_schema":{"type":"object"}}`,
		"inboxes/leads.json": `{"name":"询盘","sources":[{"id":"form","type":"webhook","auth":{"kind":"hmac","header":"X-Sig","prefix":"sha256=","secret":"secret:HOOK_KEY"}}],"handler":"leads.receive","notify":[{"when":"received","to":"secret:BOT","text":"新询盘：{{payload.json.name}}"}]}`,
	} {
		p := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(body), 0o644)
	}
	creght.WriteOffline(dir, creght.OfflineProject{ProjectID: "local-r", Name: "R"})

	cfg := &config.Config{Dir: root, Creght: srv.URL}
	s := newTestServer(cfg, &creght.Workspace{Dir: dir, ProjectID: "local-r", Offline: true, APIHost: srv.URL}, nil)
	s.ready.Store(true)
	s.secrets = cfg.LoadSecrets()
	s.secrets.Set("HOOK_KEY", "k")
	s.secrets.Set("BOT", "https://bot.example/hook")
	t.Cleanup(func() { localdb.Close(s.offlineDBPath("local-r")) })

	ctx := context.Background()
	if _, err := creght.ReadToken(srv.URL); err != nil {
		t.Fatalf("token: %v", err)
	}
	if !s.signedIn() {
		t.Fatal("not signed in")
	}
	if _, err := s.entitlements(ctx, true); err != nil {
		t.Fatalf("ent: %v", err)
	}
	list := s.inboxList(ctx)
	if r := list["relay"].(map[string]any); r["on"] != true || r["error"] != nil {
		t.Fatalf("连着 creght 要走代收：%v", r)
	}
	if u := list["list"].([]inboxInfo)[0].Sources[0].CloudURL; u != "https://creght.example/api/inbox/TOKEN" {
		t.Fatalf("要显示云端地址：%q", u)
	}
	mu.Lock()
	notify, _ := put["notify"].([]any)
	mu.Unlock()
	if len(notify) != 1 || notify[0].(map[string]any)["to"] != "https://bot.example/hook" || put["project"] != "local-r" {
		t.Fatalf("注册的配置不对：%v", put)
	}

	s.relayProject(ctx, "local-r", dir)
	mu.Lock()
	gotAcks := acks
	mu.Unlock()
	if len(gotAcks) != 2 || gotAcks[0]["ok"] != true || gotAcks[1]["ok"] == true {
		t.Fatalf("签名对的回报成功、不对的回报失败：%v", gotAcks)
	}
	store := s.inboxStore("local-r")
	var evs []inbox.Event
	for range 100 {
		evs, _ = store.List("leads", 0)
		if len(evs) == 1 && evs[0].Status == inbox.Done {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(evs) != 1 || evs[0].Via != "cloud" || evs[0].Status != inbox.Done {
		t.Fatalf("只存签名对的那条、处理完：%+v", evs)
	}

	os.Remove(filepath.Join(dir, "inboxes", "leads.json"))
	s.relayProject(ctx, "local-r", dir)
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(deleted, "source=form") {
		t.Fatalf("项目里删了的来源云端也要删：%q", deleted)
	}
}
