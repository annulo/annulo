package creght

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// 自己部署的 creght：管理员令牌存下来就能用（不过期、不刷新），能力清单里没有的就当没有
func TestSelfhost(t *testing.T) {
	t.Setenv("ANNULO_DIR", t.TempDir())
	t.Setenv("SHUTTLE_DIR", "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tr/system_info":
			w.Write([]byte(`{"data":{"deployment":"selfhost","api_version":1,"capabilities":{"cms":{"version":1},"table":{"version":1},"mcp":{"version":1}}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	if Connected(srv.URL) {
		t.Fatal("还没存令牌")
	}
	if err := StoreStaticToken(srv.URL, "admin-token"); err != nil {
		t.Fatal(err)
	}
	if tok, err := ReadToken(srv.URL); err != nil || tok != "admin-token" || !Connected(srv.URL) {
		t.Fatalf("静态令牌要原样读回来：%q %v", tok, err)
	}
	if !Has(srv.URL, "cms") || Has(srv.URL, "templates") || Has(srv.URL, "llm") {
		t.Fatal("能力按清单里的 key 判断")
	}
	if !Has("http://127.0.0.1:1", "templates") {
		t.Fatal("读不到清单的当作有（老平台）")
	}
	Disconnect(srv.URL)
	if Connected(srv.URL) {
		t.Fatal("断开后不算连着")
	}

	SetCustom(map[string]string{srv.URL: "我的 creght"})
	if c := ClusterOf(srv.URL); c == nil || !c.Custom || c.Name() != "我的 creght" {
		t.Fatalf("自己部署的地址要算一个集群：%+v", c)
	}
	SetCustom(nil)
}
