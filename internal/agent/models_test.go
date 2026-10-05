package agent

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/annulo/annulo/internal/config"
	"github.com/annulo/annulo/internal/creght"
)

func TestCheapestCreght(t *testing.T) {
	cg := func(id string, p *config.Pricing) config.LLM {
		return config.LLM{ID: creghtPrefix + id, Provider: config.CreghtProvider, Model: id, Pricing: p}
	}
	own := config.LLM{ID: "m1", Provider: "p1", Model: "mine"}

	// 平台没给价格：用平台排的第一个
	mix := config.DefaultTokenMix
	if m, _ := cheapestCreght([]config.LLM{cg("a", nil), cg("b", nil), own}, mix); m.Model != "a" {
		t.Fatalf("没价格应取第一个，得到 %s", m.Model)
	}
	// cn 上的真实价格，按默认构成（缓存为主）应该选 luna，不是输出最便宜的 glm
	sol := &config.Pricing{Input: 1520, CachedInput: 152, Output: 7600}
	luna := &config.Pricing{Input: 100, CachedInput: 10, Output: 500}
	glm := &config.Pricing{Input: 90, CachedInput: 18, Output: 300}
	ds := &config.Pricing{Input: 300, CachedInput: 6, Output: 600}
	if m, _ := cheapestCreght([]config.LLM{cg("sol", sol), cg("ds", ds), cg("luna", luna), cg("glm", glm)}, mix); m.Model != "luna" {
		t.Fatalf("应取 luna，得到 %s", m.Model)
	}
	// 输出占比大的用法（比如只写长文），glm 更便宜
	if m, _ := cheapestCreght([]config.LLM{cg("luna", luna), cg("glm", glm)}, config.TokenMix{Cached: 0.3, Input: 0.2, Output: 0.5}); m.Model != "glm" {
		t.Fatalf("输出为主时应取 glm，得到 %s", m.Model)
	}
	c := sol
	// 有价格的优先于没价格的
	if m, _ := cheapestCreght([]config.LLM{cg("x", nil), cg("c", c)}, mix); m.Model != "c" {
		t.Fatalf("应取有价格的 c，得到 %s", m.Model)
	}
	// 没有平台模型
	if _, ok := cheapestCreght([]config.LLM{own}, mix); ok {
		t.Fatal("没有平台模型时不该找到")
	}
}

// 平台列表里的 Anthropic 模型走 /api/p/ai/llm/v1/messages，其余走 OpenAI 兼容；认不得的协议不列
func TestCreghtModelApis(t *testing.T) {
	var apis string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apis = r.URL.Query().Get("apis")
		io.WriteString(w, `{"data":[{"id":"gpt"},{"id":"claude","api":"anthropic-messages"},{"id":"gem","api":"google-generative-ai"}]}`)
	}))
	defer srv.Close()
	t.Setenv("ANNULO_DIR", t.TempDir())
	if err := creght.StoreToken(srv.URL, "tok", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	a := &Agent{cfg: &config.Config{}}
	a.SetCreghtHost(srv.URL)
	list, err := a.RefreshCreghtModels(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if apis != "openai-completions,anthropic-messages" {
		t.Fatalf("apis 参数不对：%q", apis)
	}
	if len(list) != 2 {
		t.Fatalf("应只列 2 个模型，得到 %d", len(list))
	}
	want := map[string][2]string{
		"gpt":    {"openai-completions", srv.URL + "/api/p/ai/llm/v1"},
		"claude": {"anthropic-messages", srv.URL + "/api/p/ai/llm"},
	}
	for _, m := range list {
		l, err := a.Resolve(m)
		if err != nil {
			t.Fatal(err)
		}
		if w := want[m.Model]; l.Api != w[0] || l.BaseURL != w[1] || l.Key() != "tok" {
			t.Fatalf("%s 解析成 %s %s", m.Model, l.Api, l.BaseURL)
		}
	}
	// 界面测试连接只传 id，也要按列表认出协议
	if l, _ := a.Resolve(config.LLM{ID: creghtPrefix + "claude", Provider: config.CreghtProvider, Model: "claude"}); l.Api != "anthropic-messages" {
		t.Fatalf("只传 id 时协议应是 anthropic-messages，得到 %s", l.Api)
	}
}

// 关掉服务商：它的模型不出现，当前模型在里面就换成别的；关完就没模型可用时不让关；再打开就回来
func TestDisableProvider(t *testing.T) {
	a := &Agent{cfg: &config.Config{Dir: t.TempDir(),
		Providers: []config.Provider{{ID: "p1", Name: "mine"}},
		Models:    []config.LLM{{ID: "m1", Provider: "p1", Model: "mine"}},
		Active:    creghtPrefix + "luna"}}
	a.creght.list = []config.LLM{{ID: creghtPrefix + "luna", Provider: config.CreghtProvider, Model: "luna"}}
	has := func(id string) bool {
		for _, m := range a.ModelSettings().Models {
			if m.ID == id {
				return true
			}
		}
		return false
	}
	if err := a.SetProviderEnabled(config.CreghtProvider, false); err != nil {
		t.Fatal(err)
	}
	if has(creghtPrefix+"luna") || a.ModelSettings().Active != "m1" {
		t.Fatalf("关掉 creght 后：active=%s", a.ModelSettings().Active)
	}
	if err := a.SetActiveModel(creghtPrefix + "luna"); err == nil {
		t.Error("关掉的服务商的模型不能选")
	}
	// 再关 p1 就没模型可用了（本机装了 claude / codex 时还有它们的，这一步跳过）
	if len(cliModels()) == 0 {
		if err := a.SetProviderEnabled("p1", false); err == nil || !has("m1") {
			t.Fatalf("全关了应该拒绝，且不改配置：%v", err)
		}
	}
	if err := a.SetProviderEnabled("nope", false); err == nil {
		t.Error("不认识的服务商")
	}
	// 重新打开：模型回来，之前选的 luna（配置里没动）又是当前模型
	if err := a.SetProviderEnabled(config.CreghtProvider, true); err != nil || !has(creghtPrefix+"luna") || a.ModelSettings().Active != creghtPrefix+"luna" {
		t.Fatalf("重新打开：%v active=%s", err, a.ModelSettings().Active)
	}
}
