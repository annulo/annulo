package wsgit

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPluginInstallUpgradeRemove(t *testing.T) {
	repo, tag := gitTemplateRepo(t) // 插件放在仓库的 tpl/ 子目录：插件 id 是 tpl
	meta := `{"name": {"zh": "示例", "en": "Sample"}, "min_annulo_api": 28}`
	tag("v1.0.0", "第一版", map[string]string{"plugin.json": meta, "local/x.ts": "x1\nx\nx\nx\n", "tables/posts.json": "{}"})
	src := Template{Site: GitSite(repo, "tpl"), Cache: filepath.Join(t.TempDir(), "cache"), API: 28}
	ctx := context.Background()
	id, err := PluginID(src.Site)
	if err != nil || id != "tpl" {
		t.Fatalf("插件 id：%q %v", id, err)
	}

	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.ts"), []byte("项目自己的\n"), 0o644)
	if _, err := Commit(dir, "项目"); err != nil {
		t.Fatal(err)
	}

	res, err := UpgradePlugin(ctx, dir, id, src, 0)
	if err != nil || res.Status != "merged" || res.From != 0 || res.To != 1000000 {
		t.Fatalf("安装：%+v %v", res, err)
	}
	if read(t, dir, "plugins/tpl/local/x.ts") != "x1\nx\nx\nx\n" || read(t, dir, "a.ts") != "项目自己的\n" {
		t.Fatal("装进 plugins/tpl/，项目的文件不动")
	}
	if PluginBase(dir, id) != 1000000 || PluginSource(dir, id) != src.Site {
		t.Fatalf("记下版本和来源：%d %q", PluginBase(dir, id), PluginSource(dir, id))
	}

	// 项目改了插件文件的第一行；插件新版改了最后一行、删了表、加了任务
	os.WriteFile(filepath.Join(dir, "plugins/tpl/local/x.ts"), []byte("mine\nx\nx\nx\n"), 0o644)
	if _, err := Commit(dir, "修插件"); err != nil {
		t.Fatal(err)
	}
	tag("v1.1.0", "改了最后一行", map[string]string{"plugin.json": meta, "local/x.ts": "x1\nx\nx\nx2\n", "tasks/w.md": "写\n"})
	res, err = UpgradePlugin(ctx, dir, id, src, 0)
	if err != nil || res.Status != "merged" || res.From != 1000000 || res.To != 1001000 {
		t.Fatalf("升级：%+v %v", res, err)
	}
	if read(t, dir, "plugins/tpl/local/x.ts") != "mine\nx\nx\nx2\n" || read(t, dir, "plugins/tpl/tasks/w.md") != "写\n" {
		t.Fatalf("两边的改动都在：%q", read(t, dir, "plugins/tpl/local/x.ts"))
	}
	if _, err := os.Stat(filepath.Join(dir, "plugins/tpl/tables/posts.json")); !os.IsNotExist(err) {
		t.Fatal("新版删掉的文件要删掉")
	}
	if res, err := UpgradePlugin(ctx, dir, id, src, 0); err != nil || res.Status != "up_to_date" {
		t.Fatalf("已经是最新：%+v %v", res, err)
	}

	// 要求更高的能力版本：不合并
	tag("v2.0.0", "要新 Annulo", map[string]string{"plugin.json": `{"min_annulo_api": 99}`, "local/x.ts": "x\n"})
	if _, err := UpgradePlugin(ctx, dir, id, src, 0); err == nil {
		t.Fatal("能力版本不够要拒绝")
	}

	if err := RemovePlugin(dir, id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "plugins/tpl")); !os.IsNotExist(err) || PluginBase(dir, id) != 0 {
		t.Fatal("卸载：目录和分支都没了")
	}
	// 卸载后再装：从头装最新能装的那版
	if res, err := UpgradePlugin(ctx, dir, id, src, 1001000); err != nil || res.Status != "merged" || read(t, dir, "plugins/tpl/local/x.ts") != "x1\nx\nx\nx2\n" {
		t.Fatalf("重装：%+v %v", res, err)
	}
}
