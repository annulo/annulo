package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/annulo/annulo/internal/config"
	"github.com/annulo/annulo/internal/creght"
)

func TestLocalFiles(t *testing.T) {
	s := newTestServer(&config.Config{Dir: t.TempDir()}, &creght.Workspace{ProjectID: "p1"}, nil)

	req := httptest.NewRequest(http.MethodPost, "/_shuttle/api/local/files", strings.NewReader("0123456789"))
	req.Header.Set("X-Filename", "%E6%88%91%E7%9A%84%E8%A7%86%E9%A2%91.MP4")
	req.Header.Set("Content-Type", "video/mp4")
	w := httptest.NewRecorder()
	s.apiLocalFileUpload(w, req)
	var got struct {
		Ref, URL, Name, Filename string
		Size                     int64
	}
	json.Unmarshal(w.Body.Bytes(), &got)
	if w.Code != 200 || !strings.HasPrefix(got.Ref, "local:") || !strings.HasSuffix(got.Name, ".mp4") || got.Filename != "我的视频.MP4" || got.Size != 10 {
		t.Fatalf("上传：%d %s", w.Code, w.Body)
	}

	// 预览支持 Range
	req = httptest.NewRequest(http.MethodGet, got.URL, nil)
	req.Header.Set("Range", "bytes=2-4")
	w = httptest.NewRecorder()
	s.handleLocalFile(w, req)
	if b, _ := io.ReadAll(w.Body); w.Code != http.StatusPartialContent || string(b) != "234" {
		t.Fatalf("预览：%d %q", w.Code, b)
	}
	// 猜路径、穿目录都不行
	for _, bad := range []string{"/_shuttle/files/../x", "/_shuttle/files/abc.mp4"} {
		w = httptest.NewRecorder()
		s.handleLocalFile(w, httptest.NewRequest(http.MethodGet, bad, nil))
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s 应该 404：%d", bad, w.Code)
		}
	}

	w = httptest.NewRecorder()
	s.apiLocalFileDelete(w, got.Name)
	if _, err := os.Stat(filepath.Join(s.localFilesDir(), got.Name)); w.Code != 200 || !os.IsNotExist(err) {
		t.Fatalf("删除：%d %v", w.Code, err)
	}

}

// 超过 90 天的文件清掉，但表里引用着的留下；读表出错就一个都不删
func TestPruneLocalFiles(t *testing.T) {
	dir := t.TempDir()
	put := func(name string, age time.Duration) string {
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte("x"), 0o600)
		os.Chtimes(p, time.Now().Add(-age), time.Now().Add(-age))
		return p
	}
	day := 24 * time.Hour
	oldFree := put("00000000000000000000000000000001.mp4", 100*day)
	oldUsed := put("00000000000000000000000000000002.mp4", 100*day)
	fresh := put("00000000000000000000000000000003.mp4", day)
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }

	pruneLocalFiles(dir, maxLocalFileAge, func() (map[string]bool, error) { return nil, errors.New("表读不了") })
	if !exists(oldFree) || !exists(oldUsed) {
		t.Fatal("读表出错时不该删")
	}
	called := false
	pruneLocalFiles(dir, maxLocalFileAge, func() (map[string]bool, error) {
		called = true
		return map[string]bool{"00000000000000000000000000000002.mp4": true}, nil
	})
	if !called || exists(oldFree) || !exists(oldUsed) || !exists(fresh) {
		t.Fatalf("没引用的旧文件删掉，引用的、新的留下：%v %v %v", exists(oldFree), exists(oldUsed), exists(fresh))
	}
	// 没有该删的就不读表
	pruneLocalFiles(dir, 1000*day, func() (map[string]bool, error) { t.Fatal("不该读表"); return nil, nil })

	// 表里写的 local:<name> 和预览地址都算引用
	b := []byte(`{"video":"local:00000000000000000000000000000004.mp4","body":"<video src=\"/_annulo/files/00000000000000000000000000000005\">"}`)
	var got []string
	for _, m := range localFileRefRe.FindAllSubmatch(b, -1) {
		got = append(got, string(m[1]))
	}
	if strings.Join(got, ",") != "00000000000000000000000000000004.mp4,00000000000000000000000000000005" {
		t.Fatal(got)
	}
}

// 本机文件带 CSP sandbox（网页直接打开也拿不到项目源）；local/open 认 local:<name>，不开可执行的，服务端部署时给页面一个地址
func TestLocalFileSandboxAndOpen(t *testing.T) {
	s := newTestServer(&config.Config{Dir: t.TempDir()}, &creght.Workspace{ProjectID: "p1"}, nil)
	dir := s.localFilesDir()
	os.MkdirAll(dir, 0o700)
	name := "0123456789abcdef0123456789abcdef.html"
	os.WriteFile(filepath.Join(dir, name), []byte("<script>1</script>"), 0o600)
	os.WriteFile(filepath.Join(dir, "0123456789abcdef0123456789abcde0.sh"), []byte("echo"), 0o600)

	w := httptest.NewRecorder()
	s.handleLocalFile(w, httptest.NewRequest(http.MethodGet, localFilesURL+name, nil))
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("要带 sandbox：%d %q", w.Code, w.Header().Get("Content-Security-Policy"))
	}
	for _, bad := range []string{"local:0123456789abcdef0123456789abcde0.sh", "local:../x.html", "local:0123456789abcdef0123456789abcdee.html"} {
		w = httptest.NewRecorder()
		s.openLocalFile(w, strings.TrimPrefix(bad, "local:"), false)
		if w.Code == 200 {
			t.Fatalf("%s 不该打开", bad)
		}
	}
	s.cfg.Server = true
	w = httptest.NewRecorder()
	s.openLocalFile(w, name, false)
	if !strings.Contains(w.Body.String(), `"open_url":"/_shuttle/files/`+name) {
		t.Fatalf("服务端部署要给地址：%s", w.Body)
	}
}

// 写表后合并一下再告诉页面：同一批里几张表一起发，页面自己写的不算 others
func TestDataChanged(t *testing.T) {
	s := newTestServer(&config.Config{Dir: t.TempDir()}, &creght.Workspace{ProjectID: "p1"}, nil)
	s.dataChanged(byPage(context.Background()), "assets")
	s.dataChanged(byPage(context.Background()), "articles")
	s.changes.mu.Lock()
	got, others, armed := len(s.changes.tables), s.changes.others, s.changes.timer != nil
	s.changes.mu.Unlock()
	if got != 2 || others || !armed {
		t.Fatalf("合并：%d 张表 others=%v timer=%v", got, others, armed)
	}
	s.dataChanged(context.Background(), "assets")
	s.changes.mu.Lock()
	others = s.changes.others
	s.changes.mu.Unlock()
	if !others {
		t.Fatal("不是页面写的要标 others")
	}
	s.flushDataChanges()
	if s.changes.tables != nil || s.changes.timer != nil {
		t.Fatal("发完要清空")
	}
}
