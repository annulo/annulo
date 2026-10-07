package server

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRecordPlugin(t *testing.T) {
	dir := t.TempDir()
	tpl := "{\n  \"name\": { \"zh\": \"外贸\", \"en\": \"Trade\" },\n  \"plugins\": { \"social\": \"https://github.com/annulo/plugins#social\" }\n}\n"
	os.WriteFile(filepath.Join(dir, "annulo.json"), []byte(tpl), 0o644)
	user := filepath.Join(dir, "user", "annulo.json")
	if got := projectPlugins(dir); got["social"] != "https://github.com/annulo/plugins#social" {
		t.Fatal("模板写的", got)
	}

	// 装模板要的：和模板写的一样，不用记
	if err := recordPlugin(dir, "social", "https://github.com/annulo/plugins#social"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(user); !os.IsNotExist(err) {
		t.Fatal("和模板一样不写 user/annulo.json")
	}
	// 用户自己装的：记在 user/annulo.json，模板的 annulo.json 不动
	if err := recordPlugin(dir, "crm", "https://example.com/p#crm"); err != nil {
		t.Fatal(err)
	}
	// 卸掉模板要的：记成 null，不再算项目要的
	if err := recordPlugin(dir, "social", ""); err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"plugins\": {\n    \"crm\": \"https://example.com/p#crm\",\n    \"social\": null\n  }\n}\n"
	if b, _ := os.ReadFile(user); string(b) != want {
		t.Fatalf("%s", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "annulo.json")); string(b) != tpl {
		t.Fatal("模板的 annulo.json 不动")
	}
	if got := projectPlugins(dir); len(got) != 1 || got["crm"] == "" {
		t.Fatal("项目要的", got)
	}
	// 再装回来、卸掉 crm：user/annulo.json 没东西了就删掉
	recordPlugin(dir, "social", "https://github.com/annulo/plugins#social")
	recordPlugin(dir, "crm", "")
	if _, err := os.Stat(user); !os.IsNotExist(err) {
		t.Fatal("空了就删掉")
	}
}

func TestPluginDeclarations(t *testing.T) {
	root := t.TempDir()
	write := func(p, c string) {
		os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755)
		os.WriteFile(filepath.Join(root, p), []byte(c), 0o644)
	}
	write("tables/notes.json", `{"name":"备忘","json_schema":{"type":"object"}}`)
	write("plugins/social/plugin.json", `{}`)
	write("plugins/social/tables/posts.json", `{"name":"帖子","json_schema":{"type":"object"}}`)
	write("plugins/social/schedules/x.collect.json", `{"fn":"social/x.collect","every":"6h"}`)
	defs, err := parseTables(root)
	if err != nil || len(defs) != 2 || defs[0].Key != "notes" || defs[1].Key != "social_posts" {
		t.Fatalf("表：%+v %v", defs, err)
	}
	sched, err := parseSchedules(root)
	if err != nil || len(sched) != 1 || sched[0].ID != "social/x.collect" {
		t.Fatalf("定时任务：%+v %v", sched, err)
	}
	write("tables/social_posts.json", `{"name":"重名","json_schema":{"type":"object"}}`)
	if _, err := parseTables(root); err == nil {
		t.Fatal("项目的表和插件的表重名要报错")
	}
	if id, action := splitNameAction(root, "social/write-x/run"); id != "social/write-x" || action != "run" {
		t.Fatal(id, action)
	}
	if id, action := splitNameAction(root, "weekly/run"); id != "weekly" || action != "run" {
		t.Fatal(id, action)
	}
}
