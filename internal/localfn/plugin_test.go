package localfn

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 插件的本机函数：plugins/<id>/local/x.ts 的 f 叫 <id>/x.f；云端版本生成 backend/func/local/<id>__x.ts
func TestPluginFunctions(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "notes.ts", `export function count() { return 1 }`)
	pdir := filepath.Join(dir, "plugins", "social")
	os.MkdirAll(filepath.Join(pdir, Dir), 0o755)
	os.WriteFile(filepath.Join(pdir, "plugin.json"), []byte(`{}`), 0o644)
	os.WriteFile(filepath.Join(pdir, Dir, "_i18n.ts"), []byte(`export const L = (zh: string) => zh`), 0o644)
	os.WriteFile(filepath.Join(pdir, Dir, "x.ts"), []byte(`import { L } from './_i18n'
export const cloud = ['list']
export const remote = ['publish']
export function list() { return L('列表') }
export function publish(input: { id: string }) { return 'pub ' + input.id }`), 0o644)

	fns, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range fns {
		names = append(names, f.Name+"@"+f.File)
	}
	if got := strings.Join(names, ","); got != "notes.count@local/notes.ts,social/x.list@plugins/social/local/x.ts,social/x.publish@plugins/social/local/x.ts" {
		t.Fatal(got)
	}
	res, err := Run(context.Background(), host(dir, &memDB{rows: map[string][]map[string]any{}}), "social/x.publish", map[string]any{"id": "7"}, func(Event) {})
	if err != nil || res != "pub 7" {
		t.Fatalf("%v %v", res, err)
	}
	if ok, err := IsRemote(dir, "social/x.publish"); !ok || err != nil {
		t.Fatal("remote", ok, err)
	}
	if fns, _ := RemoteFns(dir); strings.Join(fns, ",") != "social/x.publish" {
		t.Fatal(fns)
	}
	files, err := WriteCloud(dir)
	if err != nil || strings.Join(files, ",") != CloudDir+"/_manifest.ts,"+CloudDir+"/social__x.ts" {
		t.Fatalf("%v %v", files, err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, CloudDir, "social__x.ts"))
	if !strings.Contains(string(b), `"social/x.list"`) || !strings.Contains(string(b), "plugins/social/local/x.ts") {
		t.Fatalf("%s", b)
	}
	if _, err := Run(context.Background(), host(dir, &memDB{rows: map[string][]map[string]any{}}), "Bad/x.f", nil, func(Event) {}); err == nil {
		t.Fatal("插件 id 不对要报错")
	}
}

// 项目的代码 import 了插件的文件、插件却没装：报错说清楚去装哪个插件
func TestMissingPluginError(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "channels.ts", `import { summary } from '../plugins/social/local/stats'
export const cloud = ['stats']
export function stats() { return summary() }`)
	_, err := Run(context.Background(), host(dir, &memDB{rows: map[string][]map[string]any{}}), "channels.stats", nil, func(Event) {})
	if err == nil || !strings.Contains(err.Error(), "插件 social") || !strings.Contains(err.Error(), "设置 → 项目 → 插件") {
		t.Fatalf("本机跑：%v", err)
	}
	if _, err := WriteCloud(dir); err == nil || !strings.Contains(err.Error(), "插件 social") {
		t.Fatalf("推送时打包云端版本：%v", err)
	}
}
