package agent

import (
	"slices"
	"testing"

	"github.com/annulo/annulo/internal/config"
)

// 思考档位：资料里查得到的按资料（带厂商前缀也认），用户自己配的优先，查不到按通用的，本机 agent 只有 低 / 中 / 高
func TestModelThinking(t *testing.T) {
	cases := []struct {
		l      config.LLM
		levels []string
		source string
	}{
		{config.LLM{Model: "deepseek-v4.1-flash", Api: "openai-completions", Reasoning: true}, []string{"low", "high", "max"}, ThinkingCatalog},
		{config.LLM{Model: "deepseek/deepseek-v4.1-flash", Api: "openai-completions", Reasoning: true}, []string{"low", "high", "max"}, ThinkingCatalog},
		{config.LLM{Model: "gpt-5.5", Api: "openai-responses", Reasoning: true}, []string{"off", "low", "medium", "high", "xhigh"}, ThinkingCatalog},
		{config.LLM{Model: "my-own-model", Api: "openai-completions", Reasoning: true}, []string{"off", "low", "medium", "high"}, ThinkingDefault},
		{config.LLM{Model: "my-own-model", Api: "openai-completions", Reasoning: true, ThinkingLevels: map[string]string{"low": "", "high": "hard"}}, []string{"low", "high"}, ThinkingCustom},
		{config.LLM{Model: "gpt-5.5", Api: "openai-responses"}, []string{"off"}, ThinkingCatalog},
		{config.LLM{Model: "sonnet", Provider: EngineClaude}, []string{"low", "medium", "high"}, ThinkingAgent},
	}
	for _, c := range cases {
		got := ModelThinking(c.l)
		if !slices.Equal(got.Levels, c.levels) || (c.l.Reasoning && got.Source != c.source) {
			t.Errorf("%s：档位 %v 来源 %s，想要 %v %s", c.l.Model, got.Levels, got.Source, c.levels, c.source)
		}
	}
	// 选的档模型没有：用最接近的；自己配的值原样发
	ds := config.LLM{Model: "deepseek-v4.1-flash", Api: "openai-completions", Reasoning: true}
	if e := EffectiveThinking(ds, "medium"); e != "low" && e != "high" {
		t.Errorf("deepseek 选中：%s", e)
	}
	if e := EffectiveThinking(config.LLM{Model: "x", Provider: EngineCodex}, "max"); e != "high" {
		t.Errorf("本机 agent 选最高：%s", e)
	}
	own := config.LLM{Model: "my-own-model", Api: "openai-completions", Reasoning: true, ThinkingLevels: map[string]string{"high": "hard"}}
	m, _, _ := modelFor(config.LLM{Model: own.Model, Api: own.Api, BaseURL: "https://x", APIKey: "k", Reasoning: true, ThinkingLevels: own.ThinkingLevels})
	if v := m.ThinkingLevelMap["high"]; v == nil || *v != "hard" {
		t.Errorf("自己配的值没带上：%v", m.ThinkingLevelMap)
	}
	if v, ok := m.ThinkingLevelMap["low"]; !ok || v != nil {
		t.Error("没配的档要标成不支持")
	}
}

// 对话固定的思考档位优先于全局设置，清掉后回到全局
func TestChatThinking(t *testing.T) {
	a := &Agent{cfg: &config.Config{Dir: t.TempDir(), Thinking: "high"}}
	a.SetChatThinking("c1", "low")
	if got, other := a.thinkingFor("c1"), a.thinkingFor("c2"); got != "low" || other != "high" {
		t.Fatalf("c1=%s c2=%s", got, other)
	}
	a.SetChatThinking("c1", "")
	if got := a.thinkingFor("c1"); got != "high" {
		t.Fatalf("清掉后应回到全局：%s", got)
	}
}
