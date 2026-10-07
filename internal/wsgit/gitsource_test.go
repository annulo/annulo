package wsgit

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitTemplateRepo 建一个本机的模板仓库：模板放在 tpl/ 子目录，每个 tag 一版。
func gitTemplateRepo(t *testing.T) (repo string, tag func(name, note string, files map[string]string)) {
	t.Helper()
	repo = t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v：%s", args, out)
		}
	}
	run("init", "-q")
	os.WriteFile(filepath.Join(repo, "README.md"), []byte("源码说明，不在模板里\n"), 0o644)
	return repo, func(name, note string, files map[string]string) {
		t.Helper()
		os.RemoveAll(filepath.Join(repo, "tpl"))
		for p, c := range files {
			full := filepath.Join(repo, "tpl", p)
			os.MkdirAll(filepath.Dir(full), 0o755)
			os.WriteFile(full, []byte(c), 0o644)
		}
		run("add", "-A")
		run("commit", "-q", "-m", name)
		run("tag", "-a", name, "-m", note)
	}
}

func TestSemverNo(t *testing.T) {
	for tag, want := range map[string]int{"v1.2.3": 1002003, "1.0.0": 1000000, "v0.10.7": 10007, "v12.0.1": 12000001} {
		if got, ok := SemverNo(tag); !ok || got != want {
			t.Errorf("%s → %d %v，想要 %d", tag, got, ok, want)
		}
	}
	for _, tag := range []string{"v2.0.0-beta.1", "release", "v1.2", "v1.1000.0"} {
		if _, ok := SemverNo(tag); ok {
			t.Errorf("%s 不该认", tag)
		}
	}
	if VersionLabel("git:x", 1002003) != "v1.2.3" || VersionLabel("p/s", 7) != "7" {
		t.Fatal("版本显示")
	}
	if site := GitSite("https://x/y.git", "/tpl/"); site != "git:https://x/y.git#tpl" || !IsGitSite(site) {
		t.Fatal(site)
	}
	if r, s := ParseGitSite("git:https://x/y.git#tpl"); r != "https://x/y.git" || s != "tpl" {
		t.Fatal(r, s)
	}
}

func TestGitTemplateVersionsAndUpgrade(t *testing.T) {
	repo, tag := gitTemplateRepo(t)
	v1 := map[string]string{"a.ts": "a1\na\na\na\n", "annulo.json": `{"name": {"zh": "空白", "en": "Blank"}, "description": "空白模板"}`}
	tag("v1.0.0", "第一版", v1)
	tpl := Template{Site: GitSite(repo, "tpl"), Cache: filepath.Join(t.TempDir(), "cache"), API: 26}
	ctx := context.Background()

	vs, err := Versions(ctx, tpl)
	if err != nil || len(vs) != 1 || vs[0].No != 1000000 || vs[0].Label != "v1.0.0" || vs[0].Note != "第一版" {
		t.Fatalf("版本：%+v %v", vs, err)
	}
	files, err := FilesAt(ctx, tpl, vs[0].No)
	if err != nil || files["a.ts"] != v1["a.ts"] || files["README.md"] != "" {
		t.Fatalf("只取子目录里的文件：%v %v", files, err)
	}
	if m, err := GitMeta(ctx, tpl); err != nil || m.Name != [2]string{"空白", "Blank"} || m.Desc[0] != "空白模板" {
		t.Fatalf("名字和简介：%+v %v", m, err)
	}

	// 照新建离线项目的做法建项目：文件写进去，template 分支从这一版开始
	dir := t.TempDir()
	for p, c := range files {
		os.WriteFile(filepath.Join(dir, p), []byte(c), 0o644)
	}
	if err := InitOffline(dir, vs[0], tpl.Site); err != nil {
		t.Fatal(err)
	}
	if Base(dir) != 1000000 || TemplateSite(dir) != tpl.Site {
		t.Fatalf("记下基于哪一版：%d %q", Base(dir), TemplateSite(dir))
	}
	os.WriteFile(filepath.Join(dir, "a.ts"), []byte("mine\na\na\na\n"), 0o644) // 项目改了第一行
	os.MkdirAll(filepath.Join(dir, "user"), 0o755)
	os.WriteFile(filepath.Join(dir, "user", "x.md"), []byte("我的\n"), 0o644)
	if _, err := Commit(dir, "项目自己的改动"); err != nil {
		t.Fatal(err)
	}

	tag("v1.1.0", "改了最后一行", map[string]string{"a.ts": "a1\na\na\na2\n", "annulo.json": v1["annulo.json"], "b.ts": "b\n"})
	tag("v2.0.0-beta.1", "预发布", map[string]string{"a.ts": "beta\n"})
	vs, err = Versions(ctx, tpl)
	if err != nil || len(vs) != 2 || vs[0].Label != "v1.1.0" {
		t.Fatalf("预发布不列，新的在前：%+v %v", vs, err)
	}
	res, err := Upgrade(ctx, dir, tpl, 0)
	if err != nil || res.Status != "merged" || res.From != 1000000 || res.To != 1001000 {
		t.Fatalf("升级：%+v %v", res, err)
	}
	if read(t, dir, "a.ts") != "mine\na\na\na2\n" || read(t, dir, "b.ts") != "b\n" || read(t, dir, "user/x.md") != "我的\n" {
		t.Fatalf("两边的改动都在：a=%q b=%q user=%q", read(t, dir, "a.ts"), read(t, dir, "b.ts"), read(t, dir, "user/x.md"))
	}
}

func TestGitTemplateNotFound(t *testing.T) {
	tpl := Template{Site: GitSite(filepath.Join(t.TempDir(), "nope"), ""), Cache: filepath.Join(t.TempDir(), "cache")}
	_, err := Versions(context.Background(), tpl)
	if err == nil || !strings.Contains(err.Error(), "模板仓库") {
		t.Fatalf("地址不对要说清楚：%v", err)
	}
}

func TestCatalogDirs(t *testing.T) {
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v：%s", args, out)
		}
	}
	write := func(p, c string) {
		os.MkdirAll(filepath.Dir(filepath.Join(repo, p)), 0o755)
		os.WriteFile(filepath.Join(repo, p), []byte(c), 0o644)
	}
	run("init", "-q")
	write("blank/annulo.json", "{}")
	write("old/shuttle.json", "{}")
	write("docs/readme.md", "说明，不是模板")
	write("nested/a/annulo.json", "{}") // 只认一级子目录
	run("add", "-A")
	run("commit", "-q", "-m", "v1")
	run("tag", "-a", "v1.0.0", "-m", "v1")
	write("creator/annulo.json", "{}") // 没发版本的不算
	run("add", "-A")
	run("commit", "-q", "-m", "wip")

	tpl := Template{Site: GitSite(repo, ""), Cache: filepath.Join(t.TempDir(), "cache")}
	dirs, err := CatalogDirs(context.Background(), tpl, "annulo.json", "shuttle.json")
	if err != nil || strings.Join(dirs, ",") != "blank,old" {
		t.Fatalf("%v %v", dirs, err)
	}
	run("tag", "-a", "v1.1.0", "-m", "v1.1")
	if dirs, _ := CatalogDirs(context.Background(), tpl, "annulo.json"); strings.Join(dirs, ",") != "blank,creator" {
		t.Fatalf("新版本里加的模板要列出来：%v", dirs)
	}
}
