package localfn

// 本机函数也在云端跑（docs/mobile-remote.md 原语一）：手机上打开运营后台时没有 Shuttle，
// 只读表、算统计的函数在文件里声明 export const cloud = ['list', 'stats']，
// annulo push 前把它们打包成站点 Func backend/func/local/<文件>.ts，页面不在 Shuttle 里时调 invoke('local/<文件>.<函数>')。
//
// 云端那份只给 ctx.db、ctx.mcp('creght', …)（站点 Func 的，只读、只给所有者）、ctx.member、ctx.locale；
// progress / log / sleep 什么都不做；ctx.workspace 是 undefined（ctx.workspace?.x ?? 默认值 的写法照常）；
// secrets、browser、llm、exec 这些本机能力一碰就抛错，写清楚要回电脑上跑。
// 调用者必须是项目成员（ctx.member.require()）：表里是用户自己的业务数据。
//
// 生成的文件不进项目的 git（.git/info/exclude，见 wsgit），只随 push 发到 creght。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/annulo/annulo/internal/brand"
	"github.com/annulo/annulo/internal/i18n"
	"github.com/annulo/annulo/internal/plugin"
	"github.com/dop251/goja"
	"github.com/evanw/esbuild/pkg/api"
)

// CloudDir 是生成的站点 Func 在项目里的目录（相对项目根目录）。
const CloudDir = "backend/func/local"

// cloudMark 是生成文件的第一行：清理时只删带它的文件，不碰用户自己放在这个目录里的 Func。新名字的标记也认（brand.CloudMarkers）。
var cloudMark = brand.CloudMarkers[0]

// cloudCtx 是云端那份 ctx：站点 Func 的 ctx 只挑这几样给出去。
const cloudCtx = `
const __forbid = ['secrets', 'fetchAll', 'html', 'oauth', 'browser', 'llm', 'exec', 'agent']
const __sys = ['id', 'sort', 'created_at', 'updated_at', 'table_id', 'user_id']
// 站点 Func 的宿主能力出错时抛的是字符串，本机函数都按 Error 读 e.message：换成 Error
const __err = (e) => (e instanceof Error ? e : new Error(String(e)))
function __wrap(f) {
  return function () {
    try {
      const r = f.apply(this, arguments)
      return r && typeof r.then === 'function' ? r.then(undefined, (e) => { throw __err(e) }) : r
    } catch (e) {
      throw __err(e)
    }
  }
}
// 本机的 order_by 写 'date desc' 也行（Annulo 补 body.），站点 Func 只认 'body.date desc'：照本机的规则补上
function __order(o) {
  return o.split(',').map((t) => {
    const f = t.trim().split(/\s+/)
    if (f[0] && __sys.indexOf(f[0]) < 0 && f[0].indexOf('body.') !== 0) f[0] = 'body.' + f[0]
    return f.join(' ')
  }).join(', ')
}
// 本机的 filter 也认简写 [{ field, op, value }]，站点 Func 只认 { conditions: [{ fieldId, operator, value }] }：换过去
function __filter(f) {
  if (!Array.isArray(f)) return f
  return { match: 'and', conditions: f.map((c) => (c && c.field !== undefined ? { fieldId: c.field, operator: c.op, value: c.value } : c)) }
}
// query 的 order_by 是表的字段（要补 body.），aggregate 的 order_by 是分组和 metrics 的 as，不动
function __args(q, order) {
  if (!q || typeof q !== 'object') return q
  const out = Object.assign({}, q)
  if (q.filter !== undefined) out.filter = __filter(q.filter)
  if (order && typeof q.order_by === 'string') out.order_by = __order(q.order_by)
  return out
}
function __cloudCtx(ctx, locale, fn) {
  const en = locale === 'en'
  const db = {}
  for (const k of ['get', 'query', 'aggregate', 'insert', 'update', 'delete']) if (ctx.db && ctx.db[k]) db[k] = __wrap(ctx.db[k])
  const { query, aggregate } = db
  if (query) db.query = (table, q) => query(table, __args(q, true))
  if (aggregate) db.aggregate = (table, q) => aggregate(table, __args(q, false))
  const c = {
    db,
    member: ctx.member,
    locale: en ? 'en' : 'zh',
    chat_id: '', // 云端没有对话
    mcp: Object.assign(__wrap((server, tool, args) => {
      if (server !== 'creght') throw new Error(en ? 'In the cloud ctx.mcp only reaches creght (read-only); "' + server + '" is only on the computer running Annulo' : '云端的 ctx.mcp 只有 creght（只读），「' + server + '」只在电脑上的 Annulo 里有')
      return ctx.mcp(server, tool, args)
    }), { servers: () => [{ name: 'creght', status: 'connected' }] }),
    progress() {},
    log() {},
    async sleep() {},
  }
  for (const k of __forbid) {
    Object.defineProperty(c, k, { get() {
      throw new Error(en ? fn + ' runs in the cloud here, where ctx.' + k + " isn't available; take it out of cloud in its file so it only runs in Annulo on the computer" : fn + ' 现在在云端跑，云端不能用 ctx.' + k + '：把它从文件的 cloud 里去掉，只在电脑上的 Annulo 里跑')
    } })
  }
  return c
}
`

// CloudFns 读一个本机函数文件声明的 cloud 列表（没声明返回 nil），并检查每一项都是导出的函数。
func CloudFns(path string) ([]string, error) {
	names, _, err := Declared(path)
	return names, err
}

// Declared 读文件声明的 cloud（云端跑）和 remote（转给电脑跑）两个列表，检查每一项都是导出的函数、两边不重复。
func Declared(path string) (cloud, remote []string, err error) {
	code, err := compile(path)
	if err != nil {
		return nil, nil, err
	}
	vm := goja.New()
	exports, err := load(vm, code)
	if err != nil {
		return nil, nil, err
	}
	read := func(key string) ([]string, error) {
		v := exports.Get(key)
		if v == nil || goja.IsUndefined(v) {
			return nil, nil
		}
		var names []string
		if err := vm.ExportTo(v, &names); err != nil {
			return nil, i18n.Errorf("%s 的 %s 要是函数名的数组，比如 export const %s = ['list']", "%s in %s must be an array of function names, e.g. export const %s = ['list']", filepath.Base(path), key, key)
		}
		for _, n := range names {
			if _, ok := goja.AssertFunction(exports.Get(n)); !ok {
				return nil, i18n.Errorf("%s 的 %s 里写了 %q，但文件没有导出这个函数", "%s in %s lists %q, but the file doesn't export that function", key, filepath.Base(path), n)
			}
		}
		return names, nil
	}
	if cloud, err = read("cloud"); err != nil {
		return nil, nil, err
	}
	if remote, err = read("remote"); err != nil {
		return nil, nil, err
	}
	for _, n := range remote {
		for _, c := range cloud {
			if n == c {
				return nil, nil, i18n.Errorf("%s 的 %s 同时写在 cloud 和 remote 里：只读表的放 cloud（云端跑），要电脑的放 remote（转给电脑跑）", "%s lists %s in both cloud and remote: table-only goes in cloud (runs in the cloud), computer-only in remote (forwarded to the computer)", filepath.Base(path), n)
			}
		}
	}
	return cloud, remote, nil
}

// IsRemote：这个函数（文件.函数）有没有声明 remote，中转只执行声明过的（internal/relay）。
func IsRemote(workDir, name string) (bool, error) {
	file, fn, ok := strings.Cut(name, ".")
	if !ok {
		return false, nil
	}
	path, err := source(workDir, file)
	if err != nil {
		return false, err
	}
	_, remote, err := Declared(path)
	if err != nil {
		return false, err
	}
	for _, n := range remote {
		if n == fn {
			return true, nil
		}
	}
	return false, nil
}

// BuildCloud 把一个文件里声明在 cloud 的函数打包成一份站点 Func 的源码。
// 插件的文件（social/x）生成 backend/func/local/social__x.ts，页面调 local/social__x.<函数>（plugin.ChatKey）。
func BuildCloud(workDir, file string, names []string) (string, error) {
	id, base, _ := plugin.Split(file)
	rel := plugin.Rel(id, Dir) + "/" + base
	var b strings.Builder
	fmt.Fprintf(&b, "import * as m from './%s'\n%s", rel, cloudCtx)
	for _, n := range names {
		fn, _ := json.Marshal(file + "." + n)
		// 页面传 { input, locale }（lib/shuttle.ts 的 runLocal），这里拆开再交给本机函数，和本机的 (input, ctx) 一样
		fmt.Fprintf(&b, "export function %s(req, ctx) {\n  ctx.member.require()\n  return m.%s(req && req.input, __cloudCtx(ctx, req && req.locale, %s))\n}\n", n, n, fn)
	}
	res := api.Build(api.BuildOptions{
		Stdin:         &api.StdinOptions{Contents: b.String(), ResolveDir: workDir, Sourcefile: "cloud.ts", Loader: api.LoaderTS},
		Bundle:        true,
		Write:         false,
		AbsWorkingDir: workDir, // 产物里的文件注释写 local/xx.ts，不带本机路径
		Charset:       api.CharsetUTF8,
		Format:        api.FormatESModule,
		Platform:      api.PlatformNeutral,
		Target:        api.ES2017, // 站点 Func 自己还会降到 ES2015
		External:      []string{"talizen", "talizen/*"},
		LogLevel:      api.LogLevelSilent,
		Sourcemap:     api.SourceMapNone,
	})
	if len(res.Errors) > 0 {
		if err := missingPlugin(res.Errors); err != nil {
			return "", err
		}
		return "", i18n.Errorf("打包 %s.ts 到云端失败：%s", "Bundling %s.ts for the cloud failed: %s", rel, res.Errors[0].Text)
	}
	if len(res.OutputFiles) == 0 {
		return "", i18n.New("打包没有产物", "Bundling produced no output")
	}
	head := fmt.Sprintf("%s%s.ts\n// 由 annulo push 从 %s.ts 生成（cloud 里列的函数），不要改这里：改 %s.ts。\n\n", cloudMark, rel, rel, rel)
	return head + string(res.OutputFiles[0].Contents), nil
}

// ManifestFile 是生成的清单（站点 Func local/_manifest）：哪些本机函数在云端跑（cloud）、哪些转给电脑跑（remote）。
// remote 那一列是兜底：电脑连平台时自己会报（internal/relay 的 hello），页面优先用电脑报的。
// 页面不在 Shuttle 里时按它判断按钮能不能点，按钮上不用再标；下划线开头，不会和本机函数文件重名（本机函数文件名不能以 _ 开头）。
const ManifestFile = "_manifest.ts"

func manifestSource(m map[string][]string) string {
	for _, k := range []string{"cloud", "remote"} {
		sort.Strings(m[k])
	}
	b, _ := json.Marshal(m)
	return fmt.Sprintf("%s%s\n// 由 annulo push 生成：local/*.ts 里 cloud、remote 声明的函数，页面不在 Annulo 里时按它判断能不能调。不要改这里。\n\nexport function get(_input, ctx) {\n  ctx.member.require()\n  return %s\n}\n", cloudMark, "manifest", string(b))
}

// RemoteFns 列出一个项目里声明了 remote 的函数（文件.函数），中转连平台时报上去（internal/relay 的 hello）。
// 读声明要编译文件，按文件的修改时间缓存：没改过的不重读；读不了的文件跳过（push、试跑时会报出来）。
func RemoteFns(workDir string) ([]string, error) {
	files, err := fnFiles(workDir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, f := range files {
		info, err := f.Entry.Info()
		if err != nil {
			continue
		}
		key := fmt.Sprintf("%s|%d|%d", f.Path, info.ModTime().UnixNano(), info.Size())
		var remote []string
		if v, ok := remoteCache.Load(key); ok {
			remote = v.([]string)
		} else {
			_, remote, _ = Declared(f.Path)
			remoteCache.Store(key, remote)
		}
		for _, n := range remote {
			out = append(out, f.Name+"."+n)
		}
	}
	sort.Strings(out)
	return out, nil
}

var remoteCache sync.Map // 路径|修改时间|大小 → 这个文件声明的 remote

// WriteCloud 按 local/*.ts 的 cloud 声明重新生成 backend/func/local/，删掉不再需要的生成文件。返回写了哪些文件（相对项目根目录）。
func WriteCloud(workDir string) ([]string, error) {
	files, err := fnFiles(workDir)
	if err != nil {
		return nil, err
	}
	want := map[string]string{}
	manifest := map[string][]string{"cloud": {}, "remote": {}}
	for _, f := range files {
		names, remote, err := Declared(f.Path)
		if err != nil {
			return nil, err
		}
		for _, n := range remote {
			manifest["remote"] = append(manifest["remote"], f.Name+"."+n)
		}
		// remote 的不用生成：页面调模板自带的 backend/func/shuttle.ts 的 call，转给电脑后由 Shuttle 按声明把关
		if len(names) == 0 {
			continue
		}
		src, err := BuildCloud(workDir, f.Name, names)
		if err != nil {
			return nil, err
		}
		want[plugin.ChatKey(f.Name)+".ts"] = src
		for _, n := range names {
			manifest["cloud"] = append(manifest["cloud"], f.Name+"."+n)
		}
	}
	if len(manifest["cloud"]) > 0 || len(manifest["remote"]) > 0 {
		want[ManifestFile] = manifestSource(manifest)
	}

	out := filepath.Join(workDir, CloudDir)
	old, _ := os.ReadDir(out)
	for _, e := range old {
		p := filepath.Join(out, e.Name())
		if _, keep := want[e.Name()]; keep || e.IsDir() {
			continue
		}
		if b, err := os.ReadFile(p); err == nil && brand.HasCloudMarker(b) {
			os.Remove(p)
		}
	}
	var written []string
	for name, src := range want {
		p := filepath.Join(out, name)
		if b, err := os.ReadFile(p); err == nil && !brand.HasCloudMarker(b) {
			return nil, i18n.Errorf("%s/%s 是你自己写的站点 Func，和本机函数 %s/%s 的云端版本重名了：换个名字", "%s/%s is a site Func you wrote, and it clashes with the cloud copy of local function %s/%s: rename one of them", CloudDir, name, Dir, name)
		} else if err == nil && string(b) == src {
			continue
		}
		if err := os.MkdirAll(out, 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			return nil, err
		}
		written = append(written, CloudDir+"/"+name)
	}
	sort.Strings(written)
	return written, nil
}
