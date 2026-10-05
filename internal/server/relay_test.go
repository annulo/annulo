package server

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/annulo/annulo/internal/config"
	"github.com/annulo/annulo/internal/creght"
)

func writeProject(t *testing.T, dir, siteID, host string, files map[string]string) {
	t.Helper()
	os.MkdirAll(filepath.Join(dir, ".creght"), 0o755)
	os.WriteFile(filepath.Join(dir, ".creght", "state.json"), []byte(`{"site_id":"`+siteID+`","api_host":"`+host+`"}`), 0o644)
	for name, code := range files {
		p := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(code), 0o644)
	}
}

// 电脑上开着 A，手机上调 B：B 的函数在 B 的目录里跑，用 B 的表声明；别的集群的项目不登记
func TestRelayOtherProject(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "backends", "pa")
	writeProject(t, a, "pa/sa", "https://creght.cn", nil)
	writeProject(t, filepath.Join(root, "backends", "pb"), "pb/sb", "https://creght.cn", map[string]string{
		"local/pub.ts":         "export const remote = ['publish']\nexport function publish(input: any, ctx: any) { return { project: ctx.workspace.project_id, ok: ctx.db ? true : false } }\nexport function other() { return 1 }",
		"tables/articles.json": `{"name":"文章","desc":"","json_schema":{"type":"object"}}`,
	})
	writeProject(t, filepath.Join(root, "backends", "pc"), "pc/sc", "https://creght.com", nil)

	s := &Server{cfg: &config.Config{Dir: root}, ws: &creght.Workspace{Dir: a, ProjectID: "pa", SiteID: "sa", APIHost: "https://creght.cn"}}
	s.ready.Store(true)
	s.creght = creght.NewClient(s.ws.APIHost)

	ps := s.relayProjects()
	if !slices.Contains(ps, "pa") || !slices.Contains(ps, "pb") || slices.Contains(ps, "pc") {
		t.Fatalf("登记的项目不对：%v", ps)
	}
	h, err := s.projectHost("pb")
	if err != nil {
		t.Fatal(err)
	}
	if h.WorkDir != filepath.Join(root, "backends", "pb") || h.Workspace["project_id"] != "pb" {
		t.Fatalf("B 的环境不对：%s %v", h.WorkDir, h.Workspace)
	}
	db := h.DB.(localDB).p
	if db.projectID != "pb" || !db.isTable("articles") || db.isTable("leads") {
		t.Fatalf("B 的表不对：%s", db.projectID)
	}
	if _, err := s.relayRun(t.Context(), "pb", "pub.other", nil, func(any) {}); err == nil || !strings.Contains(err.Error(), "remote") {
		t.Fatalf("没声明 remote 的应该拒绝：%v", err)
	}
	if _, err := s.projectHost("pz"); err == nil {
		t.Fatal("本机没有的项目应该报错")
	}
	if _, err := s.projectHost("pc"); err == nil {
		t.Fatal("别的集群的项目应该报错")
	}
}
