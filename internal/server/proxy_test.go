package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// 代理只把界面语言 cookie 带给 creght 预览，其余 cookie、本地凭据都不带
func TestProxyForwardsOnlyLocaleCookie(t *testing.T) {
	var got http.Header
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = r.Header.Clone() }))
	defer up.Close()
	p := newProxy(up.URL)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Cookie", "session=secret; CREGHT_LOCALE=en")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Authorization", "Bearer x")
	p.ServeHTTP(httptest.NewRecorder(), req)
	if c := got.Get("Cookie"); c != "CREGHT_LOCALE=en" {
		t.Errorf("Cookie = %q，只应该有 CREGHT_LOCALE", c)
	}
	if got.Get("Accept-Language") != "en-US,en;q=0.9" || got.Get("Authorization") != "" {
		t.Errorf("Accept-Language 要转发、Authorization 不能带：%v", got)
	}

	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Cookie", "CREGHT_LOCALE=<script>")
	p.ServeHTTP(httptest.NewRecorder(), req)
	if c := got.Get("Cookie"); c != "" {
		t.Errorf("不认识的语言值不转发：%q", c)
	}
}
