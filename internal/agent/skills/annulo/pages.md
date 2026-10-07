# 项目的页面

左侧显示的是项目的页面：Annulo 直接渲染工作目录里的页面代码，保存后几秒内自动刷新。

## 页面能调的本机接口

页面请求 `/_annulo/api/…`，要带 `X-Annulo: 1` 头（老项目里写的 `/_shuttle/api/`、`X-Shuttle` 也认）。项目的 `lib/` 里一般已经封装好，先看看有没有现成的。

| 接口 | 作用 |
|---|---|
| `POST local/run { fn, input }` | 执行本机函数，用 SSE 推 `progress` / `log`，最后推 `result` 或 `error`。页面关掉也会跑完 |
| `GET local/functions` | 能调的函数 |
| `POST local/tasks/<id>/run { input? }` | 开一段对话，把任务交给助手在后台跑，返回 `{ chat_id, task }`；同样的参数正在跑会返回 409（见 `tasks.md`） |
| `GET local/tasks` / `GET local/tasks/<id>` | 项目的任务：`list: [{ id, name, description, file, body, running: [{ chat_id, input, started_at }], last: { ok, error, chat_id, result, started_at, ms } }]` |
| `PUT local/tasks/<id> { body }` / `{ prompt }` / `{ reset_prompt: true }` | 改任务说明的正文（frontmatter 不动）/ 存用户改的写法（`user/prompts/<id>.md`）/ 恢复默认写法，都会在项目的 git 里提交 |
| `POST local/ask { text, title? }` | 「交给助手」：新开一段对话把这句话交给助手，在后台跑，返回 `{ chat_id }`；`GET agent/running` 的 `chats` 里还有它，就是还在跑 |
| `GET local/schedules` | 定时任务和每个任务上次运行的结果：`list: [{ id, fn, task, prompt, name, every, at, disabled, running, next_at, last: { started_at, ok, error, ms, result, chat_id } }]` |
| `GET local/llm/providers` | 设置 → 模型 里的服务商和模型（不含 key），页面上做「选哪个服务商 / 模型」用；本机函数里是 `ctx.llm.providers()` |
| `POST local/upload` | 上传文件，拿到公开地址（multipart，字段名 `file`，单个最大 200 MB）。在线项目传到项目站点的 creght 素材里；离线项目存在本机，地址是 `/_annulo/uploaded/…`（不带端口，原样存进表、写进页面）。返回 `{ url, path, size, content_type, existed }`（`existed` 表示同样的内容以前传过）。要给外部平台用的文件走它。**你自己要传本机文件（生成的图、下载的图）用 `annulo upload <文件…>`**，打印地址（`--json` 带大小、类型），走的是同一套 |
| `POST local/files` | 把文件存在**本机**、不传云端：请求体就是文件本身（`fetch(…, { method: 'POST', body: file, headers: { 'X-Annulo': '1', 'X-Filename': encodeURIComponent(file.name), 'content-type': file.type } })`，要显示进度就用 XMLHttpRequest），单个最大 8 GB，90 天后清理。返回 `{ ref: 'local:<name>', url: '/_annulo/files/<name>', filename, size }`：`ref` 存进表、交给本机函数（比如 `b.upload`），`url` 给页面预览（`<video src>`）。只在本机用的大文件走它；`DELETE local/files/<name>` 删除 |
| `PUT local/secrets { name, value }` | 写一个本机密钥（只能写，读不回来）。见下面「页面上让用户填密钥」 |
| `GET local/secrets?names=A,B` | 这几个密钥配了没有：`{ set: { A: true, B: false } }`，不返回值 |
| `GET/POST/PATCH/DELETE db/<表>` | 读写业务表（`?id=` 定位一行）。只放行 `tables/` 里声明的表 |
| `POST fetch { url, method, headers, body }` | 代页面请求外部地址（不能访问内网），页面上看一眼外部数据用；要 key、要批量的写成本机函数 |

**外站图片**：有防盗链的图床，页面里直接写 `<img src>` 会返回 403。改成 `<img src={'/_annulo/img?url=' + encodeURIComponent(图片地址)}>`，
由本机 Annulo 不带 Referer 去取，缓存 7 天。只返回图片。

**给 Annulo 外壳发消息**（`window.parent.postMessage(…, location.origin)`）：
`{ type: 'annulo:open-chat', chat_id }` 在右侧打开某段对话（比如任务、定时任务开的那段）；
`{ type: 'annulo:navigate', view: 'settings', hash: '#secrets' }` 打开 Annulo 的设置页。

在线项目的页面单独在浏览器里打开时，没有这些本机接口，页面会改走站点 Func（免费版每天只有 200 次，用完返回 402），本机函数只有 `cloud` / `remote` 的能用（见 `functions.md`）。

## 页面上让用户填密钥

要用户当场填 key 的地方（接一个外部服务），页面上放一个密码输入框，提交时用 `PUT local/secrets` 写进本机密钥；
表里只存密钥名，本机函数再用 `ctx.secrets.get(密钥名)` 读。页面只写不读，也不要把值放进表、日志或返回值。

## 页面标题就是 Annulo 标签页的名字

Annulo 顶栏的标签页显示页面的网页标题（`document.title`）。页面没设标题时，标签只显示一个笼统的「后台」，用户分不清哪个标签是哪个页面。

- 每个页面设自己的标题，和导航里的名字一致：页面文件导出 `generateMetadata`，返回 `{ title }`。不要在 `talizen.config.ts` 的 `metadata` 里给全站写一个固定标题，那样每个标签都会显示成它。
- 页面里切换视图但不整页跳转的（导航改 state 或查询参数），切换时同步改 `document.title`。
- 加新页面、改导航名字时，一起改标题。

## 用 page_errors 查页面报错

页面的错误大多要在浏览器里运行时才出现（读了 null 的字段、组件抛异常）。用 curl 请求页面返回 200，页面也可能是白屏。
`page_errors` 读的是左侧页面在用户的 Annulo 窗口里实际运行时的报错（带调用栈），它不会主动告诉你，要你自己去查。

- 有报错就接着修、再查，别让用户替你发现。只改了本机函数或数据时，不用查页面。
- 查完看返回的 `page` 是不是你要的那个页面：打开的是 404 页面，即使没有 JavaScript 报错，也不算验证通过。
- 返回 `open: false`：用户没开着 Annulo 的窗口，查不到，如实告诉用户「没法在页面里验证」。
- 用户说页面空白、打不开、有红色报错：先用 `page_errors` 拿到报错原文和调用栈，按它定位，别只凭读代码猜。
- **报错可能是别人造成的**：同一个项目可能有别人（或另一段对话）正在改，改到一半就会报错。看调用栈里的文件是不是你这次改过的：
  是就修；不是（或者看不出来）就把报错告诉用户，问要不要处理，不要顺手去改别人的代码。
