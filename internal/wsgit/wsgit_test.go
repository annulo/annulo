package wsgit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/br41n10/qetag"

	"github.com/annulo/annulo/internal/creght"
)

// fakeSite 是假的 creght 平台上的一个站点：file_list 列文件、site_action 改文件，和 creght-cli 的 sitesync 测试一样。
type fakeSite struct {
	mu      sync.Mutex
	files   map[string]string // 路径（带开头的 /）→ 内容
	ids     map[string]string
	n       int
	actions int // site_action 调了几次（推送了几回）
}

func (f *fakeSite) put(p, body string) {
	if _, ok := f.ids[p]; !ok {
		f.n++
		f.ids[p] = fmt.Sprintf("f%d", f.n)
	}
	f.files[p] = body
}

func (f *fakeSite) edit(p, body string) { f.mu.Lock(); f.put(p, body); f.mu.Unlock() }

func (f *fakeSite) get(p string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.files[p]
	return b, ok
}

func (f *fakeSite) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case strings.HasSuffix(r.URL.Path, "/file_list"):
		list := []map[string]any{}
		for p, b := range f.files {
			q := qetag.New()
			q.Write([]byte(b))
			list = append(list, map[string]any{"id": f.ids[p], "path": p, "body": b, "hash": q.Etag()})
		}
		json.NewEncoder(w).Encode(map[string]any{"list": list})
	case strings.HasSuffix(r.URL.Path, "/site_action"):
		f.actions++
		var req struct {
			Changes []struct {
				Action string `json:"action"`
				File   struct {
					ID   string  `json:"id"`
					Path *string `json:"path"`
					Body *string `json:"body"`
				} `json:"file"`
			} `json:"changes"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		for _, c := range req.Changes {
			switch c.Action {
			case "file_create":
				f.put(*c.File.Path, *c.File.Body)
			case "file_update":
				for p, id := range f.ids {
					if id == c.File.ID {
						f.files[p] = *c.File.Body
					}
				}
			case "file_delete":
				for p, id := range f.ids {
					if id == c.File.ID || (c.File.Path != nil && *c.File.Path == p) {
						delete(f.files, p)
					}
				}
			}
		}
		fmt.Fprintf(w, `{"ok":true,"result":{"total":%d,"success":%d,"failed":0}}`, len(req.Changes), len(req.Changes))
	default:
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"code":404,"message":"not found"}`))
	}
}

// newWorkspace 在假平台上建一个站点（a.txt、b.txt），拉到本机、初始化 git：和真实的在线项目一样。
func newWorkspace(t *testing.T) (string, *fakeSite) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("没有 git")
	}
	site := &fakeSite{files: map[string]string{}, ids: map[string]string{}}
	site.put("/a.txt", "a\n")
	site.put("/b.txt", "b\n")
	srv := httptest.NewServer(site)
	t.Cleanup(srv.Close)
	t.Setenv("ANNULO_DIR", t.TempDir())
	creght.StoreToken(srv.URL, "tok", time.Now().Add(time.Hour))
	dir := filepath.Join(t.TempDir(), "ws")
	if err := PullSite(context.Background(), srv.URL, "p1/s1", dir, nil); err != nil {
		t.Fatal(err)
	}
	if err := Ensure(dir); err != nil {
		t.Fatal(err)
	}
	return dir, site
}

func gitOut(t *testing.T, dir string, args ...string) string {
	out, err := git(dir, args...)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(out)
}

func TestEnsure(t *testing.T) {
	dir, _ := newWorkspace(t)
	files := gitOut(t, dir, "ls-files")
	if files != ".creghtignore\n.gitignore\na.txt\nb.txt" {
		t.Fatalf("初始提交应该只有 .gitignore 和 a.txt（.creght/、AGENTS.md 被忽略），实际：%q", files)
	}
	if err := Ensure(dir); err != nil { // 第二次什么都不做
		t.Fatal(err)
	}
	if n := gitOut(t, dir, "rev-list", "--count", "HEAD"); n != "1" {
		t.Fatalf("提交数 %s", n)
	}
	if ok, err := Commit(dir, "无改动"); ok || err != nil {
		t.Fatalf("没改动不该提交：%v %v", ok, err)
	}
}

func TestPushLocalChange(t *testing.T) {
	dir, site := newWorkspace(t)
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a2\n"), 0o644)
	if err := Push(context.Background(), dir, "改 a", true, io.Discard); err != nil {
		t.Fatal(err)
	}
	if b, _ := site.get("/a.txt"); b != "a2\n" {
		t.Fatalf("推上去：%q", b)
	}
	if m := gitOut(t, dir, "log", "-1", "--format=%s"); m != "改 a" {
		t.Fatalf("提交说明：%q", m)
	}
}

func TestPushDelete(t *testing.T) {
	dir, site := newWorkspace(t)
	os.Remove(filepath.Join(dir, "b.txt"))
	if err := Push(context.Background(), dir, "", true, io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, ok := site.get("/b.txt"); ok {
		t.Fatal("本地删了的文件远端也要删")
	}
}

func TestPushNothing(t *testing.T) {
	dir, site := newWorkspace(t)
	if err := Push(context.Background(), dir, "", true, io.Discard); err != nil {
		t.Fatal(err)
	}
	if site.actions != 0 {
		t.Fatalf("没改动不该推：%d", site.actions)
	}
}

func TestPushMergesRemoteFirst(t *testing.T) {
	dir, site := newWorkspace(t)
	site.edit("/b.txt", "remote\n") // 编辑器或别的设备改了一个文件
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a2\n"), 0o644)
	if err := Push(context.Background(), dir, "改 a", true, io.Discard); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "b.txt")); string(b) != "remote\n" {
		t.Fatalf("远端的改动合进来：%q", b)
	}
	if b, _ := site.get("/a.txt"); b != "a2\n" {
		t.Fatalf("本地的改动推上去：%q", b)
	}
	subjects := gitOut(t, dir, "log", "--format=%s")
	if !strings.HasPrefix(subjects, "合入远端改动：") || !strings.Contains(strings.SplitN(subjects, "\n", 2)[0], "b.txt") || !strings.Contains(subjects, "\n改 a\n") {
		t.Fatalf("远端改动要单独提交：%q", subjects)
	}
}

func TestPushConflict(t *testing.T) {
	dir, site := newWorkspace(t)
	site.edit("/a.txt", "remote\n")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("local\n"), 0o644)
	err := Push(context.Background(), dir, "", true, io.Discard)
	if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "a.txt") {
		t.Fatalf("要报冲突并列出文件：%v", err)
	}
	if site.actions != 0 {
		t.Fatal("有冲突不该推")
	}
	if b, _ := site.get("/a.txt"); b != "remote\n" {
		t.Fatalf("远端不动：%q", b)
	}
	if strings.Contains(gitOut(t, dir, "log", "--format=%s"), "合入远端") {
		t.Fatal("冲突标记不该被提交")
	}
}

// macOS 默认不区分大小写：git 里同时有 pages/index.tsx 和 pages/Index.tsx 时，磁盘上只放得下一个，
// status 一直显示另一个改了、add 又加不进去。这时提交应该当成没有改动，不能报 git commit 失败（模板升级前要先提交）
func TestCommitCaseOnlyDuplicate(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "probe"), nil, 0o644)
	if _, err := os.Stat(filepath.Join(dir, "PROBE")); err != nil {
		t.Skip("文件系统区分大小写，不会出现这种情况")
	}
	os.Remove(filepath.Join(dir, "probe"))
	if err := Ensure(dir); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(dir, "pages"), 0o755)
	os.WriteFile(filepath.Join(dir, "pages", "index.tsx"), []byte("new"), 0o644)
	if _, err := Commit(dir, "页面"); err != nil {
		t.Fatal(err)
	}
	// 用底层命令塞进一个只差大小写、内容不同的 pages/Index.tsx（老项目从别的模板改过来时就是这样）
	cmd := exec.Command("git", "-C", dir, "hash-object", "-w", "--stdin")
	cmd.Stdin = strings.NewReader("old")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := git(dir, "update-index", "--add", "--cacheinfo", "100644,"+strings.TrimSpace(string(out))+",pages/Index.tsx"); err != nil {
		t.Fatal(err)
	}
	if _, err := git(dir, "commit", "-q", "-m", "两个只差大小写的文件"); err != nil {
		t.Fatal(err)
	}
	if st, _ := git(dir, "status", "--porcelain"); !strings.Contains(st, "Index.tsx") {
		t.Fatalf("应该显示 Index.tsx 改了：%q", st)
	}
	ok, err := Commit(dir, "升级前的改动")
	if err != nil || ok {
		t.Fatalf("只差大小写的文件不该让提交失败：ok=%v err=%v", ok, err)
	}
}
