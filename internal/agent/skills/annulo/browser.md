# ctx.browser：用本机 Chrome 操作网页

网站没有开放接口（要登录的后台、只有网页版的平台）时，发布、采集都靠它。**发布、采集这些确定的动作写成本机函数（见 `functions.md`），不要让模型自己去点页面。**
选择器写成常量放在文件开头，网站改版时只改一处。

## 打开和账号

`const b = await ctx.browser.open({ profile, url?, show?, offscreen?, keep_open? })`

- **一个账号一个 `profile`**：只用小写字母、数字、`-`。登录态留在本机的这个 profile 里，不导出 cookie。同一个 profile 同时只能被一个任务打开。
- `ctx.browser.profile(profile)` 返回 `{ id }`，本机没有这个 profile 就返回 `null`。id 存在 profile 目录里，跟着登录态走：登录成功后把它记到账号上；
  以后 id 对不上或者本机没有，说明登录态在别的电脑或别的 Annulo 上。这时定时任务跳过这个账号，不要当成登录过期。
- 默认在后台跑（headless）。被网站识别出来时改用 `offscreen: true`。
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

## 失败现场

`goto` / `waitFor` / `click` / `type` / `upload` / `responses` 失败时，会自动存一份现场：截图 `shot.png`、页面上看得见的可操作元素 `outline.txt`、源码 `page.html`、`meta.json`。
报错末尾带「（现场：目录）」，`err.snapshot` 就是这个目录；也可以用 `await b.snapshot({ label })` 主动存一份。
网站改版、选择器失效时，先读 `outline.txt` 找到新的元素（aria-label、data-* 属性、文字），再改选择器常量。
