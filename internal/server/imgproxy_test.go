package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/annulo/annulo/internal/config"
)

func TestImageProxy(t *testing.T) {
	t.Setenv("SHUTTLE_FETCH_ALLOW_PRIVATE", "1")
	var hits atomic.Int32
	var gotReferer atomic.Value
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		gotReferer.Store(r.Header.Get("Referer"))
		// 防盗链：带了 Referer 就拒
		if r.Header.Get("Referer") != "" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/a.jpg":
			w.Header().Set("content-type", "image/jpeg")
			w.Write([]byte("jpeg-bytes"))
		default:
			w.Header().Set("content-type", "text/html")
			w.Write([]byte("<html>"))
		}
	}))
	defer origin.Close()

	s := &Server{cfg: &config.Config{Dir: t.TempDir()}}
	get := func(target string, h map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/_shuttle/img?url="+url.QueryEscape(target), nil)
		// 浏览器在 localhost 页面里请求图片时会带上这个 Referer，代理不能把它转发出去
		req.Header.Set("Referer", "http://127.0.0.1:7799/?view=social")
		for k, v := range h {
			req.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		s.handleImage(w, req)
		return w
	}

	w := get(origin.URL+"/a.jpg", map[string]string{"Sec-Fetch-Site": "same-origin"})
	if w.Code != 200 || w.Body.String() != "jpeg-bytes" || w.Header().Get("content-type") != "image/jpeg" {
		t.Fatalf("取图片：%d %q %s", w.Code, w.Body.String(), w.Header().Get("content-type"))
	}
	if ref, _ := gotReferer.Load().(string); ref != "" {
		t.Fatalf("不该把 Referer 转发出去：%q", ref)
	}
	// 第二次走缓存，不再请求原站
	if w := get(origin.URL+"/a.jpg", nil); w.Code != 200 || hits.Load() != 1 {
		t.Fatalf("应该走缓存：%d，原站被请求了 %d 次", w.Code, hits.Load())
	}
	// 不是图片的不给
	if w := get(origin.URL+"/page", nil); w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "不是图片") {
		t.Fatalf("非图片：%d %q", w.Code, w.Body.String())
	}
	// 别的网站发起的请求不给（不做开放代理）
	if w := get(origin.URL+"/a.jpg", map[string]string{"Sec-Fetch-Site": "cross-site"}); w.Code != http.StatusForbidden {
		t.Fatalf("跨站请求应该被拒：%d", w.Code)
	}
	for _, bad := range []string{"file:///etc/passwd", "javascript:alert(1)", ""} {
		if w := get(bad, nil); w.Code != http.StatusBadRequest {
			t.Fatalf("%q 应该被拒：%d", bad, w.Code)
		}
	}
	// 内网地址：没放开时拒绝
	t.Setenv("SHUTTLE_FETCH_ALLOW_PRIVATE", "")
	if w := get(origin.URL+"/b.jpg", nil); w.Code != http.StatusBadGateway {
		t.Fatalf("内网地址应该被拒：%d %q", w.Code, w.Body.String())
	}
}
