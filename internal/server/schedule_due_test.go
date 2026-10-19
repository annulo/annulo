package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/annulo/annulo/internal/config"
	"github.com/annulo/annulo/internal/creght"
	"github.com/annulo/annulo/internal/localdb"
	"github.com/annulo/annulo/internal/localfn"
)

func TestParseDueSchedules(t *testing.T) {
	root := t.TempDir()
	writeSchedules(t, root, map[string]string{"publish": `{"fn":"p.go","input":{"post_id":"$id"},"due":{"table":"posts","field":"at","where":{"status":"scheduled"},"filter":[{"field":"kind","op":"neq","value":"x"}]}}`})
	defs, err := parseSchedules(root)
	if err != nil || len(defs) != 1 || defs[0].Due == nil || len(defs[0].Due.filter) != 1 {
		t.Fatalf("defs=%+v err=%v", defs, err)
	}
	for name, bad := range map[string]string{
		"a": `{"fn":"p.go","due":{"table":"posts"}}`,
		"b": `{"prompt":"发","due":{"table":"posts","field":"at"}}`,
		"c": `{"fn":"p.go","every":"1h","due":{"table":"posts","field":"at"}}`,
		"d": `{"fn":"p.go","due":{"table":"posts","field":"at","filter":[{"field":"a","op":"like"}]}}`,
	} {
		writeSchedules(t, root, map[string]string{name: bad})
		if _, err := parseSchedules(root); err == nil {
			t.Errorf("%s 应该报错", bad)
		}
	}
}

func TestDueInput(t *testing.T) {
	row := map[string]any{"id": "r1", "article_id": "a1"}
	got, _ := json.Marshal(dueInput(map[string]any{"post_id": "$id", "x": []any{"$row.article_id", "k"}}, row))
	if string(got) != `{"post_id":"r1","x":["a1","k"]}` {
		t.Fatal(string(got))
	}
	if got, _ := json.Marshal(dueInput(nil, row)); string(got) != `{"id":"r1"}` {
		t.Fatal(string(got))
	}
}

// 到点的行一条条跑；没到点的列在接下来要跑的里；跑过的同一个时间不再跑，改了时间再跑
func TestDueSchedule(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "backends", "pa")
	writeProject(t, a, "pa/sa", "https://creght.cn", nil)
	b := filepath.Join(root, "backends", "local-b")
	for name, body := range map[string]string{
		// 失败的不改状态，让它留在 where 里：验证不会反复重跑
		"local/pub.ts":           "export function go(input: any, ctx: any) { const p = ctx.db.get('posts', input.post_id); ctx.db.insert('notes', { post: p.title }); if (p.title === 'bad') throw new Error('boom'); ctx.db.update('posts', p.id, { status: 'published' }) }",
		"tables/posts.json":      `{"name":"帖子","desc":"","json_schema":{"type":"object"}}`,
		"tables/notes.json":      `{"name":"笔记","desc":"","json_schema":{"type":"object"}}`,
		"schedules/publish.json": `{"fn":"pub.go","input":{"post_id":"$id"},"name":"发布","due":{"table":"posts","field":"at","where":{"status":"scheduled"}}}`,
	} {
		p := filepath.Join(b, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(body), 0o644)
	}
	creght.WriteOffline(b, creght.OfflineProject{ProjectID: "local-b", Name: "B"})
	s := newTestServer(&config.Config{Dir: root}, &creght.Workspace{Dir: a, ProjectID: "pa", SiteID: "sa", APIHost: "https://creght.cn"}, nil)
	s.ready.Store(true)
	s.creght = creght.NewClient(s.ws.APIHost)
	t.Cleanup(func() { localdb.Close(s.offlineDBPath("local-b")) })

	host, err := s.projectHost("local-b")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now()
	ins := func(title, status string, at any) string {
		r, err := host.DB.Insert(ctx, "posts", map[string]any{"title": title, "status": status, "at": at})
		if err != nil {
			t.Fatal(err)
		}
		return r["id"].(string)
	}
	ins("old", "scheduled", now.Add(-2*time.Hour).UTC().Format(time.RFC3339))
	bad := ins("bad", "scheduled", now.Add(-time.Hour).UTC().Format(time.RFC3339))
	ins("future", "scheduled", now.Add(time.Hour).UTC().Format(time.RFC3339))
	ins("draft", "draft", now.Add(-time.Hour).UTC().Format(time.RFC3339))
	ins("empty", "scheduled", "")

	notes := func() []string {
		list, _, _, _ := host.DB.Query(ctx, "notes", localfnQuery())
		var out []string
		for _, r := range list {
			out = append(out, r["post"].(string))
		}
		sort.Strings(out)
		return out
	}
	tick := func() {
		s.sched.mu.Lock()
		for _, p := range s.sched.projects {
			p.polled = map[string]time.Time{}
		}
		s.sched.mu.Unlock()
		s.tickSchedules()
		waitSchedulesIdle(t, s)
	}
	tick()
	if got := notes(); len(got) != 2 || got[0] != "bad" || got[1] != "old" {
		t.Fatalf("应该跑了 old、bad：%v", got)
	}
	var job scheduleInfo
	for _, g := range s.scheduleResponse()["projects"].([]scheduleGroup) {
		for _, j := range g.List {
			if j.ID == "publish" {
				job = j
			}
		}
	}
	if job.Due == nil || len(job.Due.Upcoming) != 1 || job.NextAt == nil || job.Last == nil || job.Last.OK || job.Fails != 1 {
		t.Fatalf("设置页的信息不对：%+v %+v", job, job.Due)
	}

	tick()
	if got := notes(); len(got) != 2 {
		t.Fatalf("失败的同一个时间不该重跑：%v", got)
	}
	host.DB.Update(ctx, "posts", bad, map[string]any{"at": now.Add(-time.Minute).UTC().Format(time.RFC3339)})
	tick()
	if got := notes(); len(got) != 3 {
		t.Fatalf("改了时间应该再跑：%v", got)
	}
}

func localfnQuery() localfn.Query { return localfn.Query{Limit: 100} }
