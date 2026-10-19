package mcphub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestSetToolsEnabled(t *testing.T) {
	h := New(t.TempDir())
	cfg := `{"mcpServers":{"n":{"type":"http","url":"https://x/mcp","headers":{"A":"b"},"extra":1}},"other":true}`
	if err := os.WriteFile(h.File(), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &server{name: "n", status: "connected", tools: []*mcp.Tool{{Name: "a"}, {Name: "b"}, {Name: "c"}}}
	h.servers["n"] = s
	names := func() (out []string) {
		for _, x := range h.Tools() {
			out = append(out, x.Name)
		}
		return
	}

	if err := h.SetToolsEnabled("n", []string{"a", "b"}, false); err != nil {
		t.Fatal(err)
	}
	if got := names(); !slices.Equal(got, []string{"mcp__n__c"}) {
		t.Fatalf("关掉 a、b 后应只剩 c，得到 %v", got)
	}
	// 重复关不产生重复项；开回 a
	h.SetToolsEnabled("n", []string{"b"}, false)
	h.SetToolsEnabled("n", []string{"a"}, true)
	if !slices.Equal(s.cfg.ExcludeTools, []string{"b"}) {
		t.Fatalf("应只排除 b，得到 %v", s.cfg.ExcludeTools)
	}

	// 文件里的其他字段保留，重新解析后 excludeTools 一致
	b, _ := os.ReadFile(h.File())
	var raw map[string]any
	json.Unmarshal(b, &raw)
	sc := raw["mcpServers"].(map[string]any)["n"].(map[string]any)
	if raw["other"] != true || sc["extra"] != float64(1) || sc["headers"] == nil {
		t.Fatalf("其他字段丢了：%s", b)
	}
	c, err := parseConfig(b)
	if err != nil || !slices.Equal(c.MCPServers["n"].ExcludeTools, []string{"b"}) {
		t.Fatalf("解析不对：%v %v", err, c.MCPServers["n"].ExcludeTools)
	}

	// 全部开回来后字段去掉
	h.SetToolsEnabled("n", []string{"b"}, true)
	b, _ = os.ReadFile(h.File())
	json.Unmarshal(b, &raw)
	if _, ok := raw["mcpServers"].(map[string]any)["n"].(map[string]any)["excludeTools"]; ok {
		t.Fatalf("全开后不该留 excludeTools：%s", b)
	}
	if err := h.SetToolsEnabled("nope", []string{"a"}, false); err == nil {
		t.Fatal("不存在的 server 应报错")
	}
}

func TestSetManaged(t *testing.T) {
	h := New(t.TempDir())
	defer h.Close()
	read := func() map[string]any {
		b, _ := os.ReadFile(h.File())
		var c map[string]any
		json.Unmarshal(b, &c)
		return c
	}
	tok := func(context.Context) (string, error) { return "t", nil }
	// 以前用户自己加的（为了躲 OAuth 停用了、改了地址）：接管过来，只留关掉的工具
	os.WriteFile(h.File(), []byte(`{"mcpServers":{"a":{"command":"x","disabled":true},"creght":{"type":"http","url":"https://old","disabled":true,"excludeTools":["t1"]}},"keep":1}`), 0o600)
	if err := h.SetManaged("creght", &Managed{URL: "https://c/api/mcp", Token: tok}); err != nil {
		t.Fatal(err)
	}
	c := read()
	cr := c["mcpServers"].(map[string]any)["creght"].(map[string]any)
	if cr["url"] != "https://c/api/mcp" || cr["managed"] != true || cr["disabled"] != nil || len(cr["excludeTools"].([]any)) != 1 || c["keep"] == nil || c["mcpServers"].(map[string]any)["a"] == nil {
		t.Fatalf("应该接管 creght、保留别的：%v", c)
	}
	// 用户停用了内置的：换集群时保留停用
	b, _ := os.ReadFile(h.File())
	h.SaveConfig([]byte(strings.Replace(string(b), `"managed": true`, `"managed": true, "disabled": true`, 1)))
	h.SetManaged("creght", &Managed{URL: "https://com/api/mcp", Token: tok})
	cr = read()["mcpServers"].(map[string]any)["creght"].(map[string]any)
	if cr["url"] != "https://com/api/mcp" || cr["disabled"] != true {
		t.Fatalf("换集群应改地址、保留停用：%v", cr)
	}
	// 在 JSON 里删掉、改地址：保存时补回来
	h.SaveConfig([]byte(`{"mcpServers":{}}`))
	if cr, _ := read()["mcpServers"].(map[string]any)["creght"].(map[string]any); cr == nil || cr["url"] != "https://com/api/mcp" {
		t.Fatalf("删掉内置的应补回来：%v", read())
	}
	if h.Logout("creght") == nil {
		t.Fatal("内置的不能退出授权")
	}
	if st := h.Status(); len(st) != 1 || !st[0].Managed {
		t.Fatalf("状态应标出内置：%+v", st)
	}
	// 断开 creght：去掉
	h.SetManaged("creght", nil)
	if _, ok := read()["mcpServers"].(map[string]any)["creght"]; ok {
		t.Fatal("断开后应去掉 creght")
	}
}

func TestManagedSendsToken(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	h := New(t.TempDir())
	h.CallbackURL = "http://localhost/cb"
	defer h.Close()
	h.SetManaged("creght", &Managed{URL: srv.URL, Token: func(context.Context) (string, error) { return "abc", nil }})
	for i := 0; i < 100; i++ {
		if st := h.Status(); len(st) == 1 && st[0].Status == "failed" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	st := h.Status()
	if got != "Bearer abc" || st[0].Status != "failed" || st[0].AuthURL != "" {
		t.Fatalf("内置的应带 token、不发起 OAuth：auth=%q status=%+v", got, st)
	}
}
