package localsite

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
)

// route 是 pages/ 下的一个页面。规则照平台：
// pages/Index.tsx → /，pages/About.tsx → /About，pages/blog/[slug].tsx → /blog/:slug，末段 index 表示所在目录。
// 大小写不敏感交给 react-router（默认就不区分），静态段优先于动态段也是它排的。
type route struct {
	File string // 相对项目根目录，如 pages/blog/[slug].tsx
	Path string // react-router 的 path，如 /blog/:slug
}

var pageExts = map[string]bool{".tsx": true, ".ts": true, ".jsx": true, ".js": true}

func buildRoutes(files []string) []route {
	var out []route
	seen := map[string]bool{}
	// pages/ 优先于老的 page/：同一个地址只取先出现的
	for _, prefix := range []string{"pages/", "page/"} {
		var list []string
		for _, f := range files {
			if strings.HasPrefix(f, prefix) {
				list = append(list, f)
			}
		}
		sort.Strings(list)
		for _, f := range list {
			rel := strings.TrimPrefix(f, prefix)
			ext := path.Ext(rel)
			// *.canvas.tsx 是 creght 编辑器的画布，不是页面
			if !pageExts[ext] || strings.HasSuffix(rel, ".d.ts") || strings.Contains(rel, ".canvas.") {
				continue
			}
			rel = strings.TrimSuffix(rel, ext)
			segs := strings.Split(rel, "/")
			for i, s := range segs {
				if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
					segs[i] = ":" + s[1:len(s)-1]
				}
			}
			if strings.EqualFold(segs[len(segs)-1], "index") {
				segs = segs[:len(segs)-1]
			}
			p := "/" + strings.Join(segs, "/")
			if seen[strings.ToLower(p)] {
				continue
			}
			seen[strings.ToLower(p)] = true
			out = append(out, route{File: f, Path: p})
		}
	}
	return out
}

var serverExportRE = regexp.MustCompile(`(?m)^\s*export\s+(async\s+)?(function|const|let)\s+getServerSideProps\b`)

// entrySource 是入口模块（/_client/m/@entry）：每个页面一条 react-router 路由，页面组件收到 { params }。
// 它不自己挂载，导出 createApp(basename) 交给运行时（runtime.go）：运行时先注册好 React Refresh 再渲染，
// 热更新整个换掉时也是运行时拿新入口的 createApp 重新渲染。页面按 URL import（pageURL），和别的模块一样按版本缓存。
func entrySource(routes []route, pageURL func(file string) string) string {
	var b strings.Builder
	b.WriteString(`import React from "react";
import { createBrowserRouter, RouterProvider, useParams, useRouteError } from "react-router";
`)
	for i, r := range routes {
		fmt.Fprintf(&b, "import P%d from %q;\n", i, pageURL(r.File))
	}
	b.WriteString(`
const h = React.createElement;
function report(e, source) {
  try {
    (window.__TALIZEN_RENDER_ERRORS__ = window.__TALIZEN_RENDER_ERRORS__ || []).push({
      level: "error", source: source, message: String((e && e.message) || e), detail: (e && e.stack) || "",
    });
  } catch (_) {}
}
// 渲染出错的报错页。浏览器给的堆栈指向 /_client/m/<文件>?v=… 的编译产物：让 Annulo 按 sourcemap 换回源码位置再显示。
// 挂着的时候记在 __SHUTTLE_ROUTE_ERRORS__ 里：热更新就地替换组件救不回已经出错的错误边界，运行时看到它就整页刷新（runtime.go）
function RouteError() {
  const e = useRouteError();
  const raw = String((e && (e.stack || e.message)) || e);
  const [text, setText] = React.useState(raw);
  React.useEffect(function () {
    window.__SHUTTLE_ROUTE_ERRORS__ = (window.__SHUTTLE_ROUTE_ERRORS__ || 0) + 1;
    return function () { window.__SHUTTLE_ROUTE_ERRORS__--; };
  }, []);
  React.useEffect(function () {
    report(e, "render");
    setText(raw);
    fetch("/_shuttle/api/ui/symbolicate", {
      method: "POST", headers: { "content-type": "application/json", "X-Shuttle": "1" }, body: JSON.stringify({ text: raw }),
    }).then(function (r) { return r.ok ? r.json() : null; }).then(function (j) { if (j && j.text) setText(j.text); }).catch(function () {});
  }, [e]);
  // 顶上留出 Annulo 外壳「后台页面出错了」提示条的位置
  return h("div", { style: { padding: "80px 24px 24px", font: "12px/1.6 ui-monospace, monospace" } },
    h("pre", { style: { margin: 0, whiteSpace: "pre-wrap", color: "#e5484d" } }, text),
    h("p", { style: { marginTop: 16, opacity: 0.6, fontFamily: "system-ui" } }, (window.TalizenConfig && window.TalizenConfig.i18n && window.TalizenConfig.i18n.locale === "en") ? "The page will reload once the code is fixed." : "改好代码后页面会自动刷新。"));
}
function NotFound() {
  return h("div", { style: { padding: 24, font: "14px system-ui", opacity: 0.6 } }, "404 · " + location.pathname);
}
function page(C) {
  return function Page() { return h(C, { params: useParams() }); };
}
const routes = [
`)
	notFound := "NotFound"
	for i, r := range routes {
		fmt.Fprintf(&b, "  { path: %q, Component: page(P%d) },\n", r.Path, i)
		if r.Path == "/404" {
			notFound = fmt.Sprintf("page(P%d)", i)
		}
	}
	fmt.Fprintf(&b, "  { path: \"*\", Component: %s },\n", notFound)
	b.WriteString(`].map(function (r) { return Object.assign(r, { ErrorBoundary: RouteError }); });

export function createApp(basename) {
  return h(RouterProvider, { router: createBrowserRouter(routes, { basename: basename || "/" }) });
}
`)
	return b.String()
}

func formatMessage(m api.Message) string {
	if m.Location == nil {
		return m.Text
	}
	l := m.Location
	return fmt.Sprintf("%s:%d:%d: %s\n    %s", l.File, l.Line, l.Column, m.Text, strings.TrimSpace(l.LineText))
}
