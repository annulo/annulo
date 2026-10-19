package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDomainManager(t *testing.T) {
	d := newDomainManager(t.TempDir())
	for _, bad := range []string{"", "http://a.com", "a.com/x", "localhost", "a_b.com", "1.2.3.4"} {
		if err := d.bind(context.Background(), bad, "", true); err == nil {
			t.Errorf("%q 应该被拒", bad)
		}
	}
	if st := d.status(); st["status"] != "none" || st["domain"] != "" {
		t.Fatalf("没绑时状态不对：%v", st)
	}
	// 自检：HTTP 监听认自己发的一次性 token，别的 404；没绑域名时不跳 https
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("app")) })
	h := d.HTTPHandler(next)
	d.checks["tok123"] = time.Now()
	for path, want := range map[string]string{"/_annulo/domain-check/tok123": "tok123", "/_shuttle/domain-check/tok123": "tok123", "/_annulo/domain-check/other": "404 page not found\n", "/": "app"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "http://1.2.3.4"+path, nil))
		if w.Body.String() != want {
			t.Errorf("%s → %q，要 %q", path, w.Body.String(), want)
		}
	}
}
