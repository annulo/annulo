package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/annulo/annulo/internal/config"
)

// 产品改名 Annulo 的兼容层（internal/brand）：/_annulo/ 前缀和 X-Annulo 头和旧名字一样管用。
func TestAnnuloPrefixAndHeader(t *testing.T) {
	s := &Server{cfg: &config.Config{Port: 7799}}
	var seen string
	mux := http.NewServeMux()
	mux.HandleFunc("/_shuttle/api/", func(w http.ResponseWriter, r *http.Request) { seen = r.URL.Path })
	h := canonicalPaths(s.guard(mux))

	for _, c := range []struct {
		path, header string
		code         int
	}{
		{"/_shuttle/api/status", "X-Shuttle", 200},
		{"/_annulo/api/status", "X-Shuttle", 200},
		{"/_annulo/api/status", "X-Annulo", 200},
		{"/_shuttle/api/status", "X-Annulo", 200},
		{"/_annulo/api/status", "", 403}, // 换前缀不能绕过门卫
	} {
		seen = ""
		r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:7799"+c.path, nil)
		if c.header != "" {
			r.Header.Set(c.header, "1")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != c.code {
			t.Errorf("%s %s：%d，想要 %d", c.path, c.header, w.Code, c.code)
		}
		if c.code == 200 && seen != "/_shuttle/api/status" {
			t.Errorf("%s 到的路由：%q", c.path, seen)
		}
	}
}
