package localsite

import (
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestBuildRoutes(t *testing.T) {
	got := buildRoutes([]string{
		"pages/Index.tsx", "pages/Index.canvas.tsx", "pages/About.tsx", "pages/blog/[slug].tsx",
		"pages/blog/index.tsx", "pages/types.d.ts", "pages/notes.md", "page/About.tsx", "page/Old.tsx", "components/X.tsx",
	})
	want := []route{
		{"pages/About.tsx", "/About"},
		{"pages/Index.tsx", "/"},
		{"pages/blog/[slug].tsx", "/blog/:slug"},
		{"pages/blog/index.tsx", "/blog"},
		{"page/Old.tsx", "/Old"}, // page/About.tsx 被 pages/About.tsx 盖住
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("routes = %+v", got)
	}
}

func TestEntrySourceNotFound(t *testing.T) {
	src := entrySource([]route{{"pages/Index.tsx", "/"}, {"pages/404.tsx", "/404"}}, func(f string) string { return moduleURL(f, "v1") })
	if !strings.Contains(src, `import P1 from "/_client/m/pages/404.tsx?v=v1"`) || !strings.Contains(src, `{ path: "*", Component: page(P1) }`) {
		t.Fatal(src)
	}
}

func TestMergeImportMap(t *testing.T) {
	sys := &renderConfig{
		ImportMap:       map[string]string{"react": "https://esm.talizen.com/react@19", "lucide-react": "https://esm.talizen.com/lucide-react@1?external=react,react-dom"},
		IgnoreImportMap: []string{"react"},
	}
	got := mergeImportMap(sys, map[string]string{
		"react":            "https://evil/react",
		"@tiptap/react":    "https://esm.talizen.com/@tiptap/react@3?bundle&external=@tiptap/core",
		"three/":           "https://esm.talizen.com/three@0.1/",
		"three":            "https://esm.talizen.com/three@0.1",
		"@tiptap/core/raw": "https://esm.talizen.com/@tiptap/core@3/x.js?raw",
	})
	want := map[string]string{
		"react":                 "https://esm.talizen.com/react@19", // 系统保护，站点改不了
		"lucide-react":          "https://esm.talizen.com/lucide-react@1?external=react,react-dom",
		"react-router":          shuttleImports["react-router"],
		"react-refresh/runtime": shuttleImports["react-refresh/runtime"],
		"@tiptap/react":         "https://esm.talizen.com/@tiptap/react@3?bundle&external=@tiptap/core,react,react-dom",
		"three/":                "https://esm.talizen.com/three@0.1&external=react,react-dom,three/",
		"three":                 "https://esm.talizen.com/three@0.1?external=react,react-dom",
		"@tiptap/core/raw":      "https://esm.talizen.com/@tiptap/core@3/x.js?raw&external=react,react-dom",
	}
	if !reflect.DeepEqual(got, want) {
		for k, v := range got {
			if want[k] != v {
				t.Errorf("%s = %s, want %s", k, v, want[k])
			}
		}
		t.Fatal()
	}
}

func TestDetectLocale(t *testing.T) {
	c := i18nConfig{DefaultLocale: "zh", Locales: []string{"zh", "en"}}
	r := &Renderer{}
	req := func(cookie, accept string) string {
		q := httptest.NewRequest("GET", "/", nil)
		if cookie != "" {
			q.Header.Set("Cookie", "CREGHT_LOCALE="+cookie)
		}
		q.Header.Set("Accept-Language", accept)
		return r.detectLocale(q, c)
	}
	if got := req("zh", "en-US,en"); got != "zh" {
		t.Errorf("cookie 优先：%q", got)
	}
	if got := req("", "en-US,en;q=0.9"); got != "en" {
		t.Errorf("Accept-Language：%q", got)
	}
	r.UILocale = func() string { return "zh" }
	if got := req("", "en-US"); got != "zh" {
		t.Errorf("没有 cookie 时用 Annulo 的界面语言：%q", got)
	}
}
