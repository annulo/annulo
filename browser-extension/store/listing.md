# Chrome 应用商店资料

上传 `scripts/pack.sh` 打出来的 `dist/annulo-extension-<版本>.zip`，下面各段照着贴进开发者后台（https://chrome.google.com/webstore/devconsole）。
商店页面的语言：默认 English，再加「中文（简体）」。

## 商店页面（Store listing）

**名称**：Annulo（来自 manifest，不用填）

**类别**：Productivity → Tools（工具）

**简短说明**（manifest 的 description，自动带出，最多 132 字）：
- en：Lets Annulo on this computer work on the websites you allow, in tabs it opens itself.
- zh_CN：让这台电脑上的 Annulo 在你允许的网站上替你操作网页，只用它自己打开的标签页。

**详细说明（English）**：

```
Annulo is a desktop app that runs your business operations on your own computer: publishing posts, collecting data, checking how AI search engines answer questions about your company, and more.

Some of that work happens on websites without an API, where you are already signed in. This extension lets Annulo do it in your own Chrome, with your existing sign-in, instead of asking you to sign in again in a separate browser.

How it stays limited:
• It only works with the Annulo desktop app running on the same computer. It talks to it through Chrome's native messaging; nothing goes through our servers.
• Annulo asks before it uses a website for the first time. You allow sites one by one, and you can see and remove them from the toolbar menu at any time.
• It only operates tabs it opened itself, in its own window. It never touches your other tabs.
• If a page leaves the sites you allowed, the extension stops controlling it.
• It never reads or changes your cookies, and never reads data from other sites.

Requires the Annulo desktop app: https://annulo.dev
```

**详细说明（中文）**：

```
Annulo 是在你自己电脑上运行的运营助手桌面应用：发布帖子、采集数据、检测 AI 搜索怎么回答关于你公司的问题，等等。

有些工作要在没有接口的网站上做，而你已经在 Chrome 里登录了这些网站。装了这个插件，Annulo 就能直接在你的 Chrome 里、用你已有的登录完成这些操作，不用在另一个浏览器里重新登录。

它能做的事是受限的：
• 只和同一台电脑上运行的 Annulo 桌面应用配合，通过 Chrome 的本机通信连接，不经过我们的服务器。
• 第一次要用某个网站时，Annulo 会先问你。网站一个一个授权，随时可以在工具栏菜单里查看、移除。
• 只操作它自己打开的标签页，在它自己的窗口里，不碰你的其他标签页。
• 页面离开你允许的网站，插件就不再控制它。
• 不读取、不修改你的 cookie，不读取其他网站的数据。

需要安装 Annulo 桌面应用：https://annulo.dev
```

**图片**：
- 图标：`extension/icons/icon-128.png`
- 截图（至少 1 张，1280×800）：设置 → 浏览器 页；网站授权弹窗（grant.html）；工具栏菜单（popup，已允许的网站）；Annulo 在插件窗口里工作的样子
- 宣传小图（440×280）

**官网**：https://annulo.dev
**支持**：hi@creght.com

## 隐私（Privacy practices）

**单一用途（Single purpose）**：

```
Let the Annulo desktop app on the same computer operate web pages in the user's own Chrome, only on websites the user has allowed and only in tabs the extension opens itself.
```

**权限理由（Permission justification）**：

| 权限 | 理由 |
|---|---|
| debugger | Annulo operates pages for the user (click, type, read page content) through the Chrome DevTools Protocol. The extension only attaches to tabs it opened itself, only while they are on sites the user allowed, and forwards only an allow-list of commands; cookie, storage and browser-wide commands are always refused. |
| nativeMessaging | The only way the extension talks to the Annulo desktop app on the same computer (native host com.annulo.browser). No remote server is involved. |
| tabs | To open, find and close the extension's own tabs and window, and to stop controlling a tab when it navigates away from the allowed sites. |
| storage | To keep a random installation ID (so Annulo can tell several Chrome profiles apart) and which tabs the extension opened, across service worker restarts. |
| alarms | To reconnect to the Annulo desktop app periodically, since the service worker can be stopped by Chrome. |
| 网站权限（optional host permissions `https://*/*`、`http://*/*`） | Optional and requested one site at a time: the first time Annulo needs a website, the user is asked to allow that site only. The extension refuses to operate any site the user has not allowed. Users can remove sites from the toolbar menu. |

**远程代码（Remote code）**：No, I am not using remote code.

**数据使用（Data usage）**：勾「Website content」（插件把用户允许的网站的页面内容交给本机的 Annulo）。其余都不勾。
下面三条声明都勾上：
- I do not sell or transfer user data to third parties, outside of the approved use cases
- I do not use or transfer user data for purposes that are unrelated to my item's single purpose
- I do not use or transfer user data to determine creditworthiness or for lending purposes

**隐私政策链接**：https://annulo.dev/privacy （「Annulo Chrome 插件」一节）

## 上架以后

商店版的 id 是 `gbionmhpkofgoeheaibcmgikpboddbgp`（和开发版不同，上传草稿就有）：
1. ~~加进 `internal/extbridge/bridge.go` 的 `ExtensionIDs`，发一版 Annulo~~（2026-10-14 已加）；
2. 审核通过后，`web/src/components/BrowserSettings.tsx` 的 `EXTENSION_LISTED` 改成 `true`；
3. 官网 `help/platform.ts` 填 `EXTENSION_STORE`，发布 `/help/browser`。
