package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/annulo/annulo/internal/config"
)

const testAsset = "0123456789abcdef0123456789abcdef.png"

// 离线项目上传：地址不带主机和端口，同源直接能取
func TestOfflineAssetPath(t *testing.T) {
	s := &Server{cfg: &config.Config{Dir: t.TempDir(), Host: "127.0.0.1", Port: 7799}}
	a, err := s.saveOfflineAsset("cover.png", "image/png", []byte("png-bytes"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(a.URL, "/_annulo/uploaded/") || a.URL != a.Path || !strings.HasSuffix(a.URL, ".png") {
		t.Fatalf("地址应该是 /_annulo/uploaded/…：%+v", a)
	}
	mux := http.NewServeMux()
	mux.HandleFunc(assetsURL, s.handleAsset)
	w := httptest.NewRecorder()
	canonicalPaths(mux).ServeHTTP(w, httptest.NewRequest("GET", a.URL, nil))
	if w.Code != 200 || w.Body.String() != "png-bytes" {
		t.Fatalf("取上传的文件：%d %q", w.Code, w.Body.String())
	}
	if again, _ := s.saveOfflineAsset("other-name.png", "image/png", []byte("png-bytes")); again.URL != a.URL || !again.Existed {
		t.Fatalf("同样的内容应该是同一个地址：%+v", again)
	}
}

func TestLocalAssetFile(t *testing.T) {
	s := &Server{cfg: &config.Config{Dir: t.TempDir()}}
	want := filepath.Join(s.assetsDir(), testAsset)
	for _, ok := range []string{
		"/_annulo/uploaded/" + testAsset,
		"/_shuttle/uploaded/" + testAsset,
		"http://127.0.0.1:7799/_shuttle/uploaded/" + testAsset, // 老数据：带端口
		"http://localhost:7800/_annulo/uploaded/" + testAsset,
	} {
		if got := s.localAssetFile(ok); got != want {
			t.Errorf("%s → %q", ok, got)
		}
	}
	for _, bad := range []string{
		"/_annulo/uploaded/../config.json",
		"https://example.com/_annulo/uploaded/" + testAsset,
		"/_annulo/uploaded/" + testAsset + "?x=1",
		"x/_annulo/uploaded/" + testAsset,
	} {
		if got := s.localAssetFile(bad); got != "" {
			t.Errorf("%s 不该认：%q", bad, got)
		}
	}
}

// 转在线：新旧写法都换成线上地址，同一个文件只传一次；找不到、传失败的原样留着并列出来
func TestUploadLocalAssets(t *testing.T) {
	s := &Server{cfg: &config.Config{Dir: t.TempDir()}}
	os.MkdirAll(s.assetsDir(), 0o700)
	os.WriteFile(filepath.Join(s.assetsDir(), testAsset), []byte("png"), 0o600)
	broken := "fedcba9876543210fedcba9876543210.jpg"
	os.WriteFile(filepath.Join(s.assetsDir(), broken), []byte("jpg"), 0o600)
	missing := "00000000000000000000000000000000.png"

	oldRef := "http://127.0.0.1:7799/_shuttle/uploaded/" + testAsset
	newRef := "/_annulo/uploaded/" + testAsset
	bodies := [][2]string{
		{"articles", `{"body":"<img src=\"` + newRef + `\"><img src=\"` + oldRef + `\">"}`},
		{"assets", `{"url":"` + newRef + `"}`},
		{"assets", `{"url":"/_annulo/uploaded/` + broken + `"}`},
		{"assets", `{"url":"/_annulo/uploaded/` + missing + `"}`},
	}
	calls := 0
	pairs, failed := s.uploadLocalAssets(bodies, func(name string, b []byte) (string, error) {
		calls++
		if name == broken {
			return "", errors.New("boom")
		}
		return "https://cdn.example.com/" + name, nil
	})
	if calls != 2 {
		t.Fatalf("应该传 2 次（同一个文件只传一次，找不到的不传）：%d", calls)
	}
	rep := strings.NewReplacer(pairs...)
	got := rep.Replace(bodies[0][1])
	if strings.Contains(got, "uploaded") || strings.Count(got, "https://cdn.example.com/"+testAsset) != 2 {
		t.Fatalf("新旧写法都要换掉：%s", got)
	}
	if got := rep.Replace(bodies[2][1]); !strings.Contains(got, "/_annulo/uploaded/"+broken) {
		t.Fatalf("传失败的地址要原样留着：%s", got)
	}
	if len(failed) != 2 || failed[0].Ref != "/_annulo/uploaded/"+broken || failed[1].Ref != "/_annulo/uploaded/"+missing || failed[1].Table != "assets" {
		t.Fatalf("失败列表：%+v", failed)
	}
}

// 助手在对话里升级模板：只有它自己那段在跑不算忙，别的对话在跑就算
func TestBusyExcept(t *testing.T) {
	for _, c := range []struct {
		running []string
		self    string
		want    bool
	}{
		{nil, "", false},
		{[]string{"a"}, "", true},
		{[]string{"a"}, "a", false},
		{[]string{"a", "b"}, "a", true},
		{[]string{"b"}, "a", true},
	} {
		if got := busyExcept(c.running, c.self); got != c.want {
			t.Errorf("%v / %q：%v", c.running, c.self, got)
		}
	}
}
