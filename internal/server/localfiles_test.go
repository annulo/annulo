package server

import (
	"encoding/json"
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
	s := &Server{cfg: &config.Config{Dir: t.TempDir()}, ws: &creght.Workspace{ProjectID: "p1"}}

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

	// 太旧的文件在下次上传时清掉
	old := filepath.Join(s.localFilesDir(), "0123456789abcdef0123456789abcdef.mp4")
	os.WriteFile(old, []byte("x"), 0o600)
	os.Chtimes(old, time.Now().Add(-100*24*time.Hour), time.Now().Add(-100*24*time.Hour))
	s.apiLocalFileUpload(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/_shuttle/api/local/files", strings.NewReader("y")))
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("超过 90 天的文件应该被清掉")
	}
}
