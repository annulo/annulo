package localfn

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dop251/goja"
	"github.com/evanw/esbuild/pkg/api"
)

// 照站点 Func 的做法跑生成的文件：esbuild 转成 CommonJS（ES2015），goja 里执行（和 creght 平台的站点 Func 运行时一样）
func runAsSiteFunc(t *testing.T, src, method string, req any, ctx map[string]any) (goja.Value, error) {
	t.Helper()
	res := api.Transform(src, api.TransformOptions{Loader: api.LoaderTS, Format: api.FormatCommonJS, Target: api.ES2015})
	if len(res.Errors) > 0 {
		t.Fatalf("站点 Func 编译不了：%s", res.Errors[0].Text)
	}
	vm := goja.New()
	module := vm.NewObject()
	exports := vm.NewObject()
	module.Set("exports", exports)
	vm.Set("module", module)
	vm.Set("exports", exports)
	if _, err := vm.RunString(string(res.Code)); err != nil {
		t.Fatal(err)
	}
	f, ok := goja.AssertFunction(module.Get("exports").ToObject(vm).Get(method))
	if !ok {
		t.Fatalf("没有导出 %s", method)
	}
	return f(goja.Undefined(), vm.ToValue(req), vm.ToValue(ctx))
}

func TestWriteCloud(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "_i18n.ts", `export const L = (ctx: any, zh: string, en: string) => (ctx.locale === 'en' ? en : zh)`)
	write(t, dir, "stats.ts", `import { L } from './_i18n'
export const cloud = ['count', 'secret']
export function count(input: { status: string }, ctx: any) {
  ctx.progress({ done: 1 })
  const n = ctx.db.query('leads', { where: { status: input.status }, order_by: 'date desc, created_at', filter: [{ field: 'views', op: 'gte', value: 1 }] }).total
  let err = ''
  try { ctx.db.get('leads', 'boom') } catch (e: any) { err = e.message }
  return { n, label: L(ctx, '条', 'leads'), err }
}
export function secret(_: any, ctx: any) { return ctx.secrets.get('K') }
export function sync(_: any, ctx: any) { return ctx.secrets.get('K') }
`)
	write(t, dir, "plain.ts", `export function a() { return 1 }`)
	// 用户自己写的站点 Func 不能被清理掉
	os.MkdirAll(filepath.Join(dir, CloudDir), 0o755)
	os.WriteFile(filepath.Join(dir, CloudDir, "mine.ts"), []byte("export function x() { return 1 }"), 0o644)
	os.WriteFile(filepath.Join(dir, CloudDir, "gone.ts"), []byte(cloudMark+"local/gone.ts\n"), 0o644)

	files, err := WriteCloud(dir)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(files, ",") != CloudDir+"/_manifest.ts,"+CloudDir+"/stats.ts" {
		t.Fatalf("生成的文件不对：%v", files)
	}
	mb, _ := os.ReadFile(filepath.Join(dir, CloudDir, ManifestFile))
	mv, err := runAsSiteFunc(t, string(mb), "get", nil, map[string]any{"member": map[string]any{"require": func() {}}})
	if err != nil || fmt.Sprint(mv.Export()) != "map[cloud:[stats.count stats.secret] remote:[]]" {
		t.Fatalf("清单不对：%v %v", mv, err)
	}
	if _, err := os.Stat(filepath.Join(dir, CloudDir, "mine.ts")); err != nil {
		t.Fatal("用户自己的站点 Func 被删了")
	}
	if _, err := os.Stat(filepath.Join(dir, CloudDir, "gone.ts")); !os.IsNotExist(err) {
		t.Fatal("不再需要的生成文件没删")
	}
	if again, _ := WriteCloud(dir); len(again) != 0 {
		t.Fatalf("没改动时不该重写：%v", again)
	}
	b, _ := os.ReadFile(filepath.Join(dir, CloudDir, "stats.ts"))
	src := string(b)
	if strings.Contains(src, "function sync(") && strings.Contains(src, "export function sync") {
		t.Fatal("没列在 cloud 里的函数不该导出")
	}

	required := 0
	order, filter := "", ""
	ctx := map[string]any{
		"member": map[string]any{"require": func() { required++ }},
		"db": map[string]any{
			"query": func(table string, q map[string]any) map[string]any {
				order = q["order_by"].(string)
				filter = fmt.Sprint(q["filter"])
				return map[string]any{"total": 3, "list": []any{}}
			},
			// 站点 Func 出错时抛字符串
			"get": func(goja.FunctionCall) goja.Value { panic(goja.New().ToValue("record not found")) },
		},
	}
	v, err := runAsSiteFunc(t, src, "count", map[string]any{"input": map[string]any{"status": "new"}, "locale": "en"}, ctx)
	if err != nil {
		t.Fatal(err)
	}
	m := v.Export().(map[string]any)
	if m["n"] != int64(3) || m["label"] != "leads" || required != 1 || order != "body.date desc, created_at" || m["err"] != "record not found" || filter != "map[conditions:[map[fieldId:views operator:gte value:1]] match:and]" {
		t.Fatalf("云端跑的结果不对：%v required=%d", m, required)
	}
	if _, err := runAsSiteFunc(t, src, "secret", map[string]any{"input": nil}, ctx); err == nil || !strings.Contains(err.Error(), "ctx.secrets") {
		t.Fatalf("云端用 ctx.secrets 应该报错：%v", err)
	}
	ctx["member"] = map[string]any{"require": func() error { return errors.New("member_login_required") }}
	if _, err := runAsSiteFunc(t, src, "count", map[string]any{"input": map[string]any{}}, ctx); err == nil {
		t.Fatal("不是项目成员时应该拦住")
	}
}

func TestCloudFnsErrors(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.ts", `export const cloud = ['nope']
export function list() { return [] }`)
	if _, err := WriteCloud(dir); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("cloud 里写了没导出的函数应该报错：%v", err)
	}
	write(t, dir, "a.ts", `export const cloud = ['list']
export function list() { return [] }`)
	os.MkdirAll(filepath.Join(dir, CloudDir), 0o755)
	os.WriteFile(filepath.Join(dir, CloudDir, "a.ts"), []byte("export function mine() {}"), 0o644)
	if _, err := WriteCloud(dir); err == nil {
		t.Fatal("和用户自己的站点 Func 重名应该报错")
	}
}

func TestWriteCloudRemote(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "pub.ts", `export const remote = ['publish']
export async function publish(input: { article_id: string }, ctx: any) { return ctx.secrets.get('K') }`)
	if _, err := WriteCloud(dir); err != nil {
		t.Fatal(err)
	}
	// remote 的不生成转发 Func（页面调模板的 shuttle.call），只进清单
	if _, err := os.Stat(filepath.Join(dir, CloudDir, "pub.ts")); !os.IsNotExist(err) {
		t.Fatal("remote 的函数不该生成站点 Func")
	}
	mb, _ := os.ReadFile(filepath.Join(dir, CloudDir, ManifestFile))
	mv, err := runAsSiteFunc(t, string(mb), "get", nil, map[string]any{"member": map[string]any{"require": func() {}}})
	if err != nil || fmt.Sprint(mv.Export()) != "map[cloud:[] remote:[pub.publish]]" {
		t.Fatalf("清单不对：%v %v", mv, err)
	}
	if ok, _ := IsRemote(dir, "pub.publish"); !ok {
		t.Fatal("pub.publish 声明了 remote")
	}
	if ok, _ := IsRemote(dir, "pub.other"); ok {
		t.Fatal("没声明的不该算 remote")
	}
	if got, _ := RemoteFns(dir); fmt.Sprint(got) != "[pub.publish]" {
		t.Fatalf("RemoteFns 不对：%v", got)
	}
	write(t, dir, "pub.ts", `export const cloud = ['publish']
export const remote = ['publish']
export function publish() {}`)
	if _, err := WriteCloud(dir); err == nil {
		t.Fatal("同一个函数既 cloud 又 remote 应该报错")
	}
}
