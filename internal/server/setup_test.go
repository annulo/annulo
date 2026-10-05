package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/annulo/annulo/internal/creght"
)

// stubTemplates 让模板列表不连平台：list 为 nil 表示平台读不到。
func stubTemplates(t *testing.T, list map[string][]creght.OpsTemplate) {
	old := fetchOpsTemplates
	fetchOpsTemplates = func(_ context.Context, host string) ([]creght.OpsTemplate, error) {
		if l, ok := list[host]; ok {
			return l, nil
		}
		return nil, errors.New("404")
	}
	tplCache.Lock()
	tplCache.m = nil
	tplCache.Unlock()
	// creght 的模板连着 creght 才出现：这些测试都当作连着 cn 和 com
	t.Setenv("ANNULO_DIR", t.TempDir())
	for _, h := range []string{"https://creght.cn", "https://creght.com"} {
		creght.StoreToken(h, "tok", time.Now().Add(time.Hour))
	}
	oldGit := defaultTemplateSources
	defaultTemplateSources = nil // 只看 creght 的模板，不连 GitHub
	t.Cleanup(func() {
		defaultTemplateSources = oldGit
		fetchOpsTemplates = old
		tplCache.Lock()
		tplCache.m = nil
		tplCache.Unlock()
	})
}

func TestTemplateOf(t *testing.T) {
	stubTemplates(t, nil)
	cases := []struct {
		p    creght.ProjectInfo
		want string
	}{
		{creght.ProjectInfo{ID: "a", FromProjectID: "p9ok3myl0ne6"}, "trade"},
		{creght.ProjectInfo{ID: "b", FromProjectID: "p9k3n5y5hbbm"}, "ops"}, // 通用运营后台退役了，从它复制的老项目还认得
		{creght.ProjectInfo{ID: "p9kmhz3stqqa"}, "trade"},                   // 外贸模板的来源（经通用模板）是它
		{creght.ProjectInfo{ID: "c", FromProjectID: "other"}, ""},
		{creght.ProjectInfo{ID: "p9ok3myl0ne6", FromProjectID: "p9k3n5y5hbbm"}, ""}, // 外贸模板本身不是用户的项目
	}
	for _, c := range cases {
		got := ""
		if tp := templateOf(c.p); tp != nil {
			got = tp.Key
		}
		if got != c.want {
			t.Errorf("templateOf(%+v) = %q, want %q", c.p, got, c.want)
		}
	}
	if templateByKey("https://creght.cn", "trade") == nil || templateByKey("https://creght.cn", "nope") != nil || templateByKey("https://creght.com", "trade") != nil {
		t.Error("templateByKey")
	}
	if on := templatesOn("https://creght.cn"); len(on) != 1 || on[0].Key != "trade" {
		t.Error("读不到平台时只给外贸助手（退役的通用运营后台不给选）")
	}
	if templateByKey("https://creght.cn", "ops") != nil {
		t.Error("退役的模板不能再被选来新建、换过去")
	}
}

func TestTemplatesFromPlatform(t *testing.T) {
	stubTemplates(t, map[string][]creght.OpsTemplate{
		"https://creght.cn":  {{ProjectID: "p9k3n5y5hbbm", SiteID: "p9k3n5zpgh26", NameLocales: map[string]string{"zh-CN": "运营"}}},
		"https://creght.com": {{ProjectID: "pcom1", SiteID: "scom1", NameLocales: map[string]string{"en": "Export"}, PreviewURL: "https://scom1.site.creght.com"}},
	})
	cn := templatesOn("https://creght.cn/")
	if len(cn) != 1 || cn[0].Key != "ops" || cn[0].name != [2]string{"运营", "运营"} {
		t.Fatalf("cn 按平台的来，沿用内置的 key：%+v", cn)
	}
	com := templatesOn("https://creght.com")
	if len(com) != 1 || com[0].Key != "pcom1" || com[0].PreviewURL() != "https://scom1.site.creght.com/" {
		t.Fatalf("com 上的新模板用项目 id 做 key：%+v", com)
	}
	if tp := templateOf(creght.ProjectInfo{ID: "x", FromProjectID: "pcom1"}); tp == nil || tp.Key != "pcom1" {
		t.Error("从 com 模板复制的项目要认得")
	}
	if tp := templateOf(creght.ProjectInfo{ID: "b", FromProjectID: "p9k3n5y5hbbm"}); tp == nil || tp.Key != "ops" {
		t.Error("平台上又登记了退役的模板时，key 沿用 ops")
	}
	if tp := templateOf(creght.ProjectInfo{ID: "p9kmhz3stqqa"}); tp == nil || tp.Key != "trade" {
		t.Error("Origins 沿用内置的")
	}
	if len(templatesOn("https://talizen.com")) != 0 {
		t.Error("talizen 读不到、也没有内置的")
	}
}
