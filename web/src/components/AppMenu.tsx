import { useEffect, useRef, useState } from "react";
import {
  BarChart3,
  Check,
  ChevronDown,
  ChevronRight,
  Info,
  Languages,
  Link2,
  MonitorDown,
  Moon,
  Settings,
  Sun,
} from "lucide-react";
import { post } from "@/lib/api";
import { ConnectDialog } from "@/components/SetupPage";
import { cn } from "@/lib/utils";
import { t } from "@/lib/i18n";
import { navigate } from "@/lib/router";
import { useInstall } from "@/lib/pwa";
import { InstallHelp } from "@/components/InstallHelp";

type Lang = "auto" | "zh" | "en";
const LANGS: { value: Lang; label: string }[] = [
  { value: "auto", label: "跟随系统" },
  { value: "zh", label: "中文" },
  { value: "en", label: "English" },
];

/** 左上角 logo：Annulo 自己的菜单（设置、用量、深浅色、语言、关于），连不连 creght 都在；没连时多一项「连接 creght」。
 *  设置和用量主要从运营后台里的入口打开（弹窗），这里是兜底：模板还没升级、没有那个入口时也进得去。
 *  语言在这里能直接切（和 设置 → 语言 同一个接口），切完 onLanguage 重拉 status，App 按 status.locale 换语言。
 *  服务端部署的「连接 creght」去 设置 → 连接：那里有个人访问令牌、自己部署的 creght，用 IP 打开时也连得上 */
export function AppMenu({
  theme,
  onToggleTheme,
  loggedIn,
  viewer = false,
  server = false,
  language = "auto",
  canSetLanguage = true,
  onLanguage,
}: {
  theme: "dark" | "light";
  onToggleTheme: () => void;
  loggedIn: boolean;
  viewer?: boolean;
  server?: boolean;
  language?: Lang;
  canSetLanguage?: boolean; // 服务端部署时语言是整个服务器的设置，只有管理员能改
  onLanguage: () => void;
}) {
  const [open, setOpen] = useState(false);
  const [langOpen, setLangOpen] = useState(false);
  const [connecting, setConnecting] = useState(false);
  const [installHelp, setInstallHelp] = useState(false);
  const install = useInstall();
  const root = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return setLangOpen(false);
    const onDown = (e: MouseEvent) => {
      if (!root.current?.contains(e.target as Node)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && setOpen(false);
    // 点进运营后台 iframe 时外层收不到 mousedown，但窗口会失焦
    const onBlur = () =>
      setTimeout(
        () =>
          document.activeElement instanceof HTMLIFrameElement && setOpen(false),
      );
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey);
    window.addEventListener("blur", onBlur);
    return () => {
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("keydown", onKey);
      window.removeEventListener("blur", onBlur);
    };
  }, [open]);

  const setLanguage = async (v: Lang) => {
    setOpen(false);
    if (v === language) return;
    try {
      await post("settings/language", { language: v }, "PUT");
    } finally {
      onLanguage();
    }
  };

  const mac = /Mac/.test(navigator.platform);
  // 只读账号（服务端部署）进不了设置：只留深浅色
  const items = [
    ...(viewer
      ? []
      : [
          {
            label: t("设置"),
            icon: Settings,
            go: () => navigate("settings"),
            hint: mac ? "⌘," : "Ctrl+,",
          },
          {
            label: t("用量"),
            icon: BarChart3,
            go: () => navigate("settings", "#usage"),
          },
        ]),
    {
      label: theme === "dark" ? t("切换到浅色") : t("切换到深色"),
      icon: theme === "dark" ? Sun : Moon,
      go: onToggleTheme,
    },
    // 在浏览器里打开的（远程访问、服务端部署）：加到桌面当 App 用。能弹系统的安装框就弹，不能就告诉用户怎么点
    ...(install.available
      ? [
          {
            label: t("添加到桌面"),
            icon: MonitorDown,
            go: () => {
              if (install.canPrompt) void install.prompt();
              else setInstallHelp(true);
            },
          },
        ]
      : []),
    ...(viewer
      ? []
      : [
          {
            label: t("关于 Annulo"),
            icon: Info,
            go: () => navigate("settings", "#about"),
          },
        ]),
  ];

  return (
    <div ref={root} className="relative shrink-0">
      <button
        onClick={() => setOpen((v) => !v)}
        aria-label={t("Annulo 菜单")}
        aria-expanded={open}
        aria-haspopup="menu"
        className={cn(
          "flex h-9 items-center gap-2 rounded-lg px-1.5 outline-none transition-colors hover:bg-accent focus-visible:ring-[3px] focus-visible:ring-ring/50",
          open && "bg-accent",
        )}
      >
        <img
          src={
            theme === "dark"
              ? "/_shuttle/annulo-dark.svg"
              : "/_shuttle/annulo-light.svg"
          }
          alt=""
          className="h-4"
        />
        <span className="text-xs font-bold">Annulo</span>
        <ChevronDown className="size-3.5 text-muted-foreground" />
      </button>
      {open && (
        <div
          role="menu"
          className="absolute top-full left-0 z-50 mt-1 w-60 rounded-xl border border-border bg-popover p-1.5 text-sm text-popover-foreground shadow-lg"
        >
          {items.map((it) => (
            <button
              key={it.label}
              role="menuitem"
              onClick={() => {
                it.go();
                setOpen(false);
              }}
              className="flex w-full items-center gap-2 rounded-lg px-2.5 py-2 text-left outline-none hover:bg-accent focus-visible:bg-accent"
            >
              <it.icon className="size-4" />
              <span className="flex-1">{it.label}</span>
              {it.hint && (
                <span className="text-xs text-muted-foreground">{it.hint}</span>
              )}
            </button>
          ))}
          {canSetLanguage && (
            <button
              role="menuitem"
              aria-expanded={langOpen}
              onClick={() => setLangOpen((v) => !v)}
              className="flex w-full items-center gap-2 rounded-lg px-2.5 py-2 text-left outline-none hover:bg-accent focus-visible:bg-accent"
            >
              <Languages className="size-4" />
              <span className="flex-1">{t("语言")}</span>
              <span className="text-xs text-muted-foreground">
                {t(
                  LANGS.find((l) => l.value === language)?.label ?? "跟随系统",
                )}
              </span>
              <ChevronRight
                className={cn(
                  "size-3.5 text-muted-foreground transition-transform",
                  langOpen && "rotate-90",
                )}
              />
            </button>
          )}
          {canSetLanguage &&
            langOpen &&
            LANGS.map((l) => (
              <button
                key={l.value}
                role="menuitemradio"
                aria-checked={l.value === language}
                onClick={() => setLanguage(l.value)}
                className="flex w-full items-center gap-2 rounded-lg py-1.5 pr-2.5 pl-8.5 text-left outline-none hover:bg-accent focus-visible:bg-accent"
              >
                <span className="flex-1">
                  {l.value === "auto" ? t(l.label) : l.label}
                </span>
                {l.value === language && <Check className="size-3.5" />}
              </button>
            ))}
          {!loggedIn && !viewer && (
            <>
              <div className="my-1 h-px bg-border" />
              <button
                role="menuitem"
                onClick={() => {
                  setOpen(false);
                  if (server) navigate("settings", "#connections");
                  else setConnecting(true);
                }}
                className="flex w-full items-center gap-2 rounded-lg px-2.5 py-2 text-left outline-none hover:bg-accent focus-visible:bg-accent"
              >
                <Link2 className="size-4" />
                {t("连接 creght")}
              </button>
            </>
          )}
        </div>
      )}
      {connecting && <ConnectDialog onClose={() => setConnecting(false)} />}
      {installHelp && <InstallHelp onClose={() => setInstallHelp(false)} />}
    </div>
  );
}
