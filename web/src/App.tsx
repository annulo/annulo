import { useCallback, useEffect, useRef, useState } from "react";
import {
  ArrowDownToLine,
  LayoutDashboard,
  Loader,
  MessageSquare,
  RotateCw,
  TriangleAlert,
  X,
} from "lucide-react";
import { getJSON, post, type Status } from "@/lib/api";
import { cn } from "@/lib/utils";
import { navigate, ROOT_MODE, useView, type View } from "@/lib/router";
import { useTheme } from "@/lib/theme";
import { usePageMonitor } from "@/lib/pageMonitor";
import { useDesktopUpdate } from "@/lib/desktopUpdate";
import {
  BackendFrame,
  backendPath,
  TabStrip,
  type BackendTab,
} from "@/components/BackendTabs";
import { Button } from "@/components/ui/button";
import { Tip } from "@/components/ui/tip";
import ChatPanel from "@/components/ChatPanel";
import { Toaster } from "@/components/ui/sonner";
import SettingsPage from "@/components/SettingsPage";
import SetupPage from "@/components/SetupPage";
import StartLink, { isStartLink } from "@/components/StartLink";
import { AccountMenu } from "@/components/AccountMenu";
import { ServerAccount } from "@/components/ServerAccount";
import BrowserIndicator from "@/components/BrowserIndicator";
import RemoteIndicator from "@/components/RemoteIndicator";
import { AppMenu } from "@/components/AppMenu";
import { WorkspaceSwitcher } from "@/components/WorkspaceSwitcher";
import { UpdateDialog } from "@/components/UpdateDialog";
import { GitNotice } from "@/components/GitNotice";
import { setLocale, t, useLocale } from "@/lib/i18n";
import { msgType } from "./lib/brand";
import { runtime } from "@/lib/runtime";

// 地址栏直接打开后台子页面时，服务端会把它转成 /_shuttle/?path=<原路径>；远程打开时外壳就在原路径上（ROOT_MODE）
const initialPath =
  new URLSearchParams(location.search).get("path") ||
  (ROOT_MODE && !new URLSearchParams(location.search).has("settings")
    ? location.pathname + location.search
    : "/");

const NAV: { view: View; label: string }[] = [
  { view: "backend", label: "运营后台" },
  { view: "settings", label: "设置" },
];

/**
 * 当前标签里换到 path，不重新加载：和现在是同一个页面（路径一样，只差查询参数 / hash）时，在 iframe 里 pushState 再发 popstate，
 * 页面按自己的路由切过去（模板按 popstate 重读参数），也记进后台的前进/后退（backendHistory 包了 pushState）。
 * 英文页面的地址带 /en 前缀，链接里没写的照当前页面补上。不是同一个页面、读不了 iframe 时返回 false，由调用方整页加载
 */
function softNavigate(f: HTMLIFrameElement, path: string): boolean {
  try {
    const w = f.contentWindow;
    if (!w || w.location.origin !== location.origin) return false;
    const cur = new URL(w.location.href);
    const to = new URL(path, cur);
    const locale = /^\/en(?=\/|$)/.exec(cur.pathname)?.[0] ?? "";
    if (locale && !to.pathname.startsWith(locale))
      to.pathname = (locale + to.pathname).replace(/\/$/, "") || "/";
    const bare = (p: string) =>
      p.replace(/^\/en(?=\/|$)/, "").replace(/\/$/, "") || "/";
    if (bare(cur.pathname) !== bare(to.pathname)) return false;
    if (to.href === cur.href) return true;
    w.history.pushState(null, "", to.href);
    w.dispatchEvent(new PopStateEvent("popstate", { state: null }));
    if (cur.hash !== to.hash)
      w.dispatchEvent(
        new HashChangeEvent("hashchange", {
          oldURL: cur.href,
          newURL: to.href,
        }),
      );
    return true;
  } catch {
    return false;
  }
}

/** 外壳：顶栏 + 运营后台（占满主区域）+ 右侧 AI 面板；设置（用量也在里面）是盖在上面的弹窗 */
export default function App() {
  const [status, setStatus] = useState<Status | null>(null);
  const [statusErr, setStatusErr] = useState("");
  // 窗口标题（App 的标题栏、浏览器标签）：Annulo · 当前项目的名字，没选项目就是 Annulo
  const projectName = status?.backend?.name || "";
  useEffect(() => {
    document.title = projectName ? `Annulo · ${projectName}` : "Annulo";
  }, [projectName]);
  const [view] = useView();
  const [theme, toggleTheme] = useTheme();
  // 运营后台的标签页；frame 总是指向当前标签的 iframe（刷新、报错监视、切语言都对它）
  const [tabs, setTabs] = useState<BackendTab[]>([
    { id: "main", src: initialPath },
  ]);
  const [active, setActive] = useState("main");
  const frames = useRef(new Map<string, HTMLIFrameElement>());
  const frame = useRef<HTMLIFrameElement | null>(null);
  const [activeFrame, setActiveFrame] = useState<HTMLIFrameElement | null>(
    null,
  );
  const onFrame = useCallback((id: string, el: HTMLIFrameElement | null) => {
    if (el) frames.current.set(id, el);
    else frames.current.delete(id);
  }, []);
  useEffect(() => {
    frame.current = frames.current.get(active) ?? null;
    setActiveFrame(frame.current);
  }, [active, tabs]);
  const openTab = useCallback((src = "/") => {
    const id = Math.random().toString(36).slice(2);
    setTabs((ts) => [...ts, { id, src }]);
    setActive(id);
  }, []);
  const closeTab = useCallback(
    (id: string) => {
      if (tabs.length < 2) return;
      const at = tabs.findIndex((x) => x.id === id);
      const rest = tabs.filter((x) => x.id !== id);
      setTabs(rest);
      if (active === id) setActive(rest[Math.max(0, at - 1)].id);
    },
    [tabs, active],
  );
  // 订阅界面语言：切换时整棵外壳重新渲染（各组件直接用 t）
  useLocale();

  const loadStatus = useCallback(() => {
    getJSON<Status>("status")
      .then((s) => {
        runtime.remote = !!s.remote;
        runtime.server = !!s.server;
        setStatus(s);
        setStatusErr("");
      })
      .catch((e) => setStatusErr(e.message));
  }, []);
  useEffect(loadStatus, [loadStatus]);

  // 刷新时保留后台当前所在的页面和查询参数（比如选中的渠道）
  const reloadFrame = (f: HTMLIFrameElement | null) => {
    try {
      f?.contentWindow?.location.reload();
    } catch {
      if (f) f.src = initialPath;
    }
  };
  const reloadSite = useCallback(() => reloadFrame(frame.current), []);
  // 顶栏的刷新按钮：先清掉本地渲染的编译缓存、重新构建（缓存出岔子时点一下就能恢复，不用重启 App），再刷新页面
  const [refreshing, setRefreshing] = useState(false);
  // 窄屏（手机）上后台和助手放不下两栏：一次显示一个，底部切换。页面让右侧打开对话、开始一段对话时切到助手
  const [pane, setPane] = useState<"backend" | "chat">("backend");
  useEffect(() => {
    const toChat = () => setPane("chat");
    const onMsg = (e: MessageEvent) => {
      const ty = msgType(e.data);
      if (
        e.origin === location.origin &&
        (ty === "shuttle:open-chat" || ty === "shuttle:compose" || ty === "shuttle:ask")
      )
        toChat();
    };
    window.addEventListener("shuttle:open-chat", toChat);
    window.addEventListener("message", onMsg);
    return () => {
      window.removeEventListener("shuttle:open-chat", toChat);
      window.removeEventListener("message", onMsg);
    };
  }, []);
  const refreshSite = useCallback(async () => {
    setRefreshing(true);
    try {
      await post("local/render/reset").catch(() => {});
    } finally {
      setRefreshing(false);
      reloadSite();
    }
  }, [reloadSite]);
  // 后台代码变了：每个标签都要拿到新代码
  const reloadAll = useCallback(() => frames.current.forEach(reloadFrame), []);

  // 业务表变了由 Annulo 直接推给页面（internal/server/datachanged.go，页面运行时转成 db:changed），外壳不再按助手跑的命令猜。

  // 快捷键：外壳窗口和每个标签的 iframe 窗口都挂（焦点在 iframe 里时按键发给 iframe）。
  //   Esc 关掉设置 / 用量弹窗；⌘, / Ctrl+, 打开设置（macOS 原生菜单的「设置…」发 shuttle:open-settings）；
  //   ⌘T 新标签页；⌘1–⌘8 切到第几个标签、⌘9 最后一个（和浏览器一样）；⌘W 关掉当前标签（Electron 的菜单先收到，发 shuttle:close-tab 过来）
  const dialog = useRef<HTMLDivElement>(null);
  const onKey = (e: KeyboardEvent) => {
    const mod = e.metaKey || e.ctrlKey;
    if (e.key === "Escape" && view !== "backend") navigate("backend");
    else if (mod && e.key === ",") {
      e.preventDefault();
      navigate("settings");
    } else if (mod && !e.shiftKey && !e.altKey && e.key.toLowerCase() === "t") {
      e.preventDefault();
      openTab();
    } else if (mod && !e.shiftKey && !e.altKey && /^[1-9]$/.test(e.key)) {
      e.preventDefault();
      const n = Number(e.key);
      const tab = n === 9 ? tabs.at(-1) : tabs[n - 1];
      if (tab) setActive(tab.id);
    }
  };
  const onKeyRef = useRef(onKey);
  onKeyRef.current = onKey;
  const onFrameKey = useCallback((e: KeyboardEvent) => onKeyRef.current(e), []);
  useEffect(() => {
    const open = () => navigate("settings");
    // Electron 的 ⌘W：有别的标签就关当前这个（preventDefault 告诉它别关窗口）
    const close = (e: Event) => {
      if (tabs.length < 2) return;
      e.preventDefault();
      closeTab(active);
    };
    // Electron 里 ⌘ 点击后台链接、target=_blank：本机地址交给这里开标签
    const openFromShell = (e: Event) => {
      const path = backendPath(String((e as CustomEvent<string>).detail));
      if (path) openTab(path);
    };
    // 对话里点了指向项目页面的链接（ChatPanel 的按钮）：当前标签跳过去，⌘ 点开新标签；设置开着就先回到后台；手机上切到后台
    const openPath = (e: Event) => {
      const d = (e as CustomEvent<{ path?: string; newTab?: boolean }>).detail;
      const path = backendPath(String(d?.path ?? ""));
      if (!path) return;
      navigate("backend");
      setPane("backend");
      const f = frame.current;
      if (d?.newTab || !f) return openTab(path);
      if (!softNavigate(f, path)) f.src = path;
    };
    // Electron 的 ⌘R：只刷新当前标签的后台 iframe，不整窗刷新（preventDefault 告诉它刷过了）
    const reload = (e: Event) => {
      if (!frame.current) return;
      e.preventDefault();
      reloadSite();
    };
    window.addEventListener("keydown", onFrameKey);
    window.addEventListener("shuttle:open-settings", open);
    window.addEventListener("shuttle:reload-site", reload);
    window.addEventListener("shuttle:close-tab", close);
    window.addEventListener("shuttle:open-tab", openFromShell);
    window.addEventListener("shuttle:open-path", openPath);
    return () => {
      window.removeEventListener("keydown", onFrameKey);
      window.removeEventListener("shuttle:open-settings", open);
      window.removeEventListener("shuttle:reload-site", reload);
      window.removeEventListener("shuttle:close-tab", close);
      window.removeEventListener("shuttle:open-tab", openFromShell);
      window.removeEventListener("shuttle:open-path", openPath);
    };
  }, [onFrameKey, tabs.length, active, closeTab, openTab, reloadSite]);
  useEffect(() => {
    if (view !== "backend") dialog.current?.focus();
  }, [view]);

  // 设置 → 项目 里模板升级合并成功：整页刷新左侧后台，拿到新代码
  useEffect(() => {
    const f = (e: MessageEvent) =>
      e.origin === location.origin &&
      msgType(e.data) === "shuttle:reload-backend" &&
      reloadAll();
    window.addEventListener("message", f);
    return () => window.removeEventListener("message", f);
  }, [reloadAll]);

  // 运营后台页面里的「去设置」「用量」之类：postMessage 过来，外壳打开对应的弹窗
  useEffect(() => {
    const f = (e: MessageEvent) => {
      if (
        e.origin !== location.origin ||
        msgType(e.data) !== "shuttle:navigate"
      )
        return;
      // 用量现在是设置里的一节：模板发 view: 'usage' 的转到 设置 → 用量
      if (e.data.view === "usage") return navigate("settings", "#usage");
      const v = e.data.view as View;
      if (NAV.some((n) => n.view === v))
        navigate(v, typeof e.data.hash === "string" ? e.data.hash : "");
    };
    window.addEventListener("message", f);
    return () => window.removeEventListener("message", f);
  }, []);

  // 界面语言：服务端算好的 locale 为准（「跟随系统」也由服务端按系统语言算出）。左侧后台（creght 多语言站点）
  // 按同源的 CREGHT_LOCALE cookie 选语言，代理只放行这一个 cookie。cookie 总是写成实际语言，外壳和后台才不会一个中文一个英文。
  // 变了之后不能原地刷新：后台地址里的 /en 前缀比 cookie 优先，要把当前地址的语言前缀换掉再跳过去（保留页面和参数）
  useEffect(() => {
    if (!status?.locale) return;
    setLocale(status.locale);
    const want = status.locale;
    const had =
      document.cookie.match(/(?:^|;\s*)CREGHT_LOCALE=(\w+)/)?.[1] ?? "";
    if (want === had) return;
    document.cookie = `CREGHT_LOCALE=${want}; path=/; max-age=31536000; samesite=lax`;
    frames.current.forEach((f) => {
      let path = initialPath;
      try {
        // 还没加载完时是 about:blank，不能当成后台的页面路径
        const l = f.contentWindow!.location;
        if (l.protocol.startsWith("http"))
          path = l.pathname + l.search + l.hash;
      } catch {
        // 跨域或还没加载：用初始地址
      }
      path = path.replace(/^\/en(?=\/|\?|#|$)/, "") || "/";
      if (!path.startsWith("/")) path = "/" + path;
      f.src = want === "en" ? "/en" + (path === "/" ? "" : path) : path;
    });
  }, [status?.locale]);

  // 后台页面的报错：报给 Annulo 供助手用 page_errors 查（不自动塞给助手），这里只给用户一个「交给助手修复」
  const page = usePageMonitor(
    activeFrame,
    reloadSite,
    status?.backend?.project_id,
  );
  const update = useDesktopUpdate();
  const [updateOpen, setUpdateOpen] = useState(false);
  // 菜单「检查更新…」发现新版本时打开更新弹窗
  useEffect(() => {
    const open = () => setUpdateOpen(true);
    window.addEventListener("shuttle:open-update", open);
    return () => window.removeEventListener("shuttle:open-update", open);
  }, []);
  const upd = update.state;
  const updPct = upd?.bytes
    ? Math.round(((upd.received ?? 0) / upd.bytes) * 100)
    : 0;
  // 有新版本：主界面放在顶栏；登录、选项目页没有顶栏，浮在右上角
  const updateButton = upd && upd.state !== "idle" && (
    <Tip
      label={t("新版本 {v}（当前 {current}）", {
        v: upd.version ?? "",
        current: upd.current,
      })}
    >
      <Button
        size="sm"
        variant={
          upd.state === "downloading" || upd.state === "installing"
            ? "outline"
            : "default"
        }
        onClick={() => setUpdateOpen(true)}
        className="h-7 gap-1.5 rounded-lg px-2.5 text-xs"
      >
        {upd.state === "installing" ? (
          <Loader className="size-3.5 animate-spin" />
        ) : (
          <ArrowDownToLine className="size-3.5" />
        )}
        {upd.state === "installing"
          ? t("正在安装…")
          : upd.state === "downloading"
            ? t("下载中 {pct}%", { pct: updPct })
            : upd.state === "ready"
              ? t("安装更新")
              : t("更新到 {v}", { v: upd.version ?? "" })}
      </Button>
    </Tip>
  );
  const updateDialog = updateOpen && upd && upd.state !== "idle" && (
    <UpdateDialog
      update={upd}
      onDownload={update.download}
      onInstall={update.install}
      onClose={() => setUpdateOpen(false)}
    />
  );
  const floatingUpdate = updateButton && (
    <>
      <div className="fixed right-4 top-3 z-40">{updateButton}</div>
      {updateDialog}
    </>
  );
  const [dismissed, setDismissed] = useState("");
  const pageErrKey = page.errors.length
    ? `${page.loadId}:${page.errors.length}`
    : "";
  const askFix = () => {
    const detail = page.errors
      .slice(0, 3)
      .map((e) => [e.message, e.stack].filter(Boolean).join("\n"))
      .join("\n\n");
    const text = `${t("左侧后台的页面 {path} 报错了，帮我看看、修一下：", { path: page.path || "/" })}\n\n\`\`\`\n${detail}\n\`\`\``;
    window.postMessage({ type: "shuttle:ask", text }, location.origin);
    setDismissed(pageErrKey);
  };

  // 第一次打开直接选项目、新建项目（默认离线，不用登录）；creght 在 设置 → 连接 里，连不连都能用
  if (status?.setup_required)
    return (
      <>
        <GitNotice
          git={status.git}
          onStatus={setStatus}
          className="fixed inset-x-0 top-0 z-40 border-b"
        />
        <SetupPage
          loggedIn={status.creght.logged_in}
          onDone={() => location.reload()}
        />
        {floatingUpdate}
        {isStartLink() && <StartLink />}
      </>
    );

  return (
    <div className="flex h-dvh min-w-0 flex-col overflow-hidden bg-background text-foreground">
      {/* 顶栏像 Chrome 的标签条：用侧栏色，当前标签用页面色和下面连成一片、贴着底边（上面空 8px），所以标签的文字比顶栏中线低 4px；
          logo、项目名和右边的按钮跟着下移 4px（translate-y-1），和标签的文字在一条线上 */}
      {/* 从桌面打开（PWA，状态栏透明）时内容会顶到状态栏下面：顶栏让出 safe-area */}
      <header className="relative z-40 grid h-[calc(2.75rem+env(safe-area-inset-top))] shrink-0 grid-cols-[minmax(0,1fr)_auto] gap-3 bg-sidebar px-4 pt-[env(safe-area-inset-top)]">
        <div className="flex h-full min-w-0 items-center gap-4">
          <div className="shrink-0 translate-y-1">
            <AppMenu
              theme={theme}
              onToggleTheme={toggleTheme}
              loggedIn={!!status?.creght.logged_in}
              viewer={status?.user?.role === "viewer"}
              server={!!status?.user}
              language={status?.language}
              canSetLanguage={!status?.user || status.user.role === "admin"}
              onLanguage={loadStatus}
            />
          </div>
          {status?.backend && (
            <>
              <span
                className="-mx-2 shrink-0 translate-y-1 text-muted-foreground/60"
                aria-hidden
              >
                /
              </span>
              <div className="shrink-0 translate-y-1">
                <WorkspaceSwitcher current={status.backend.project_id} remote={!!status.remote} />
              </div>
              <div className="-ml-px h-7 w-px shrink-0 translate-y-1 bg-border max-md:hidden" />
              {/* 不放运营后台的预览地址：后台要从 Annulo 里打开才能读写数据，单独打开预览会报错。手机上放不下标签条 */}
              <div className="contents max-md:hidden">
              <TabStrip
                tabs={tabs}
                active={active}
                frames={frames.current}
                onSelect={setActive}
                onClose={closeTab}
                onNew={() => openTab()}
              />
              </div>
            </>
          )}
        </div>

        <div className="flex h-full min-w-0 translate-y-1 items-center justify-end gap-3">
          {statusErr && (
            <span className="max-w-60 truncate text-xs text-destructive">
              {statusErr}
            </span>
          )}
          {status?.backend?.offline &&
            status.creght.logged_in &&
            !status.server && (
              <Tip
                label={t(
                  "这个项目是离线的：数据只在这台电脑上。在 设置 → 项目 里可以转成在线项目。",
                )}
              >
                <span className="rounded-md border border-border bg-muted px-2 py-1 text-xs font-semibold text-muted-foreground">
                  {t("离线项目")}
                </span>
              </Tip>
            )}
          {updateButton}
          {!status?.remote && !status?.server && <RemoteIndicator />}
          <BrowserIndicator />
          <Tip label={t("刷新页面")}>
            <Button
              variant="ghost"
              size="icon-sm"
              onClick={refreshSite}
              disabled={refreshing}
              aria-label={t("刷新页面")}
              className="rounded-lg text-muted-foreground hover:text-foreground"
            >
              <RotateCw
                className={cn("size-[18px]", refreshing && "animate-spin")}
              />
            </Button>
          </Tip>
          {status?.creght.user && (
            <AccountMenu
              user={status.creght.user}
              host={status.creght.api_host}
              onLoggedOut={loadStatus}
            />
          )}
          {status?.user && <ServerAccount user={status.user} />}
        </div>
      </header>
      <GitNotice
        git={status?.git}
        onStatus={setStatus}
        className="shrink-0 border-b"
      />
      {status?.update && !status?.cert && (
        <div className="flex shrink-0 items-center gap-3 border-b border-primary/20 bg-primary/5 px-4 py-2 text-xs">
          <span className="min-w-0 flex-1 truncate">
            {t("Annulo 有新版本 {v}（现在是 {current}）", {
              v: status.update.latest,
              current: status.update.current,
            })}
          </span>
          <Button
            size="sm"
            variant="outline"
            onClick={() => navigate("settings", "#about")}
          >
            {t("看看怎么升级")}
          </Button>
        </div>
      )}
      {status?.cert && (
        // 服务端部署：证书的事要管理员处理（状态旁边给处理入口）
        <div className="flex shrink-0 items-center gap-3 border-b border-warn/30 bg-warn/10 px-4 py-2 text-xs text-warn">
          <span className="min-w-0 flex-1 truncate">
            {status.cert.kind === "no_domain"
              ? t(
                  "现在用 IP 打开，没有 HTTPS，登录密码走的是明文：绑一个域名，Annulo 会自己申请证书。",
                )
              : status.cert.kind === "error"
                ? t("{domain} 的证书签不下来：{message}", {
                    domain: status.cert.domain ?? "",
                    message: status.cert.message ?? "",
                  })
                : t("{domain} 的证书 {d} 天后到期，自动续期还没成功。", {
                    domain: status.cert.domain ?? "",
                    d: status.cert.days_left ?? 0,
                  })}
          </span>
          <Button
            size="sm"
            variant="outline"
            onClick={() => navigate("settings", "#domain")}
          >
            {status.cert.kind === "no_domain" ? t("去绑域名") : t("去处理")}
          </Button>
        </div>
      )}
      {status?.project_requires && (
        // 项目在别处用新版 Annulo 升级过模板：这里的 Annulo 跑不了它用到的新能力
        <div className="shrink-0 border-b border-amber-500/30 bg-amber-500/10 px-4 py-2 text-xs text-amber-700 dark:text-amber-400">
          {t(
            "这个项目需要新版 Annulo（能力版本 {need}，这台是 {have}）：部分功能可能用不了。请更新 Annulo。",
            { need: status.project_requires, have: status.api ?? 0 },
          )}
        </div>
      )}

      <div className="flex min-h-0 flex-1">
        <main
          className={cn(
            "relative min-w-0 flex-1 bg-background",
            pane === "chat" && status?.backend && "max-md:hidden",
          )}
        >
          {/* 只读账号（服务端部署）用不了助手、也不是来修页面的：不出报错条 */}
          {view === "backend" &&
            pageErrKey &&
            dismissed !== pageErrKey &&
            status?.user?.role !== "viewer" && (
              <div className="absolute inset-x-3 top-3 z-10 flex items-center gap-3 rounded-lg border border-destructive/30 bg-background/95 px-3 py-2 text-sm shadow-sm backdrop-blur">
                <TriangleAlert className="size-4 shrink-0 text-destructive" />
                <div className="flex min-w-0 flex-1 items-baseline gap-2">
                  <span className="shrink-0 font-semibold">
                    {t("后台页面出错了")}
                  </span>
                  <span
                    className="min-w-0 truncate text-xs text-muted-foreground"
                    title={page.errors[0]?.message}
                  >
                    {page.errors[0]?.message}
                    {page.errors.length > 1
                      ? t(" 等 {n} 个", { n: page.errors.length })
                      : ""}
                  </span>
                </div>
                <Button size="sm" onClick={askFix}>
                  {t("交给助手修复")}
                </Button>
                <Button
                  variant="ghost"
                  size="icon"
                  className="size-7"
                  aria-label={t("关闭")}
                  onClick={() => setDismissed(pageErrKey)}
                >
                  <X />
                </Button>
              </div>
            )}
          {/* 后台 iframe 常驻：切到用量 / 设置、别的标签再回来，不用重新加载、也不丢当前渠道 */}
          {tabs.map((tab) => (
            <BackendFrame
              key={tab.id}
              tab={tab}
              active={tab.id === active}
              project={status?.backend?.project_id}
              loggedIn={status?.creght.logged_in}
              onFrame={onFrame}
              onKey={onFrameKey}
            />
          ))}
        </main>

        {/* 只读账号（服务端部署）不能用助手：不显示对话面板 */}
        {status?.backend && status.user?.role !== "viewer" && (
          // 手机上占满一屏（面板自己记的宽度不管用）
          <div
            className={cn(
              "flex min-w-0 max-md:flex-1 max-md:[&>aside]:!w-full max-md:[&>aside]:border-l-0",
              pane !== "chat" && "max-md:hidden",
            )}
          >
            <ChatPanel
              key={status.backend.project_id}
              workspace={status.backend.project_id}
              llm={status.llm}
              onOpenSettings={() => navigate("settings", "#llm")}
              onLLMChanged={loadStatus}
              assistant={status?.assistant}
            />
          </div>
        )}
      </div>
      {status?.backend && status.user?.role !== "viewer" && (
        <nav className="flex shrink-0 border-t border-border bg-sidebar pb-[env(safe-area-inset-bottom)] md:hidden">
          {(
            [
              ["backend", LayoutDashboard, t("后台")],
              ["chat", MessageSquare, t("助手")],
            ] as const
          ).map(([key, Icon, label]) => (
            <button
              key={key}
              type="button"
              onClick={() => setPane(key)}
              aria-pressed={pane === key}
              className={cn(
                "flex flex-1 cursor-pointer flex-col items-center gap-0.5 py-2 text-[11px]",
                pane === key ? "text-primary" : "text-muted-foreground",
              )}
            >
              <Icon className="size-5" />
              {label}
            </button>
          ))}
        </nav>
      )}
      {/* 设置（含用量）是次要的：从运营后台里的入口（或右上角头像菜单）打开，弹窗盖在后台上，关掉回到原处 */}
      {view !== "backend" && status?.user?.role !== "viewer" && (
        <div
          className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4 backdrop-blur-[2px] sm:p-8"
          onMouseDown={(e) =>
            e.target === e.currentTarget && navigate("backend")
          }
        >
          <div
            role="dialog"
            aria-modal="true"
            aria-label={t(NAV.find((n) => n.view === view)?.label ?? "")}
            className="flex h-full max-h-[860px] w-full max-w-6xl flex-col overflow-hidden rounded-2xl border border-border bg-background shadow-2xl outline-none"
          >
            <div className="flex h-12 shrink-0 items-center justify-between border-b border-border px-4">
              <span className="text-sm font-bold">
                {t(NAV.find((n) => n.view === view)?.label ?? "")}
              </span>
              <Button
                variant="ghost"
                size="icon-sm"
                onClick={() => navigate("backend")}
                aria-label={t("关闭")}
                className="rounded-lg text-muted-foreground hover:text-foreground"
              >
                <X className="size-[18px]" />
              </Button>
            </div>
            <div className="min-h-0 flex-1">
              <SettingsPage
                onSaved={loadStatus}
                creght={!!status?.creght.logged_in}
                user={status?.user ?? undefined}
                remote={!!status?.remote}
              />
            </div>
          </div>
        </div>
      )}
      {isStartLink() && status && <StartLink />}
      {updateDialog}
      <Toaster position="top-center" />
    </div>
  );
}
