//go:build !windows

package wsgit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// fakeTemplate 起一个假的平台：publish/state 列出 vers 里的版本，file_list?version=N 返回那一版的文件。
// 站点不分（哪个 <project>/<site> 都是这一套版本）。
func fakeTemplate(t *testing.T, vers map[int]map[string]string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/publish/state"):
			var list []map[string]any
			for n := range vers {
				list = append(list, map[string]any{"version_no": n, "note": "n" + itoa(n), "created_at": "2026-10-01T00:00:00Z"})
			}
			json.NewEncoder(w).Encode(map[string]any{"versions": list})
		case strings.HasSuffix(r.URL.Path, "/file_list"):
			n, _ := strconv.Atoi(r.URL.Query().Get("version"))
			var list []map[string]any
			for p, c := range vers[n] {
				list = append(list, map[string]any{"path": "/" + p, "body": c})
			}
			json.NewEncoder(w).Encode(map[string]any{"list": list})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	old := templateHost
	templateHost = func(Template) string { return srv.URL }
	t.Cleanup(func() { templateHost = old })
	t.Setenv("ANNULO_DIR", t.TempDir()) // 没连 creght：不带 token
}

func itoa(n int) string { return strconv.Itoa(n) }

// project 模拟从模板 v1 复制出来的项目
func project(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for p, c := range files {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755)
		os.WriteFile(filepath.Join(dir, p), []byte(c), 0o644)
	}
	if err := Ensure(dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestUpgradeMerges(t *testing.T) {
	v1 := map[string]string{"a.ts": "a1\n", "b.ts": "b1\nb\nb\nb\nb\n", "old.ts": "old\n"}
	v2 := map[string]string{"a.ts": "a2\n", "b.ts": "b1\nb\nb\nb\nb2\n", "new.ts": "new\n"}
	fakeTemplate(t, map[int]map[string]string{1: v1, 2: v2})
	dir := project(t, v1)
	os.WriteFile(filepath.Join(dir, "b.ts"), []byte("mine\nb\nb\nb\nb\n"), 0o644) // 用户改了 b.ts 的第一行
	os.WriteFile(filepath.Join(dir, "mine.ts"), []byte("mine\n"), 0o644)
	tpl := Template{Site: "p/s", Cache: filepath.Join(t.TempDir(), "cache")}

	res, err := Upgrade(context.Background(), dir, tpl, 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "merged" || res.From != 1 || res.To != 2 {
		t.Fatalf("结果：%+v", res)
	}
	if read(t, dir, "a.ts") != "a2\n" || read(t, dir, "new.ts") != "new\n" || read(t, dir, "old.ts") != "<没有>" {
		t.Fatalf("模板的改动没合进来：a=%q new=%q old=%q", read(t, dir, "a.ts"), read(t, dir, "new.ts"), read(t, dir, "old.ts"))
	}
	if read(t, dir, "b.ts") != "mine\nb\nb\nb\nb2\n" || read(t, dir, "mine.ts") != "mine\n" {
		t.Fatalf("用户的改动要保留、和模板的改动一起合并：b=%q", read(t, dir, "b.ts"))
	}
	if Base(dir) != 2 {
		t.Fatalf("基准应该是 v2，得到 %d", Base(dir))
	}
	if st := gitOut(t, dir, "status", "--porcelain"); st != "" {
		t.Fatalf("工作区应该是干净的：%q", st)
	}
	again, err := Upgrade(context.Background(), dir, tpl, 0)
	if err != nil || again.Status != "up_to_date" {
		t.Fatalf("已经是最新：%+v %v", again, err)
	}
}

func TestUpgradeConflict(t *testing.T) {
	v1 := map[string]string{"a.ts": "a1\n"}
	v2 := map[string]string{"a.ts": "a2\n"}
	fakeTemplate(t, map[int]map[string]string{1: v1, 2: v2})
	dir := project(t, v1)
	os.WriteFile(filepath.Join(dir, "a.ts"), []byte("mine\n"), 0o644)
	tpl := Template{Site: "p/s", Cache: filepath.Join(t.TempDir(), "cache")}

	res, err := Upgrade(context.Background(), dir, tpl, 2)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "conflict" || len(res.Conflicts) != 1 || res.Conflicts[0] != "a.ts" {
		t.Fatalf("应该报 a.ts 冲突：%+v", res)
	}
	// 冲突没解决：不能提交
	if _, err := Commit(dir, "助手：试试"); err == nil || !strings.Contains(err.Error(), "冲突") {
		t.Fatalf("有冲突标记时不能提交：%v", err)
	}
	if _, err := Upgrade(context.Background(), dir, tpl, 2); err == nil {
		t.Fatal("冲突没解决前不能再升级")
	}
	// 助手解决了（只改文件，不 git add）：提交就完成合并
	os.WriteFile(filepath.Join(dir, "a.ts"), []byte("mine+a2\n"), 0o644)
	if ok, err := Commit(dir, "助手：解决模板升级冲突"); err != nil || !ok {
		t.Fatalf("解决后应该能提交：%v %v", ok, err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git", "MERGE_HEAD")); err == nil {
		t.Fatal("提交后合并应该结束")
	}
	if Base(dir) != 2 || gitOut(t, dir, "log", "-1", "--format=%p") == "" || len(strings.Fields(gitOut(t, dir, "log", "-1", "--format=%p"))) != 2 {
		t.Fatalf("应该是一个合并提交，基准 v2：base=%d parents=%q", Base(dir), gitOut(t, dir, "log", "-1", "--format=%p"))
	}
}

// 模板当初是从这个项目同步出去的：项目第一个提交很老，但历史中间有一个提交和 v2 一样，
// 之后项目又改了东西。基准要认成 v2（不是和第一个提交最像的 v1），升级到 v3 不冲突。
func TestBootstrapFromHistory(t *testing.T) {
	v1 := map[string]string{"a.ts": "a1\n"}
	v2 := map[string]string{"a.ts": "a2\n", "s.ts": "s\n"}
	v3 := map[string]string{"a.ts": "a3\n", "s.ts": "s\n"}
	fakeTemplate(t, map[int]map[string]string{1: v1, 2: v2, 3: v3})
	dir := project(t, map[string]string{"a.ts": "a0\n"})
	os.WriteFile(filepath.Join(dir, "a.ts"), []byte("a2\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "s.ts"), []byte("s\n"), 0o644)
	Commit(dir, "同步成模板 v2 的样子")
	os.WriteFile(filepath.Join(dir, "s.ts"), []byte("s mine\n"), 0o644) // 之后项目自己改了 s.ts
	Commit(dir, "项目自己的改动")

	res, err := Upgrade(context.Background(), dir, Template{Site: "p/s", Cache: filepath.Join(t.TempDir(), "cache")}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "merged" || res.From != 2 {
		t.Fatalf("基准应该认成 v2 并干净合并：%+v", res)
	}
	if read(t, dir, "a.ts") != "a3\n" || read(t, dir, "s.ts") != "s mine\n" {
		t.Fatalf("a=%q s=%q", read(t, dir, "a.ts"), read(t, dir, "s.ts"))
	}
}

// 模板从这个项目搬过改动（v2 = 项目里的一次提交），项目记的基准还是 v1。升级到 v3（v3 在 v2 改过的同一行上又改了）
// 要先认出项目已经有 v2，把基准推到 v2 再合并：不然 v1→v2 的改动两边各算一次，和 v3 撞成冲突。
func TestUpgradeAdvancesBase(t *testing.T) {
	v1 := map[string]string{"a.ts": "a1\n"}
	v2 := map[string]string{"a.ts": "a2\n"}
	v3 := map[string]string{"a.ts": "a3\n"}
	fakeTemplate(t, map[int]map[string]string{1: v1})
	dir := project(t, v1)
	tpl := Template{Site: "p/s", Cache: filepath.Join(t.TempDir(), "cache")}
	if res, err := Upgrade(context.Background(), dir, tpl, 0); err != nil || res.Status != "up_to_date" || Base(dir) != 1 {
		t.Fatalf("先记下基准 v1：%+v %v", res, err)
	}
	os.WriteFile(filepath.Join(dir, "a.ts"), []byte("a2\n"), 0o644) // 项目里做的改动，后来被搬进模板成了 v2
	Commit(dir, "项目里改的")
	fakeTemplate(t, map[int]map[string]string{1: v1, 2: v2, 3: v3})

	res, err := Upgrade(context.Background(), dir, tpl, 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "merged" || res.From != 2 || read(t, dir, "a.ts") != "a3\n" {
		t.Fatalf("基准要推到 v2 再干净合并到 v3：%+v a=%q", res, read(t, dir, "a.ts"))
	}
}

// 模板新版本的 shuttle.json 要求更高的能力版本：不合并，报「先更新 Shuttle」，项目文件不动
func TestUpgradeRequiresNewerShuttle(t *testing.T) {
	v1 := map[string]string{"a.ts": "a1\n"}
	v2 := map[string]string{"a.ts": "a2\n", "shuttle.json": `{"min_shuttle_api": 5}`}
	fakeTemplate(t, map[int]map[string]string{1: v1, 2: v2})
	dir := project(t, v1)
	tpl := Template{Site: "p/s", Cache: filepath.Join(t.TempDir(), "cache"), API: 3}
	_, err := Upgrade(context.Background(), dir, tpl, 0)
	if err == nil || !strings.Contains(err.Error(), "先更新 Annulo") {
		t.Fatalf("要拒绝：%v", err)
	}
	if read(t, dir, "a.ts") != "a1\n" || Base(dir) != 0 {
		t.Fatalf("不该动项目：a=%q base=%d", read(t, dir, "a.ts"), Base(dir))
	}
	tpl.API = 5
	if res, err := Upgrade(context.Background(), dir, tpl, 0); err != nil || res.Status != "merged" {
		t.Fatalf("能力版本够了就能升：%+v %v", res, err)
	}
}

func TestSwitchTemplate(t *testing.T) {
	// 假 creght 不分站点：v1 当作旧模板（通用）的版本，v5 当作新模板（外贸）的版本
	ops := map[string]string{"a.ts": "a1\na\na\na\n", "xhs.ts": "xhs\n", "edition.ts": "ops\n"}
	trade := map[string]string{"a.ts": "a1\na\na\na2\n", "edition.ts": "trade\n", "trade.ts": "t\n"}
	fakeTemplate(t, map[int]map[string]string{1: ops, 5: trade})
	dir := project(t, ops)
	os.WriteFile(filepath.Join(dir, "a.ts"), []byte("mine\na\na\na\n"), 0o644) // 项目自己改了 a.ts 第一行
	os.WriteFile(filepath.Join(dir, "mine.ts"), []byte("mine\n"), 0o644)
	from := Template{Site: "ops/s", Cache: filepath.Join(t.TempDir(), "ops")}
	to := Template{Site: "trade/s", Cache: filepath.Join(t.TempDir(), "trade")}
	if TemplateSite(dir) != "" {
		t.Fatal("没换过模板时不该有记录")
	}

	res, err := Switch(context.Background(), dir, from, to, 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "merged" || res.From != 1 || res.To != 5 {
		t.Fatalf("结果：%+v", res)
	}
	if read(t, dir, "a.ts") != "mine\na\na\na2\n" || read(t, dir, "mine.ts") != "mine\n" {
		t.Fatalf("项目自己的改动要保留、和新模板一起合并：a=%q", read(t, dir, "a.ts"))
	}
	if read(t, dir, "edition.ts") != "trade\n" || read(t, dir, "trade.ts") != "t\n" || read(t, dir, "xhs.ts") != "<没有>" {
		t.Fatalf("新模板的行业文件要进来、它删掉的要删掉：edition=%q trade=%q xhs=%q", read(t, dir, "edition.ts"), read(t, dir, "trade.ts"), read(t, dir, "xhs.ts"))
	}
	if TemplateSite(dir) != "trade/s" || Base(dir) != 5 {
		t.Fatalf("之后按新模板认：site=%q base=%d", TemplateSite(dir), Base(dir))
	}
	if _, err := Switch(context.Background(), dir, to, to, 0); err == nil {
		t.Fatal("换成同一个模板要报错")
	}
	// 之后照常跟着新模板升级：记录不丢
	again, err := Upgrade(context.Background(), dir, to, 0)
	if err != nil || again.Status != "up_to_date" || TemplateSite(dir) != "trade/s" {
		t.Fatalf("换完再升级：%+v %v site=%q", again, err, TemplateSite(dir))
	}
}

// 项目把模板文件改坏、删掉了：列出来，选中的恢复成模板那一版，接着升级就是最新版；没选的、项目自己加的不动；能撤销。
func TestRestoreThenUpgrade(t *testing.T) {
	v1 := map[string]string{"a.ts": "a1\n", "b.ts": "b1\n", "c.ts": "c1\n", ".gitignore": "x\n"}
	v2 := map[string]string{"a.ts": "a2\n", "b.ts": "b2\n", "c.ts": "c1\n", ".gitignore": "x\n"}
	fakeTemplate(t, map[int]map[string]string{1: v1, 2: v2})
	dir := project(t, v1)
	tpl := Template{Site: "p/s", Cache: filepath.Join(t.TempDir(), "cache")}
	if _, err := Upgrade(context.Background(), dir, tpl, 1); err != nil { // 记下基准 v1
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "a.ts"), []byte("broken\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.ts"), []byte("mine\n"), 0o644)
	os.Remove(filepath.Join(dir, "c.ts"))
	os.WriteFile(filepath.Join(dir, "mine.ts"), []byte("mine\n"), 0o644)

	mod, err := Modified(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, f := range mod {
		got[f.Path] = f.Deleted
	}
	if len(got) != 3 || got["a.ts"] || got["b.ts"] || !got["c.ts"] {
		t.Fatalf("应该列出 a、b（改过）和 c（删掉）：%+v", mod)
	}
	if _, err := Restore(dir, []string{"mine.ts"}); err == nil {
		t.Fatal("项目自己加的文件不能恢复")
	}
	sha, err := Restore(dir, []string{"a.ts", "c.ts"})
	if err != nil {
		t.Fatal(err)
	}
	if read(t, dir, "a.ts") != "a1\n" || read(t, dir, "c.ts") != "c1\n" || read(t, dir, "b.ts") != "mine\n" || read(t, dir, "mine.ts") != "mine\n" {
		t.Fatalf("恢复不对：a=%q c=%q b=%q", read(t, dir, "a.ts"), read(t, dir, "c.ts"), read(t, dir, "b.ts"))
	}
	res, err := Upgrade(context.Background(), dir, tpl, 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "conflict" || len(res.Conflicts) != 1 || res.Conflicts[0] != "b.ts" {
		t.Fatalf("恢复过的 a 直接取新版，只有没恢复的 b 冲突：%+v", res)
	}
	if read(t, dir, "a.ts") != "a2\n" {
		t.Fatalf("a 应该是最新版：%q", read(t, dir, "a.ts"))
	}
	if _, err := UndoRestore(dir, sha); err == nil {
		t.Fatal("合并冲突没解决前不能撤销")
	}
	os.WriteFile(filepath.Join(dir, "b.ts"), []byte("mine+b2\n"), 0o644)
	if _, err := Commit(dir, "解决冲突"); err != nil {
		t.Fatal(err)
	}
	files, err := UndoRestore(dir, sha)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || read(t, dir, "a.ts") != "broken\n" || read(t, dir, "c.ts") != "<没有>" || read(t, dir, "b.ts") != "mine+b2\n" {
		t.Fatalf("撤销要把 a、c 改回恢复前：files=%v a=%q c=%q", files, read(t, dir, "a.ts"), read(t, dir, "c.ts"))
	}
	if st := gitOut(t, dir, "status", "--porcelain"); st != "" {
		t.Fatalf("工作区应该是干净的：%q", st)
	}
	if _, err := UndoRestore(dir, "HEAD"); err == nil {
		t.Fatal("不是恢复提交的不能撤销")
	}
}

func TestUpgradeRequiresNewerAnnulo(t *testing.T) {
	// 新名字 annulo.json / min_annulo_api 和旧的一样管用
	v1 := map[string]string{"a.ts": "a1\n"}
	v2 := map[string]string{"a.ts": "a2\n", "annulo.json": `{"min_annulo_api": 5}`}
	fakeTemplate(t, map[int]map[string]string{1: v1, 2: v2})
	dir := project(t, v1)
	tpl := Template{Site: "p/s", Cache: filepath.Join(t.TempDir(), "cache"), API: 3}
	if _, err := Upgrade(context.Background(), dir, tpl, 0); err == nil {
		t.Fatal("annulo.json 要求更高的能力版本时要拒绝")
	}
	tpl.API = 5
	if res, err := Upgrade(context.Background(), dir, tpl, 0); err != nil || res.Status != "merged" {
		t.Fatalf("能力版本够了就能升：%+v %v", res, err)
	}
	if ProjectMinAPI(dir) != 5 {
		t.Fatalf("项目读 annulo.json：%d", ProjectMinAPI(dir))
	}
}

func TestMinAPIBothKeys(t *testing.T) {
	if MinAPI([]byte(`{"min_shuttle_api": 3, "min_annulo_api": 7}`)) != 7 || MinAPI([]byte(`{"min_shuttle_api": 4}`)) != 4 || MinAPI([]byte(`x`)) != 0 {
		t.Fatal("两个键取大的")
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "shuttle.json"), []byte(`{"min_shuttle_api": 9}`), 0o644)
	os.WriteFile(filepath.Join(dir, "annulo.json"), []byte(`{"min_annulo_api": 6}`), 0o644)
	if ProjectMinAPI(dir) != 9 {
		t.Fatalf("两个文件都有取大的：%d", ProjectMinAPI(dir))
	}
}
