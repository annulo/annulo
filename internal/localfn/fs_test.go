package localfn

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFS(t *testing.T) {
	dir, work, files := t.TempDir(), filepath.Join(t.TempDir(), "Annulo", "项目"), t.TempDir()
	os.WriteFile(filepath.Join(files, "0123456789abcdef0123456789abcdef.png"), []byte("png"), 0o644)
	write(t, dir, "x.ts", `
export async function run(_: any, ctx: any) {
  const empty = ctx.fs.list()
  ctx.fs.write('内容/一篇/稿子.md', '# 要求')
  ctx.fs.mkdir('内容/一篇/A 版')
  ctx.fs.write('内容/一篇/.hidden', 'x')
  ctx.fs.write('内容/一篇/node_modules/a.js', 'x')
  ctx.fs.copy('local:0123456789abcdef0123456789abcdef.png', '内容/一篇/图/封面.png')
  ctx.fs.move('内容/一篇/A 版', '内容/一篇/A 外贸版')
  const list = ctx.fs.list('内容/一篇').map((e: any) => e.path + (e.dir ? '/' : ':' + e.size)).sort()
  const text = ctx.fs.read('内容/一篇/稿子.md')
  const local1 = ctx.fs.toLocal('内容/一篇/图/封面.png')
  const local2 = ctx.fs.toLocal('内容/一篇/图/封面.png')
  ctx.fs.trash('内容/一篇/图/封面.png')
  const gone = ctx.fs.stat('内容/一篇/图/封面.png')
  const st = ctx.fs.stat('内容/一篇/稿子.md')
  const errs: string[] = []
  for (const f of [() => ctx.fs.read('../x'), () => ctx.fs.write('/etc/x', ''), () => ctx.fs.read('没有.md'), () => ctx.fs.move('内容/一篇/稿子.md', '内容/一篇/A 外贸版'), () => ctx.fs.read('link/passwd'), () => ctx.fs.open('内容/一篇/稿子.md', {})]) {
    try { f(); errs.push('ok') } catch (e: any) { errs.push(String(e.message ?? e)) }
  }
  return { canOpen: ctx.fs.canOpen, root: ctx.fs.root, empty, list, text, local1, local2, gone, size: st.size, mtime: st.mtime > 0, errs }
}
`)
	trash := t.TempDir()
	defer func(f func() string) { userTrash = f }(userTrash)
	userTrash = func() string { return trash }
	h := host(dir, &memDB{})
	h.WorkFiles, h.FilesDir = work, files
	os.MkdirAll(work, 0o755)
	os.Symlink("/etc", filepath.Join(work, "link"))
	res, err := Run(context.Background(), h, "x.run", nil, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	m := res.(map[string]any)
	if m["canOpen"] != false {
		t.Fatalf("没有 Open 时 canOpen 要是 false：%v", m["canOpen"])
	}
	if m["root"] != work || len(m["empty"].([]map[string]any)) != 1 { // 只有 link（目录）
		t.Fatalf("root / 一开始的列表不对：%#v", m)
	}
	if got := strings.Join(toStrings(m["list"]), ","); got != "A 外贸版/,图/,图/封面.png:3,稿子.md:8" {
		t.Fatalf("列表不对（要跳过 . 开头和 node_modules）：%s", got)
	}
	if m["text"] != "# 要求" || m["gone"] != nil || m["size"] != int64(8) || m["mtime"] != true {
		t.Fatalf("读、stat、删不对：%#v", m)
	}
	if _, err := os.Stat(filepath.Join(trash, "封面.png")); err != nil {
		t.Fatalf("删掉的要在废纸篓里：%v", err)
	}
	l1, _ := m["local1"].(string)
	if !strings.HasPrefix(l1, "local:") || !strings.HasSuffix(l1, ".png") || l1 != m["local2"] {
		t.Fatalf("toLocal 要给 local:<名字>，同一个文件同一个名字：%v %v", l1, m["local2"])
	}
	if b, err := os.ReadFile(filepath.Join(files, strings.TrimPrefix(l1, "local:"))); err != nil || string(b) != "png" {
		t.Fatalf("toLocal 的文件不在本机文件里：%v", err)
	}
	errs := toStrings(m["errs"])
	for i, want := range []string{"..", "相对", "没有这个文件", "已经有了", "外面", "不能用系统程序"} {
		if !strings.Contains(errs[i], want) {
			t.Fatalf("第 %d 个应该报「%s」：%q", i, want, errs[i])
		}
	}
}

func toStrings(v any) []string {
	var out []string
	switch l := v.(type) {
	case []any:
		for _, x := range l {
			out = append(out, x.(string))
		}
	case []string:
		out = l
	}
	return out
}
