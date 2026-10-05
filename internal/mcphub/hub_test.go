package mcphub

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

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

func TestEnsureServer(t *testing.T) {
	h := New(t.TempDir())
	os.WriteFile(h.File(), []byte(`{"mcpServers":{"a":{"command":"x","disabled":true}},"keep":1}`), 0o600)
	if err := h.EnsureServer("creght", ServerConfig{Type: "http", URL: "https://c/api/mcp", Disabled: true}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(h.File())
	var c map[string]any
	json.Unmarshal(b, &c)
	srv := c["mcpServers"].(map[string]any)
	if srv["creght"].(map[string]any)["url"] != "https://c/api/mcp" || srv["a"] == nil || c["keep"] == nil {
		t.Fatalf("应该加上 creght、保留别的：%s", b)
	}
	// 已经有了（用户改过）就不动
	os.WriteFile(h.File(), []byte(`{"mcpServers":{"creght":{"type":"http","url":"https://mine","disabled":true}}}`), 0o600)
	h.EnsureServer("creght", ServerConfig{Type: "http", URL: "https://c/api/mcp"})
	if b, _ := os.ReadFile(h.File()); !strings.Contains(string(b), "https://mine") {
		t.Fatalf("不该覆盖用户的配置：%s", b)
	}
}
