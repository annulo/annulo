package localsite

import (
	"encoding/json"
	"html"
	"net/http"
	"sort"
	"strings"
)

// 页面一开始就接管报错，记进 __TALIZEN_RENDER_ERRORS__（和平台运行时一样），Shuttle 的报错监控从这里读。
const errorsScript = `<script>(function(){
  var list = window.__TALIZEN_RENDER_ERRORS__ = %s;
  function push(source, message, detail){ list.push({ level: "error", source: source, message: String(message), detail: detail || "" }) }
  // ResizeObserver loop …：浏览器的提示（回调里改了布局，通知推迟到下一帧），页面没坏，不算报错
  addEventListener("error", function(e){ if (/^ResizeObserver loop /.test(e.message || "")) return; if (e.error || e.message) push("window.error", e.message || e.error, e.error && e.error.stack) });
  addEventListener("unhandledrejection", function(e){ var r = e.reason; push("unhandledrejection", r && r.message ? (r.name || "Error") + ": " + r.message : r, r && r.stack) });
  // 开发版 React 打日志用 console.error("%o\n\n%s", …) 这种格式符，照 console 的规则换掉再记
  function str(a){ return a && a.stack ? a.stack : typeof a === "object" ? JSON.stringify(a) : String(a) }
  function fmt(args){
    args = Array.prototype.slice.call(args);
    var f = args[0];
    if (typeof f === "string" && /%[sdifoOc]/.test(f)) {
      args.shift();
      f = f.replace(/%[sdifoOc]/g, function(m){ if (!args.length) return m; var a = args.shift(); return m === "%c" ? "" : str(a) });
      return [f].concat(args.map(str)).join(" ");
    }
    return args.map(str).join(" ");
  }
  var ce = console.error;
  console.error = function(){ try { push("console.error", fmt(arguments)) } catch (_) {} return ce.apply(console, arguments) };
})()</script>`

// ServePage 出页面：按地址选语言（没带语言前缀的按 CREGHT_LOCALE cookie 跳转），拼好 HTML 外壳。
// 页面里没有打包好的 JS：importMap + 模块清单（window.__SHUTTLE__）+ 运行时（runtime.go），运行时 import 入口再渲染。
func (r *Renderer) ServePage(w http.ResponseWriter, req *http.Request) error {
	b := r.ensure(req.Context())
	c := b.cfg.I18n
	locale, basename := "", "/"
	if c.enabled() {
		first, _, _ := strings.Cut(strings.TrimPrefix(req.URL.Path, "/"), "/")
		for _, l := range c.Locales {
			if first != "" && strings.EqualFold(first, l) {
				locale, basename = l, "/"+first
				break
			}
		}
		if locale == "" {
			locale = c.defaultLocale()
			if c.detection() {
				if l := r.detectLocale(req, c); l != "" && l != locale {
					target := "/" + l + strings.TrimSuffix(req.URL.Path, "/")
					if req.URL.RawQuery != "" {
						target += "?" + req.URL.RawQuery
					}
					http.Redirect(w, req, target, http.StatusTemporaryRedirect)
					return nil
				}
			}
		}
	}
	meta := b.cfg.pages[locale]

	var s strings.Builder
	s.WriteString("<!DOCTYPE html>\n<html")
	lang := locale
	if lang == "" {
		lang = "en"
	}
	writeAttrs(&s, map[string]string{"lang": lang}, meta.HTMLAttrs)
	s.WriteString(">\n<head>\n<meta charset=\"UTF-8\">\n<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\" />\n")
	if meta.Title != "" {
		s.WriteString("<title>" + html.EscapeString(meta.Title) + "</title>\n")
	}
	if meta.Description != "" {
		s.WriteString(`<meta name="description" content="` + html.EscapeString(meta.Description) + "\" />\n")
	}
	issues := b.issues
	if b.fatal != "" {
		issues = append([]errorItem{{Level: "error", Source: "build", Message: firstLine(b.fatal), Detail: b.fatal}}, issues...)
	}
	if issues == nil {
		issues = []errorItem{}
	}
	s.WriteString(strings.Replace(errorsScript, "%s", string(mustJSON(issues)), 1))
	s.WriteString("\n")
	// Tailwind browser：基础样式（平台 default.css）一份，项目的 index.css 一份（热更新只换这一份）
	s.WriteString(`<style type="text/tailwindcss">` + escapeStyle(baseCSS) + "</style>\n")
	s.WriteString(`<style type="text/tailwindcss" id="__shuttle_css__">` + escapeStyle(b.css) + "</style>\n")
	tw := b.tailwind
	if tw == "" {
		tw = tailwindBrowserURL
	}
	s.WriteString(`<script type="module" src="` + tw + `"></script>` + "\n")
	s.WriteString(meta.Head)
	s.WriteString("\n</head>\n<body")
	writeAttrs(&s, nil, meta.BodyAttrs)
	s.WriteString(">\n<div id=\"root\">")
	s.WriteString(loadingHTML(r.loadingLang(locale)))
	s.WriteString("</div>\n<script>")
	s.WriteString("window.__SHUTTLE__ = " + string(mustJSON(map[string]any{
		"build":    b.id,
		"entry":    b.entry,
		"modules":  b.manifest,
		"basename": basename,
		"error":    b.fatal, // 编译出错：运行时不渲染，盖一层报错，等修好的推送
	})) + ";\n")
	// talizen SDK 读 TalizenConfig：baseUrl 给 /api（Shuttle 反代到平台）、i18n、messages。
	// 不给 pathname：前端路由换页后它会过时，SDK 没有它时自己按 locales 从地址栏取
	s.WriteString("window.TalizenConfig = { baseUrl: window.location.origin + '/api/'")
	if c.enabled() {
		routing := c.RoutingDefaultLocale
		if routing == "" {
			routing = c.defaultLocale()
		}
		s.WriteString(", i18n: " + string(mustJSON(map[string]any{
			"locale":               locale,
			"locales":              c.Locales,
			"defaultLocale":        c.defaultLocale(),
			"routingDefaultLocale": routing,
		})))
	}
	if m := b.messages[locale]; len(m) > 0 {
		s.WriteString(", messages: " + string(m))
	}
	s.WriteString(" };</script>\n")
	s.WriteString(`<script type="importmap">` + string(mustJSON(map[string]any{"imports": b.importMap})) + "</script>\n")
	s.WriteString(`<script type="module" src="/_client/m/@runtime.js?v=` + runtimeVersion + `"></script>` + "\n")
	s.WriteString(meta.BodyEnd)
	s.WriteString("\n</body>\n</html>\n")
	writeHTML(w, s.String())
	return nil
}

// 页面渲染出来之前 #root 里放的加载提示，React 第一次渲染时整个换掉。
// 第一次打开要从 CDN 拉 React、Tailwind browser 和页面的包，慢的时候能有十几秒，不放这个就是一整片空白。
// 只用内联样式和系统颜色（Canvas / CanvasText）：这时 Tailwind 还没加载，颜色跟着模板 head 里设的 color-scheme 走。
// 等久了补一句为什么慢；脚本在 React 渲染后找不到元素就什么也不做。
const loadingTpl = `<div id="__shuttle_loading" style="position:fixed;inset:0;display:flex;flex-direction:column;align-items:center;justify-content:center;gap:12px;font:13px/1.5 system-ui,-apple-system,sans-serif;color:CanvasText;background:Canvas">` +
	`<style>@keyframes __shuttle_spin{to{transform:rotate(360deg)}}</style>` +
	`<div style="width:22px;height:22px;border-radius:50%;border:2px solid color-mix(in srgb,CanvasText 15%,transparent);border-top-color:color-mix(in srgb,CanvasText 60%,transparent);animation:__shuttle_spin .8s linear infinite"></div>` +
	`<div style="opacity:.6">%s</div>` +
	`<div id="__shuttle_loading_slow" style="opacity:.45;max-width:320px;text-align:center;visibility:hidden">%s</div>` +
	`<script>setTimeout(function(){var e=document.getElementById("__shuttle_loading_slow");if(e)e.style.visibility="visible"},5000)</script>` +
	`</div>`

func loadingHTML(lang string) string {
	text, slow := "Loading…", "The first load downloads the page's packages and can take a while."
	if strings.HasPrefix(strings.ToLower(lang), "zh") {
		text, slow = "加载中…", "第一次打开要下载页面用到的包，可能要一会儿"
	}
	return strings.Replace(strings.Replace(loadingTpl, "%s", text, 1), "%s", slow, 1)
}

// loadingLang：加载提示用的语言，页面有语言就跟页面，没有跟 Annulo 界面
func (r *Renderer) loadingLang(locale string) string {
	if locale == "" && r.UILocale != nil {
		return r.UILocale()
	}
	return locale
}

// 样式里出现 </style> 会提前结束标签
func escapeStyle(css string) string { return strings.ReplaceAll(css, "</", "<\\/") }

func writeHTML(w http.ResponseWriter, s string) {
	w.Header().Set("content-type", "text/html; charset=utf-8")
	w.Header().Set("cache-control", "no-store")
	w.Write([]byte(s))
}

func writeAttrs(s *strings.Builder, base, extra map[string]string) {
	all := map[string]string{}
	for k, v := range base {
		all[k] = v
	}
	for k, v := range extra {
		all[k] = v
	}
	keys := make([]string, 0, len(all))
	for k := range all {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		s.WriteString(" " + html.EscapeString(k) + `="` + html.EscapeString(all[k]) + `"`)
	}
}

// json.Marshal 默认把 < > & 转成 < 这类，放进 <script> 里是安全的
func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// detectLocale：CREGHT_LOCALE cookie → Shuttle 界面语言 → Accept-Language，只认配置了的语言（平台 i18n.go detectLocale，多了中间那步）。
func (r *Renderer) detectLocale(req *http.Request, c i18nConfig) string {
	match := func(v string) string {
		v = strings.TrimSpace(v)
		for _, l := range c.Locales {
			if strings.EqualFold(v, l) {
				return l
			}
		}
		base, _, _ := strings.Cut(v, "-")
		for _, l := range c.Locales {
			if strings.EqualFold(base, l) {
				return l
			}
		}
		return ""
	}
	if ck, err := req.Cookie("CREGHT_LOCALE"); err == nil {
		if l := match(ck.Value); l != "" {
			return l
		}
	}
	if r.UILocale != nil {
		if l := match(r.UILocale()); l != "" {
			return l
		}
	}
	for _, part := range strings.Split(req.Header.Get("Accept-Language"), ",") {
		tag, _, _ := strings.Cut(part, ";")
		if l := match(tag); l != "" {
			return l
		}
	}
	return ""
}
