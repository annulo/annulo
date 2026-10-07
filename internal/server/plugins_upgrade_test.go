package server

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/annulo/annulo/internal/config"
	"github.com/annulo/annulo/internal/plugin"
	"github.com/annulo/annulo/internal/wsgit"
)

// 模板新版本要一个插件，升级时项目改过的文件和模板冲突：合并停在进行中，插件也要已经装好（合并之前装的），
// 不然解决完冲突一推送，用到插件的云端函数就打包失败。
func TestTemplateUpgradeInstallsPluginsBeforeMerge(t *testing.T) {
	newRepo := func() (string, func(files map[string]string, tag string)) {
		repo := t.TempDir()
		git := func(args ...string) {
			t.Helper()
			cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
			cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v：%s", args, out)
			}
		}
		git("init", "-q")
		return repo, func(files map[string]string, tag string) {
			for p, c := range files {
				os.MkdirAll(filepath.Dir(filepath.Join(repo, p)), 0o755)
				os.WriteFile(filepath.Join(repo, p), []byte(c), 0o644)
			}
			git("add", "-A")
			git("commit", "-q", "-m", tag)
			git("tag", "-a", tag, "-m", tag)
		}
	}
	pluginRepo, pluginTag := newRepo()
	pluginTag(map[string]string{"social/plugin.json": "{}", "social/local/stats.ts": "export function summary() { return {} }\n"}, "v1.0.0")
	tplRepo, tplTag := newRepo()
	tplTag(map[string]string{"tpl/annulo.json": "{}\n", "tpl/local/channels.ts": "line1\n"}, "v1.0.0")

	data := t.TempDir()
	s := &Server{cfg: &config.Config{Dir: data}}
	tpl := wsgit.Template{Site: wsgit.GitSite(tplRepo, "tpl"), Cache: filepath.Join(data, "tplcache")}
	ctx := context.Background()

	// 从 v1 建的项目，项目自己改了 channels.ts
	dir := t.TempDir()
	vs, _ := wsgit.Versions(ctx, tpl)
	files, _ := wsgit.FilesAt(ctx, tpl, vs[0].No)
	for p, c := range files {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755)
		os.WriteFile(filepath.Join(dir, p), []byte(c), 0o644)
	}
	if err := wsgit.InitOffline(dir, vs[0], tpl.Site); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "local/channels.ts"), []byte("mine\n"), 0o644)
	wsgit.Commit(dir, "项目自己的改动")

	// 模板 v2：要社媒插件，channels.ts 也改了（和项目冲突）
	// 路径用 json.Marshal 拼进 JSON：Windows 的 C:\Users\… 直接拼进去，反斜杠会被当成转义，JSON 就坏了
	spec, _ := json.Marshal(map[string]any{"plugins": map[string]string{"social": filepath.ToSlash(pluginRepo) + "#social"}})
	tplTag(map[string]string{"tpl/annulo.json": string(spec) + "\n", "tpl/local/channels.ts": "import '../plugins/social/local/stats'\n"}, "v2.0.0")

	installed := s.installTemplatePlugins(ctx, dir, tpl, 0)
	if len(installed) != 1 || installed[0] != "social" {
		t.Fatalf("合并之前装上插件：%v", installed)
	}
	res, err := wsgit.Upgrade(ctx, dir, tpl, 0)
	if err != nil || res.Status != "conflict" {
		t.Fatalf("模板升级有冲突：%+v %v", res, err)
	}
	if ids := plugin.IDs(dir); len(ids) != 1 || ids[0] != "social" {
		t.Fatalf("冲突时插件已经在了：%v", ids)
	}

	// 用户卸掉过模板带的插件：不再自动装
	dir2 := t.TempDir()
	os.MkdirAll(filepath.Join(dir2, "user"), 0o755)
	os.WriteFile(filepath.Join(dir2, "user", "annulo.json"), []byte(`{"plugins": {"social": null}}`), 0o644)
	wsgit.Commit(dir2, "init")
	if got := s.installTemplatePlugins(ctx, dir2, tpl, 0); len(got) != 0 {
		t.Fatalf("用户不要的不装：%v", got)
	}
}
