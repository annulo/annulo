package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/annulo/annulo/internal/config"
	"github.com/annulo/annulo/internal/creght"
	"github.com/annulo/annulo/internal/localdb"
)

// 开着在线项目 A：离线项目 B 的本机函数定时任务照样跑（在 B 的目录、B 的 SQLite 里），交给助手的不跑
func TestSchedulesOtherProjects(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "backends", "pa")
	writeProject(t, a, "pa/sa", "https://creght.cn", nil)
	b := filepath.Join(root, "backends", "local-b")
	for name, body := range map[string]string{
		"local/ping.ts":           "export function run(input: any, ctx: any) { ctx.db.insert('notes', { project: ctx.workspace.project_id, offline: ctx.workspace.offline }); return { ok: true } }",
		"tables/notes.json":       `{"name":"笔记","desc":"","json_schema":{"type":"object"}}`,
		"schedules/ping.run.json": `{"fn":"ping.run","every":"1d","name":"ping"}`,
		"schedules/report.json":   `{"prompt":"写周报","every":"1d","name":"周报"}`,
	} {
		p := filepath.Join(b, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(body), 0o644)
	}
	creght.WriteOffline(b, creght.OfflineProject{ProjectID: "local-b", Name: "B 店"})

	s := &Server{cfg: &config.Config{Dir: root}, ws: &creght.Workspace{Dir: a, ProjectID: "pa", SiteID: "sa", APIHost: "https://creght.cn"}}
	s.ready.Store(true)
	s.creght = creght.NewClient(s.ws.APIHost)
	t.Cleanup(func() { localdb.Close(s.offlineDBPath("local-b")) })

	ps := s.scheduleProjects()
	if len(ps) != 2 || ps[1].ID != "local-b" || ps[1].Name != "B 店" || !ps[1].Offline {
		t.Fatalf("项目不对：%+v", ps)
	}
	s.tickSchedules()
	db, err := localdb.Open(s.offlineDBPath("local-b"))
	if err != nil {
		t.Fatal(err)
	}
	var recs []creght.Record
	for i := 0; i < 100; i++ {
		recs, _ = db.ListRecords(context.Background(), "", "notes", creght.RecordQuery{})
		if len(recs) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(recs) != 1 || !strings.Contains(string(recs[0].Body), `"offline":true,"project":"local-b"`) {
		t.Fatalf("B 的函数没在 B 里跑：%v", recs)
	}
	res := s.scheduleResponse()
	groups := res["projects"].([]scheduleGroup)
	var bg *scheduleGroup
	for i := range groups {
		if groups[i].ProjectID == "local-b" {
			bg = &groups[i]
		}
	}
	if bg == nil || len(bg.List) != 2 {
		t.Fatalf("分组不对：%+v", groups)
	}
	for _, j := range bg.List {
		if j.ID == "report" && (!j.OnlyWhenOpen || j.Last != nil) {
			t.Fatalf("交给助手的不该在别的项目里跑：%+v", j)
		}
	}
}
