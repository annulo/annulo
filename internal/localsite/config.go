package localsite

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dop251/goja"
	"github.com/evanw/esbuild/pkg/api"
)

// siteConfig 是 talizen.config.ts 里本地渲染用得到的部分。
// html / head / body / bodyEnd / metadata 可以写成 (ctx) => 值，按语言各求一遍（pageMeta）。
type siteConfig struct {
	ImportMap map[string]string
	I18n      i18nConfig
	pages     map[string]pageMeta // 语言 → 页面外壳；没配多语言时 key 是 ""
}

type i18nConfig struct {
	DefaultLocale        string   `json:"defaultLocale"`
	Locales              []string `json:"locales"`
	LocaleDetection      *bool    `json:"localeDetection"`
	RoutingDefaultLocale string   `json:"routingDefaultLocale"`
}

func (c i18nConfig) enabled() bool { return len(c.Locales) > 0 }

func (c i18nConfig) defaultLocale() string {
	if c.DefaultLocale != "" {
		return c.DefaultLocale
	}
	if len(c.Locales) > 0 {
		return c.Locales[0]
	}
	return ""
}

// 没写 localeDetection 时默认开（和平台、Next.js 一致）
func (c i18nConfig) detection() bool { return c.LocaleDetection == nil || *c.LocaleDetection }

type pageMeta struct {
	Title       string
	Description string
	HTMLAttrs   map[string]string
	BodyAttrs   map[string]string
	Head        string
	BodyEnd     string
}

var configFiles = []string{"talizen.config.ts", "talizen.config.js", "talizen.config.mjs", "talizen.config.cjs", "folia.config.ts", "folia.config.js"}

// loadConfig 读项目根目录的 talizen.config.ts：esbuild 转成 CommonJS，goja 求值取 default 导出。
// 配置里只能 import 类型（import type），真 import 别的模块会拿到空对象。
func loadConfig(dir string) (*siteConfig, error) {
	cfg := &siteConfig{pages: map[string]pageMeta{}}
	var name, body string
	for _, n := range configFiles {
		if b, err := os.ReadFile(filepath.Join(dir, n)); err == nil {
			name, body = n, string(b)
			break
		}
	}
	if name == "" {
		cfg.pages[""] = pageMeta{}
		return cfg, nil
	}
	loader := api.LoaderTS
	if !strings.HasSuffix(name, ".ts") {
		loader = api.LoaderJS
	}
	res := api.Transform(body, api.TransformOptions{Loader: loader, Format: api.FormatCommonJS, Target: api.ES2020, Sourcefile: name})
	if len(res.Errors) > 0 {
		return nil, fmt.Errorf("%s: %s", name, res.Errors[0].Text)
	}
	vm := goja.New()
	vm.SetFieldNameMapper(goja.TagFieldNameMapper("json", true))
	module := vm.NewObject()
	exports := vm.NewObject()
	_ = module.Set("exports", exports)
	_ = vm.Set("module", module)
	_ = vm.Set("exports", exports)
	_ = vm.Set("require", func(goja.FunctionCall) goja.Value { return vm.NewObject() })
	_ = vm.Set("defineConfig", func(c goja.FunctionCall) goja.Value { return c.Argument(0) })
	if _, err := vm.RunString(string(res.Code)); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	def, _ := module.Get("exports").ToObject(vm).Get("default").(*goja.Object)
	if def == nil {
		cfg.pages[""] = pageMeta{}
		return cfg, nil
	}

	if im, ok := def.Get("importMap").(*goja.Object); ok {
		if imports, ok := im.Get("imports").(*goja.Object); ok {
			cfg.ImportMap = map[string]string{}
			for _, k := range imports.Keys() {
				cfg.ImportMap[k] = imports.Get(k).String()
			}
		}
	}
	if v := def.Get("i18n"); v != nil && !goja.IsUndefined(v) && !goja.IsNull(v) {
		if err := vm.ExportTo(v, &cfg.I18n); err != nil {
			return nil, fmt.Errorf("%s: i18n: %w", name, err)
		}
	}

	locales := cfg.I18n.Locales
	if !cfg.I18n.enabled() {
		locales = []string{""}
	}
	for _, loc := range locales {
		ctx := vm.NewObject()
		_ = ctx.Set("locale", loc)
		_ = ctx.Set("locales", cfg.I18n.Locales)
		_ = ctx.Set("defaultLocale", cfg.I18n.defaultLocale())
		_ = ctx.Set("params", vm.NewObject())
		_ = ctx.Set("query", vm.NewObject())
		// 值或 (ctx) => 值
		eval := func(key string) (goja.Value, error) {
			v := def.Get(key)
			if fn, ok := goja.AssertFunction(v); ok {
				return fn(goja.Undefined(), ctx)
			}
			return v, nil
		}
		var m pageMeta
		var err error
		str := func(key string) string {
			v, e := eval(key)
			if e != nil {
				err = fmt.Errorf("%s: %s: %w", name, key, e)
			}
			if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
				return ""
			}
			return v.String()
		}
		attrs := func(key string) map[string]string {
			v, e := eval(key)
			if e != nil {
				err = fmt.Errorf("%s: %s: %w", name, key, e)
			}
			o, ok := v.(*goja.Object)
			if !ok {
				return nil
			}
			out := map[string]string{}
			for _, k := range o.Keys() {
				out[k] = o.Get(k).String()
			}
			return out
		}
		m.Head, m.BodyEnd = str("head"), str("bodyEnd")
		m.HTMLAttrs, m.BodyAttrs = attrs("html"), attrs("body")
		if md, e := eval("metadata"); e != nil {
			err = fmt.Errorf("%s: metadata: %w", name, e)
		} else if o, ok := md.(*goja.Object); ok {
			if t := o.Get("title"); t != nil && !goja.IsUndefined(t) {
				// title 可以是字符串，也可以是 { default, template }
				if to, ok := t.(*goja.Object); ok && to.ClassName() == "Object" {
					m.Title = to.Get("default").String()
				} else {
					m.Title = t.String()
				}
			}
			if d := o.Get("description"); d != nil && !goja.IsUndefined(d) {
				m.Description = d.String()
			}
		}
		if err != nil {
			return nil, err
		}
		cfg.pages[loc] = m
	}
	return cfg, nil
}
