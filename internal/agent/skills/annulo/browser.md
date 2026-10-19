# ctx.browser：用本机 Chrome 操作网页

网站没有开放接口（要登录的后台、只有网页版的平台）时，发布、采集都靠它。**发布、采集这些确定的动作写成本机函数（见 `functions.md`），不要让模型自己去点页面。**
选择器写成常量放在文件开头，网站改版时只改一处。

## 先看有没有参考代码

没有接口、只能操作网页的网站，先去 [annulo/references](https://github.com/annulo/references) 找有没有现成的参考代码（`git clone --depth 1 https://github.com/annulo/references /tmp/annulo-references`，看根目录 README 的列表）：
比如在 DeepSeek、豆包、Kimi、元宝的网页上提问（GEO 抽样、问 AI 怎么回答）用 `aichat/`，**不要自己从头写**。
照那个目录的 README 复制进项目的 `local/`，在主文件开头记下复制自哪个提交；它坏了照它的 `MAINTAIN.md` 修：先和仓库最新的比，再排查、测试。

## 打开和账号

`const b = await ctx.browser.open({ profile, url?, show?, offscreen?, keep_open? })`

- **一个账号一个 `profile`**：只用小写字母、数字、`-`。登录态留在本机的这个 profile 里，不导出 cookie。同一个 profile 同时只能被一个任务打开。
- `ctx.browser.profile(profile)` 返回 `{ id }`，本机没有这个 profile 就返回 `null`。id 存在 profile 目录里，跟着登录态走：登录成功后把它记到账号上；
  以后 id 对不上或者本机没有，说明登录态在别的电脑或别的 Annulo 上。这时定时任务跳过这个账号，不要当成登录过期。
- 默认在后台跑（headless）。被网站识别出来时改用 `offscreen: true`。

## 用户自己的浏览器：`profile: '@user'`

用户一般只有一个账号、早在自己 Chrome 里登好了的网站（豆包、DeepSeek、Kimi、ChatGPT 网页版这类），用 `profile: '@user'`：
经用户 Chrome 里的 Annulo 插件，在用户平时的浏览器里开一个新窗口操作，用户不用在 Annulo 的浏览器里再登录一遍。`b.*` 的用法完全一样。

- 要多个账号、要互相隔离的（社媒），照旧一个账号一个独立 profile。
- 第一次用某个网站时，用户的 Chrome 里会弹窗问「允许 Annulo 在这个网站上操作吗」：只有页面上点的运行会弹，后台定时任务碰到没允许的网站直接报错，提示用户先在页面上跑一次。
- 只能在用户允许过的网站上操作：跳到别的网站（`goto` 或者页面自己跳）会被插件拦下，之后的操作都报错。
- 用户有好几个 Chrome profile 时按网站自动选：允许过这个网站的那个（几个都允许过就用最近用过的）。`ctx.browser.user()` 列出连着的 profile
  `{ profiles: [{ id, name, email, sites, last_active }] }`，要指定一个就写 `profile: '@user/<id>'`。
- 没装插件、Chrome 没开时 `open` 会报错，告诉用户去装插件、打开 Chrome；`ctx.browser.user()` 的 `profiles` 是空数组就是没连上。
- `headless`、`offscreen` 对它没用：窗口总是开在用户的屏幕上（不抢前台），`show: true` 才调到前台。
- 不用排队：几个任务可以同时跑，每次 `open` 各开一个窗口。
- 函数结束时浏览器自动关掉。

## 登录

写一个专门的登录函数：用 `show: true` 弹出窗口让用户扫码或输入密码，然后轮询 `b.exists('登录后才有的元素')`，等到登录成功（给足几分钟）。
其他函数发现没登录，就抛出「先登录」，不要在定时任务里弹窗口等人。

## 页面操作

单个操作默认最多等 30 秒。

- `b.goto(url, { wait: 选择器 })`、`b.waitFor(选择器, { timeout })`、`b.exists(选择器)`、`b.url()`
- `b.click(选择器)`、`b.click({ text: '发布' })`
- `b.type(选择器, 文字, { clear })`：富文本编辑器也能用，`\n` 会按回车分段；`b.press('Enter')`
- `b.upload(file input 选择器, [文件…])`：文件可以是图片或视频的网址（流式下载，最大 2GB），可以是离线项目上传在本机的 `/_annulo/uploaded/…`（`local/upload`、`annulo upload` 给的地址，直接读本机文件），可以是本机文件 `'local:<name>'`（页面存在本机的文件，见 `pages.md` 的 `POST local/files`），也可以是截图返回的句柄
- `b.eval(() => …)`：在页面里执行，返回值要能转成 JSON
- `b.text(选择器?)`、`b.html(选择器?)`

## 读数据：抓网站自己的接口，别解析页面

先 `b.listen('接口地址里的一段')`，再打开页面，然后 `await b.responses('…', { min, timeout })` 拿到 `[{ url, status, json, text }]`。
网站的页面结构常改，接口返回的 JSON 稳定得多。

## 生成图片

`b.setContent(html)` 加 `b.screenshot({ selector })` 能把一段 HTML 渲染成图片（比如文字卡片），返回的 `{ file }` 可以直接交给 `upload`。

## 把窗口交给用户

用户要手动看一下页面时，传 `show: true, keep_open: true`：函数结束后窗口留给用户。
之后助手或本机函数用同一个 `profile` 调 `ctx.browser.open`，会接管同一个窗口（不填 `url` 停在当前页面，填了就跳过去），任务结束后交还控制，窗口继续保留。
同一时间只有一个任务能操作这个窗口；用户关掉窗口后才释放这个 profile。对这种窗口调 `b.close()` 是交还控制，旧的句柄就不能再用了。
自动任务不用传 `keep_open`，只在第一次把可见窗口交给用户时传。

脚本做到一半、剩下的只有人能做（平台检测不过、必填项脚本填不对）时，`await b.show()` 把这次的窗口调到前台，
告诉用户已经做好了什么、还要他做什么，再轮询页面等他做完（比如等发布接口的响应）；等不到就抛错，窗口留着给用户。
后台无界面运行的（定时任务）调不出来，`b.show()` 会报错：照原来报错，写清楚要用户做什么。

## 交给浏览器助手做完一件事：b.agent

页面常改、表单要多步、会弹窗的（社媒发布这类），不要把每一步写死成选择器：`await b.agent(任务, { tools, files, avoid, notes })` 让浏览器助手看着页面做完，
脚本只给它工具（富文本填写并读回核对、加标签、等上传完、带核对的发布）、列出能上传的文件、写清不许点的按钮（发布、删除这类收不回来的，只能经工具）。
它学到的经验记在笔记里：插件自带 `plugins/<插件>/notes/<名字>.md`，项目自己的 `user/notes/<插件>/<名字>.md`（项目函数是 `user/notes/<名字>.md`）。
笔记是给它看的说明（字段怎么认、坑、变化），人也能改；平台改版时先看、先改笔记。返回 `{ ok, summary | reason }`，没做完照原来交给用户（`b.show()`）。
用法和安全边界见 Annulo 的 docs/browser-agent.md；B站 投稿（`plugins/social/local/bilibili.ts` 的 `agentAttempt`）是例子。

## 失败现场

`goto` / `waitFor` / `click` / `type` / `upload` / `responses` 失败时，会自动存一份现场：截图 `shot.png`、页面上看得见的可操作元素 `outline.txt`、源码 `page.html`、`meta.json`。
报错末尾带「（现场：目录）」，`err.snapshot` 就是这个目录；也可以用 `await b.snapshot({ label })` 主动存一份。
网站改版、选择器失效时，先读 `outline.txt` 找到新的元素（aria-label、data-* 属性、文字），再改选择器常量。
