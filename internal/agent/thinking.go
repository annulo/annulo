package agent

import (
	"encoding/json"
	"slices"
	"sort"
	"strings"

	"github.com/annulo/annulo/internal/config"
	"github.com/sky-valley/pi/ai"
)

// 思考档位：每个模型支持的档不一样，每档发给接口的值也不一样（比如 deepseek-v4.1-flash 只有 低 / 高 / 最高，
// gpt-5.5 的「关」要发 none，kimi-k2.7-code 关不掉）。档位从哪来，按顺序：
//   - custom：用户在模型编辑里自己配的（config.LLM.ThinkingLevels）；
//   - catalog：pi 自带的模型资料（models_catalog.json）按模型 id 查到的，带厂商前缀的 id 也认；
//   - default：都没有，按通用的 关 / 低 / 中 / 高 发原值。
// 全局的思考强度是用户选的档；当前模型没有这档时，pi 按最接近的支持档发（ai.ClampThinkingLevel），界面也显示那一档。

// ThinkingSource 是档位的来源：custom / catalog / default（本机 agent 是 agent）
const (
	ThinkingCustom  = "custom"
	ThinkingCatalog = "catalog"
	ThinkingDefault = "default"
	ThinkingAgent   = "agent"
)

// 官方厂商在 pi 资料里的 provider 名：同一个模型在好几个渠道都有资料时，优先用官方的
var officialProviders = []string{"deepseek", "openai", "anthropic", "moonshotai", "moonshotai-cn", "google", "xai", "zai", "mistral", "minimax", "alibaba"}

// normModelID：去掉厂商前缀（deepseek/deepseek-v4.1-flash、@cf/…）和 :batch 这类后缀，小写
func normModelID(id string) string {
	id = strings.ToLower(strings.TrimSpace(id))
	if i := strings.LastIndex(id, "/"); i >= 0 {
		id = id[i+1:]
	}
	if i := strings.Index(id, ":"); i >= 0 {
		id = id[:i]
	}
	return id
}

// catalogModel 在 pi 的模型资料里按 id 找这个模型：官方厂商的优先，其次参数格式和我们要发的一样的（format），再按 provider 名排。
func catalogModel(id, format string) *ai.Model {
	want := normModelID(id)
	if want == "" {
		return nil
	}
	providers := ai.GetProviders()
	sort.Strings(providers)
	var best *ai.Model
	bestScore := -1
	for _, p := range providers {
		for _, m := range ai.GetModels(p) {
			if normModelID(m.ID) != want {
				continue
			}
			score := 1
			if slices.Contains(officialProviders, p) {
				score = 3
			} else if format != "" && thinkingFormatOf(m.Compat) == format {
				score = 2
			}
			if score > bestScore || (score == bestScore && best != nil && m.ID < best.ID) {
				best, bestScore = m, score
			}
		}
	}
	return best
}

func thinkingFormatOf(compat []byte) string {
	if len(compat) == 0 {
		return ""
	}
	var c struct {
		ThinkingFormat string `json:"thinkingFormat"`
	}
	_ = json.Unmarshal(compat, &c)
	return c.ThinkingFormat
}

// thinkingMap：这个模型每档发什么（nil 值 = 不支持这档）和来源。
func thinkingMap(l config.LLM) (ai.ThinkingLevelMap, string) {
	if len(l.ThinkingLevels) > 0 {
		m := ai.ThinkingLevelMap{}
		for _, lv := range config.ThinkingLevels {
			if v, ok := l.ThinkingLevels[lv]; ok {
				if v == "" {
					v = lv
				}
				m[ai.ModelThinkingLevel(lv)] = &v
			} else {
				m[ai.ModelThinkingLevel(lv)] = nil
			}
		}
		return m, ThinkingCustom
	}
	if c := catalogModel(l.Model, thinkingFormatOf(compatFor(l))); c != nil && c.Reasoning {
		return c.ThinkingLevelMap, ThinkingCatalog
	}
	// 通用：关 / 低 / 中 / 高，发原值（「最低」很多接口不认，不给）
	return ai.ThinkingLevelMap{"minimal": nil}, ThinkingDefault
}

// CatalogDefaults：新加模型时按资料预填的上下文窗口、能不能看图、支不支持思考；资料里没有返回 ok=false
func CatalogDefaults(l config.LLM) (window int, images, reasoning, ok bool) {
	c := catalogModel(l.Model, thinkingFormatOf(compatFor(l)))
	if c == nil {
		return 0, false, false, false
	}
	return c.ContextWindow, slices.Contains(c.Input, "image"), c.Reasoning, true
}

// ThinkingInfo 给界面：这个模型支持的档（从低到高）、来源、用户自己配的（编辑用）
type ThinkingInfo struct {
	Levels []string          `json:"thinking_levels"`
	Source string            `json:"thinking_source"`
	Custom map[string]string `json:"thinking_custom,omitempty"`
}

// cliThinkingLevels：本机 agent 的 --effort / model_reasoning_effort 只认这几档
var cliThinkingLevels = []string{"low", "medium", "high"}

func ModelThinking(l config.LLM) ThinkingInfo {
	if IsCLIProvider(l.Provider) {
		return ThinkingInfo{Levels: cliThinkingLevels, Source: ThinkingAgent}
	}
	tm, src := thinkingMap(l)
	info := ThinkingInfo{Source: src, Custom: l.ThinkingLevels, Levels: []string{}}
	for _, lv := range ai.GetSupportedThinkingLevels(&ai.Model{Reasoning: l.Reasoning, ThinkingLevelMap: tm}) {
		info.Levels = append(info.Levels, string(lv))
	}
	return info
}

// EffectiveThinking：选的档在这个模型上实际发哪一档（没有这档就用最接近的）
func EffectiveThinking(l config.LLM, level string) string {
	if IsCLIProvider(l.Provider) {
		return clampCLIThinking(level)
	}
	tm, _ := thinkingMap(l)
	return string(ai.ClampThinkingLevel(&ai.Model{Reasoning: l.Reasoning, ThinkingLevelMap: tm}, ai.ModelThinkingLevel(level)))
}

// clampCLIThinking：本机 agent 只有 低 / 中 / 高
func clampCLIThinking(level string) string {
	switch level {
	case "off", "minimal", "low":
		return "low"
	case "xhigh", "max", "high":
		return "high"
	}
	return "medium"
}
