package localsite

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestSymbolicate(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "pages"), 0o755)
	os.MkdirAll(filepath.Join(dir, "lib"), 0o755)
	os.WriteFile(filepath.Join(dir, "lib", "util.ts"), []byte("export function title(n: number) {\n  return 'n=' + n\n}\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "pages", "Index.tsx"), []byte(
		"import { title } from '../lib/util'\n\nexport default function Index() {\n  const a = title(1)\n  return <div>{a}{ctx1.x}</div>\n}\n"), 0o644)

	r := New(dir, "https://creght.cn", "p", "s", t.TempDir(), "")
	g := r.modules.buildGraph(dir, []string{"pages/Index.tsx"})
	if len(g.errs) > 0 {
		t.Fatal(g.errs)
	}
	code := g.link("pages/Index.tsx")
	b := &build{served: map[string]served{"pages/Index.tsx": {ver: g.version["pages/Index.tsx"], code: code, sourcemap: g.mods["pages/Index.tsx"].Map}}}
	r.cur = b
	// 编译产物里 ctx1 的位置（行列从 1 开始），当成浏览器堆栈里的那一帧
	var line, col int
	for i, l := range strings.Split(code, "\n") {
		if c := strings.Index(l, "ctx1"); c >= 0 {
			line, col = i+1, c+1
			break
		}
	}
	frame := "http://127.0.0.1:7799" + moduleURL("pages/Index.tsx", g.version["pages/Index.tsx"]) + ":" + strconv.Itoa(line) + ":" + strconv.Itoa(col)
	in := "ReferenceError: ctx1 is not defined\n    at Index (" + frame + ")\n    at ze (https://esm.talizen.com/react-dom@19.2.4/es2022/client.mjs:10:47846)"
	got := r.Symbolicate(in)
	want := "ReferenceError: ctx1 is not defined\n    at Index (pages/Index.tsx:5:19)\n    at ze (https://esm.talizen.com/react-dom@19.2.4/es2022/client.mjs:10:47846)"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	// 不认识的版本原样留着
	if s := r.Symbolicate("at x (http://127.0.0.1:7799/_client/m/pages/Index.tsx?v=ffffffffffff:1:1)"); !strings.Contains(s, "?v=ffffffffffff:1:1") {
		t.Fatal(s)
	}
}
