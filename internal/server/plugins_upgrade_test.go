package server

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/annulo/annulo/internal/config"
	"github.com/annulo/annulo/internal/plugin"
	"github.com/annulo/annulo/internal/wsgit"
)

// 模板新版本要一个插件，升级时项目改过的文件和模板冲突：合并停在进行中，插件也要已经装好（合并之前装的），
// 不然解决完冲突一推送，用到插件的云端函数就打包失败。
func TestTemplateUpgradeInstallsPluginsBeforeMerge(t *testing.T) {
	newRepo := func() (string, func(files map[string]string, tag string)) { return taggedRepo(t) }
	pluginRepo, pluginTag := newRepo()
	pluginTag(map[string]string{"social/plugin.json": "{}", "social/local/stats.ts": "export function summary() { return {} }\n"}, "v1.0.0")
	tplRepo, tplTag := newRepo()
	tplTag(map[string]string{"tpl/annulo.json": "{}\n", "tpl/local/channels.ts": "line1\n"}, "v1.0.0")

	data := t.TempDir()
	s := newTestServer(&config.Config{Dir: data}, nil, nil)
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

	installed, err := s.installTemplatePlugins(ctx, dir, tpl, 0)
	if err != nil || len(installed) != 1 || installed[0] != "social" {
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
	if got, _ := s.installTemplatePlugins(ctx, dir2, tpl, 0); len(got) != 0 {
		t.Fatalf("用户不要的不装：%v", got)
	}
}

// taggedRepo 建一个本地 git 仓库，返回路径和「写这些文件、提交、打 tag」的函数（模板、插件仓库都用它）。
func taggedRepo(t *testing.T) (string, func(files map[string]string, tag string)) {
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

// 模板新版本要插件至少某一版（annulo.json 的 plugins_min）：项目装的旧了，升级模板之前先把插件升上去；
// 插件那边还没发这一版时报错，不合并模板。
func TestTemplateUpgradeRaisesPluginToMin(t *testing.T) {
	pluginRepo, pluginTag := taggedRepo(t)
	pluginTag(map[string]string{"social/plugin.json": "{}", "social/local/_fields.ts": "export const FIELDS = {}\n"}, "v1.0.0")
	tplRepo, tplTag := taggedRepo(t)
	src := filepath.ToSlash(pluginRepo) + "#social"
	spec := func(min string) string {
		v := map[string]any{"plugins": map[string]string{"social": src}}
		if min != "" {
			v["plugins_min"] = map[string]string{"social": min}
		}
		b, _ := json.Marshal(v)
		return string(b) + "\n"
	}
	tplTag(map[string]string{"tpl/annulo.json": spec(""), "tpl/lib/social.ts": "v1\n"}, "v1.0.0")

	data := t.TempDir()
	s := newTestServer(&config.Config{Dir: data}, nil, nil)
	tpl := wsgit.Template{Site: wsgit.GitSite(tplRepo, "tpl"), Cache: filepath.Join(data, "tplcache")}
	ctx := context.Background()

	// 从模板 v1 建的项目，装了插件 v1.0.0
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
	if got := s.installProjectPlugins(ctx, dir); len(got) != 1 {
		t.Fatalf("装上插件：%v", got)
	}
	if b := wsgit.PluginBase(dir, "social"); b != 1_000_000 {
		t.Fatalf("插件 v1.0.0：%d", b)
	}

	// 模板 v2 要插件至少 0.5.0 → 已经够了，不动；要 1.1.0 → 插件还没发，报错、不升
	tplTag(map[string]string{"tpl/annulo.json": spec("v1.1.0"), "tpl/lib/social.ts": "import '../plugins/social/local/_fields'\n"}, "v2.0.0")
	snap, err := wsgit.VersionDir(ctx, tpl, 2_000_000)
	if err != nil {
		t.Fatal(err)
	}
	needs := s.pluginNeeds(ctx, dir, snap)
	if len(needs) != 1 || needs[0].Min != 1_001_000 || needs[0].Base != 1_000_000 || needs[0].Latest != 1_000_000 {
		t.Fatalf("升级卡片上的插件要求：%+v", needs)
	}
	if _, err := s.installTemplatePlugins(ctx, dir, tpl, 0); err == nil || !strings.Contains(err.Error(), "1.1.0") {
		t.Fatalf("插件还没发要的版本：要报错，得到 %v", err)
	}
	if b := wsgit.PluginBase(dir, "social"); b != 1_000_000 {
		t.Fatalf("报错时插件不动：%d", b)
	}

	// 插件发了 v1.1.0：升级模板之前先把插件升上去
	pluginTag(map[string]string{"social/local/_fields.ts": "export const FIELDS = { zhihu: {} }\n"}, "v1.1.0")
	got, err := s.installTemplatePlugins(ctx, dir, tpl, 0)
	if err != nil || len(got) != 1 || got[0] != "social" {
		t.Fatalf("先升插件：%v %v", got, err)
	}
	if b := wsgit.PluginBase(dir, "social"); b != 1_001_000 {
		t.Fatalf("插件升到 v1.1.0：%d", b)
	}
	if needs := s.pluginNeeds(ctx, dir, snap); len(needs) != 0 {
		t.Fatalf("够了就不再提示：%+v", needs)
	}
	if res, err := wsgit.Upgrade(ctx, dir, tpl, 0); err != nil || res.Status != "merged" {
		t.Fatalf("接着升级模板：%+v %v", res, err)
	}
}
