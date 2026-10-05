package localsite

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func writeFiles(t *testing.T, dir string, files map[string]string) {
	for p, c := range files {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755)
		if err := os.WriteFile(filepath.Join(dir, p), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// 一个小项目：页面 → 组件 → lib；另有一个只被动态 import 的组件、一对循环 import
var project = map[string]string{
	"pages/Index.tsx":     "import { Card } from '../components/Card'\nexport default function Index() { return <Card /> }\n",
	"components/Card.tsx": "import { text } from '../lib/text'\nimport { a } from '../lib/a'\nexport function Card() { const L = import('./Lazy'); return <p>{text}{a}</p> }\n",
	"components/Lazy.tsx": "export default function Lazy() { return <i /> }\n",
	"lib/text.ts":         "export const text = 'v1'\n",
	"lib/a.ts":            "import { b } from './b'\nexport const a = 'a' + b\n",
	"lib/b.ts":            "import { a } from './a'\nexport const b = 'b'\nexport const both = () => a\n",
}

func TestGraphCompilesEachFileAndLinksByVersion(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, project)
	c := &moduleCache{dir: t.TempDir()}
	g := c.buildGraph(dir, []string{"pages/Index.tsx"})
	if len(g.errs) > 0 {
		t.Fatal(g.errs)
	}
	if len(g.mods) != 6 {
		t.Fatalf("modules = %d", len(g.mods))
	}
	card := g.mods["components/Card.tsx"]
	if !reflect.DeepEqual(card.Deps, []string{"lib/a.ts", "lib/text.ts"}) || !reflect.DeepEqual(card.Lazy, []string{"components/Lazy.tsx"}) {
		t.Fatalf("deps %v lazy %v", card.Deps, card.Lazy)
	}
	// 循环 import 的两个文件共用一个版本
	if g.version["lib/a.ts"] != g.version["lib/b.ts"] {
		t.Fatal("cycle members should share a version")
	}
	linked := g.link("components/Card.tsx")
	for _, dep := range []string{"lib/text.ts", "lib/a.ts", "components/Lazy.tsx"} {
		if !strings.Contains(linked, `"`+moduleURL(dep, g.version[dep])+`"`) {
			t.Fatalf("%s not linked:\n%s", dep, linked)
		}
	}
	if strings.Contains(linked, depPlaceholder) {
		t.Fatal("placeholder left in linked code")
	}
	if !card.boundary("components/Card.tsx") || g.mods["lib/text.ts"].boundary("lib/text.ts") {
		t.Fatal("Card is a Refresh boundary, lib/text.ts is not")
	}

	// 改 lib/text.ts：它和 import 了它的一路（Card、Index）换版本，别的不变；没改的文件走缓存
	before := g.version
	writeFiles(t, dir, map[string]string{"lib/text.ts": "export const text = 'v2'\n"})
	g2 := c.buildGraph(dir, []string{"pages/Index.tsx"})
	for p, changed := range map[string]bool{"lib/text.ts": true, "components/Card.tsx": true, "pages/Index.tsx": true, "lib/a.ts": false, "components/Lazy.tsx": false} {
		if (g2.version[p] != before[p]) != changed {
			t.Errorf("%s version changed = %v, want %v", p, g2.version[p] != before[p], changed)
		}
	}
	if g2.mods["lib/a.ts"] != g.mods["lib/a.ts"] {
		t.Error("unchanged file should come from the compile cache")
	}
}

func TestDiffHotSwapsNearestBoundary(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, project)
	r := New(dir, "https://creght.cn", "p", "s", t.TempDir(), "")
	files, sig := r.scan(nil)
	// 不连平台：给一份现成的 importMap
	r.sys.cfg = &renderConfig{ImportMap: map[string]string{"react": "https://x/react"}}
	r.sys.at = time.Now()
	b1 := r.build(t.Context(), files, sig)
	if b1.fatal != "" {
		t.Fatal(b1.fatal)
	}

	writeFiles(t, dir, map[string]string{"lib/text.ts": "export const text = 'v2'\n"})
	files, sig = r.scan(b1.inputs)
	b2 := r.build(t.Context(), files, sig)
	u := diff(b1, b2)
	if u == nil || u.Type != "update" || !reflect.DeepEqual(u.Boundaries, []string{"components/Card.tsx"}) || u.CSS != nil {
		t.Fatalf("update = %+v", u)
	}

	// 页面自己导出了不是组件的东西：不是边界，走到入口，只能重新渲染
	writeFiles(t, dir, map[string]string{"pages/Index.tsx": "import { Card } from '../components/Card'\nexport const meta = 1\nexport default function Index() { return <Card /> }\n"})
	files, sig = r.scan(b2.inputs)
	b3 := r.build(t.Context(), files, sig)
	if u := diff(b2, b3); u == nil || u.Boundaries != nil {
		t.Fatalf("expected a full re-render, got %+v", u)
	}

	// 只改 index.css：带上 css，没有要换的模块
	writeFiles(t, dir, map[string]string{"index.css": ".x { color: red; }\n"})
	files, sig = r.scan(b3.inputs)
	b4 := r.build(t.Context(), files, sig)
	if u := diff(b3, b4); u == nil || u.CSS == nil || !strings.Contains(*u.CSS, "color: red") || len(u.Boundaries) != 0 || u.Boundaries == nil {
		t.Fatalf("expected a css-only update, got %+v", u)
	}

	// 写错了：推 error
	writeFiles(t, dir, map[string]string{"components/Card.tsx": "export function Card() { return <p></div> }\n"})
	files, sig = r.scan(b4.inputs)
	b5 := r.build(t.Context(), files, sig)
	if u := diff(b4, b5); u == nil || u.Type != "error" || !strings.Contains(u.Error, "components/Card.tsx:1:") {
		t.Fatalf("expected an error update, got %+v", u)
	}
}
