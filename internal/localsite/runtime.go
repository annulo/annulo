package localsite

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

// runtimeJS 是页面里的运行时（/_client/m/@runtime.js），决策照 folia-web 编辑器预览（inner/src/pages/InnerFrame.tsx）：
//
//  1. 先接上 React Refresh（react-refresh/runtime 的 injectIntoGlobalHook 必须在 react-dom 加载之前，所以 react-dom 用动态 import）；
//  2. import 入口，把每个已加载模块导出的组件注册给 Refresh（按「路径 导出名」），再用入口的 createApp 渲染；
//  3. 订阅 /_client/events：
//     - 能热替换（boundaries）：重新 import 这些模块的新 URL、注册、performReactRefresh()，页面状态保留；
//     之后冒出 hooks 相关的报错（改了 hooks 的顺序）就退回重新渲染入口；
//     - 不能（boundaries 为 null）：import 新入口重新渲染（状态丢掉）；
//     - css：只换 <style id="__shuttle_css__">，Tailwind browser 自己重新生成；
//     - error：构建出错，盖一层报错，修好后刷新一次（顺带清掉报错列表）；reload：importMap、配置、文案变了，整页刷新；
//     - 页面正显示渲染报错（路由错误边界接住的）时来了任何更新：整页刷新，已经出错的边界热替换救不回来。
//
// 组件注册放在运行时这边（import 之后对导出注册），Go 不改写源码：代价是没导出的内部组件改了会重新挂载。
// 没有 $RefreshSig$（不追踪 hooks 签名），改了 hooks 靠上面的报错兜底，和 folia-web 一样。
const runtimeJS = `const S = window.__SHUTTLE__;
const errors = (window.__TALIZEN_RENDER_ERRORS__ = window.__TALIZEN_RENDER_ERRORS__ || []);
function report(source, e) {
  errors.push({ level: "error", source, message: String((e && e.message) || e), detail: (e && e.stack) || "" });
}
function overlay(text) {
  let el = document.getElementById("__shuttle_overlay");
  if (!text) { if (el) el.remove(); return; }
  if (!el) {
    el = document.createElement("pre");
    el.id = "__shuttle_overlay";
    // 顶上留出 Annulo 外壳「后台页面出错了」提示条的位置
    el.style.cssText = "position:fixed;inset:0;z-index:2147483647;margin:0;padding:80px 24px 24px;overflow:auto;white-space:pre-wrap;font:12px/1.6 ui-monospace,monospace;color:#e5484d;background:rgba(10,10,10,.94)";
    document.body.appendChild(el);
  }
  el.textContent = text;
}

let R = null;
try {
  const m = await import("react-refresh/runtime");
  R = m.default || m;
  R.injectIntoGlobalHook(window);
} catch (e) {
  console.warn("[shuttle] React Refresh unavailable, updates will re-render the page", e);
}
window.$RefreshReg$ = () => {};
window.$RefreshSig$ = () => (t) => t;

const isComponent = (k, v) => (k === "default" || /^[A-Z]/.test(k)) && (typeof v === "function" || (v && typeof v === "object" && v.$$typeof));
async function register(path, url) {
  const ns = await import(url);
  if (R) for (const k of Object.keys(ns)) if (isComponent(k, ns[k])) R.register(ns[k], path + " " + k);
  return ns;
}

let mods = S.modules, build = S.build, broken = !!S.error, root = null;
const { createRoot } = await import("react-dom/client");
async function mount(entry) {
  const app = await import(entry);
  // 动态 import 的（lazy）还没加载，不去碰它：用到它时它自己加载，改它时再注册
  await Promise.all(Object.entries(mods).filter(([, m]) => !m.lazy).map(([p, m]) => register(p, m.url)));
  if (!root) root = createRoot(document.getElementById("root"));
  root.render(app.createApp(S.basename));
}

if (broken) overlay(S.error);
else await mount(S.entry).catch((e) => { report("render", e); overlay(String((e && e.stack) || e)); broken = true; });

async function apply(u) {
  if (u.build === build) return;
  if (u.type === "hello") return location.reload(); // 页面加载后、连上之前又建过一次：拿不到那次的更新，刷新
  if (u.type === "reload") return location.reload();
  if (u.type === "error") {
    build = u.build;
    broken = true;
    overlay(u.error);
    report("build", { message: u.error.split("\n")[0], stack: u.error });
    return;
  }
  if (broken) return location.reload();
  // 页面正显示渲染报错（路由的错误边界接住了）：就地替换救不回来，整页刷新（顺带清掉外壳的报错提示）
  if ((window.__SHUTTLE_ROUTE_ERRORS__ || 0) > 0) return location.reload();
  const prev = mods;
  mods = u.modules;
  build = u.build;
  if (u.css != null) { const el = document.getElementById("__shuttle_css__"); if (el) el.textContent = u.css; }
  if (u.boundaries && R) {
    if (!u.boundaries.length) return;
    const seen = errors.length;
    for (const p of u.boundaries) {
      if (prev[p]) await register(p, prev[p].url); // 旧版本先注册（lazy 模块之前没注册过），新版本才能认出是同一个组件
      await register(p, mods[p].url);
    }
    R.performReactRefresh();
    await new Promise((r) => setTimeout(r, 150));
    if (errors.slice(seen).some((e) => /hook/i.test(e.message))) await mount(u.entry);
    return;
  }
  await mount(u.entry);
}

let chain = Promise.resolve();
const es = new EventSource("/_client/events");
es.onmessage = (e) => {
  const u = JSON.parse(e.data);
  chain = chain.then(() => apply(u)).catch((err) => { console.error("[shuttle] hot update failed, reloading", err); location.reload(); });
};
`

var runtimeVersion = func() string {
	s := sha256.Sum256([]byte(runtimeJS))
	return hex.EncodeToString(s[:6])
}()

// Tailwind browser（@tailwindcss/browser，和 folia-web 编辑器预览一样）：在浏览器里扫页面上的 class 生成样式，
// 改了组件里的 class 不用重编 CSS；改 index.css 只换 <style type="text/tailwindcss">。
// 它不支持 @plugin 和别的 @import（一写就整份编不出来），这里去掉并提示。
const tailwindBrowserURL = "https://esm.talizen.com/@tailwindcss/browser@4.2.2"

var cssUnsupportedRE = regexp.MustCompile(`(?m)^\s*@(import|plugin)\s+[^;]*;\s*\n?`)

// browserCSS 去掉 Tailwind browser 编不了的 @import / @plugin，dropped 是去掉的那几行
func browserCSS(css string) (out string, dropped []string) {
	out = cssUnsupportedRE.ReplaceAllStringFunc(css, func(line string) string {
		if t := strings.TrimSpace(line); t != `@import "tailwindcss";` && t != `@import 'tailwindcss';` {
			dropped = append(dropped, t)
		}
		return ""
	})
	return out, dropped
}

// 平台的 default.css（shadcn 色板）去掉 @import "tailwindcss"（browser 版自带）和 typography 插件（browser 版加载不了，prose 用不了）
var baseCSS, _ = browserCSS(defaultCSS)
