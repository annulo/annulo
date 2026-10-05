package server

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/annulo/annulo/internal/brand"
	"github.com/annulo/annulo/internal/i18n"
)

// 助手面板上的名字、介绍和示例问题由项目决定（能力版本 27）：模板在 annulo.json / shuttle.json 里写
//
//	"assistant": { "name": {"zh": "运营助手", "en": "Ops assistant"}, "intro": {...}, "suggestions": {"zh": [...], "en": [...]} }
//
// 每项都能直接写字符串（不分语言）。没写的用通用的说法：Annulo 本身不认识任何行业。

type assistantInfo struct {
	Name        string   `json:"name"`
	Intro       string   `json:"intro"`
	Suggestions []string `json:"suggestions"`
}

// 项目没给助手起名字时 Name 留空：界面用「你的助手」这类通用说法，不把「Assistant」当名字用（英文里「Hi, I'm Assistant」读着别扭）
func defaultAssistant() assistantInfo {
	return assistantInfo{
		Intro: i18n.T("告诉我你想要什么工具，我在这个项目里把它做出来：页面、数据表、脚本、定时任务。做好的东西都留在这里，按钮点了直接跑。", "Tell me what tool you need and I'll build it in this project: pages, tables, scripts, schedules. What I build stays here, and its buttons run right away."),
		Suggestions: []string{
			i18n.T("这个项目现在有什么？", "What's in this project right now?"),
			i18n.T("做一个记录客户跟进的页面，能按状态筛选", "Make a page for tracking customer follow-ups, filterable by status"),
			i18n.T("每天早上 9 点抓取几个网站的标题，存进表里", "Every morning at 9, fetch the headlines of a few sites and save them to a table"),
		},
	}
}

// localizedText 取当前界面语言的那一项：字符串直接用，{zh, en} 按语言取、缺了用另一个。
func localizedText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var m map[string]string
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	for _, k := range []string{i18n.T("zh", "en"), i18n.T("en", "zh"), "zh-CN"} {
		if v := m[k]; v != "" {
			return v
		}
	}
	return ""
}

func localizedList(raw json.RawMessage) []string {
	var l []string
	if json.Unmarshal(raw, &l) == nil {
		return l
	}
	var m map[string][]string
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	if v := m[i18n.T("zh", "en")]; len(v) > 0 {
		return v
	}
	return m[i18n.T("en", "zh")]
}

// projectAssistant 读项目声明的助手信息，没写的项用通用的。
func projectAssistant(dir string) assistantInfo {
	out := defaultAssistant()
	for _, f := range brand.ProjectFiles {
		b, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			continue
		}
		var v struct {
			Assistant *struct {
				Name        json.RawMessage `json:"name"`
				Intro       json.RawMessage `json:"intro"`
				Suggestions json.RawMessage `json:"suggestions"`
			} `json:"assistant"`
		}
		if json.Unmarshal(b, &v) != nil || v.Assistant == nil {
			continue
		}
		if n := localizedText(v.Assistant.Name); n != "" {
			out.Name = n
		}
		if n := localizedText(v.Assistant.Intro); n != "" {
			out.Intro = n
		}
		if l := localizedList(v.Assistant.Suggestions); len(l) > 0 {
			out.Suggestions = l
		}
		break
	}
	return out
}
