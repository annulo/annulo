---
name: shuttle
description: 运营后台怎么做成软件的原则（确定的操作按钮直接执行本机函数，ctx.llm 只用于起标题、翻译这类短的一次性生成，写文章、周报这类长流程交给助手在看得见的对话里跑），以及 Annulo 提供的基础能力和项目的约定：本机函数（local/*.ts，ctx.db / fetch / secrets / mcp / oauth / llm / html / browser）、业务表声明（tables/<表>.json）、定时任务（schedules/<id>.json，本机函数或交给助手的提示）、项目的 skill 和 SHUTTLE.md、页面能调的本机接口、密钥。给运营后台加能力、写本机函数、改后台之前先读它。
---

# Annulo 的基础能力

Annulo 只提供和业务无关的基础能力。**运营后台能做什么（有哪些表、模块、流程）全在项目（也就是工作目录）里定义**，
你给后台加能力只改项目，不改 Annulo。Annulo 本身（程序、安装目录、源码、`~/.shuttle` 下的配置和密钥文件）不归你改，也不要读。
需要 Annulo 没有的基础能力时，告诉用户缺什么。


命令行是 `annulo`（`annulo run`、`annulo push`、`annulo logs`）；老名字 `shuttle` 也能用，模板里写的 `shuttle run` 照常跑。

## 做成软件：确定的活按钮直接做，长的生成交给助手、过程看得见

运营后台要像**开发出来的软件**：用户点一个按钮，这件事就做完，成功了显示结果，失败了说清楚原因和下一步。
给后台加功能时按这个顺序选：

1. **确定的操作**（发布、同步、检测、抓取、增删改、调外部 API）→ 本机函数 + 按钮（`RunButton` / `runLocal`）。
   需要的配置（比如发到哪个 CMS 集合、字段怎么对应）存进表里，按钮读配置执行；没配置时按钮提示「先配置」，而不是转交给助手。
2. **后台自动的判断**（没有按钮、没人盯着看，比如每条新询盘判断真假和意向）→ 本机函数里 `ctx.llm` 调一次，解析成字段写进表。
   只有这一种能在函数里调模型：它在函数里跑，用户看不到过程。**用户点按钮触发的、要 AI 读东西想东西的，哪怕只起一个标题、起草几句话，也交给助手（下一条）。**
3. **流程长的生成**（写文章、写周报、出一批选题、要查好几份数据再判断再写的）→ 交给助手，在一段看得见的对话里跑，
   用户能在右侧看到每一步。不要塞进一个本机函数里用 `ctx.llm` 硬写：用户只能干等，不知道卡在哪。
   - 写成项目的**任务**（`tasks/<id>.md`，见下文「任务」）。产出内容、用户有自己偏好的「需求」类任务（出选题、写文章、写周报…），
     用户关心的写法单独一个文件（见下文「任务」的「怎么写」），页面上发起按钮旁边的「AI 要求」只显示、编辑它，用户想改写法、加要求（比如「写完发到我邮箱」）改的就是它；
     取数、存表、字段格式、平台限制写在任务文件的「规则」和别的步骤里，那是系统提示词，用户改不到；
     按固定流程把系统配好的「系统」类任务（配置发布这种）不放可编辑的要求；
   - 取数和存表仍然用本机函数（比如 `reports.data` / `reports.save`）：数字只有一份来源，存进表的格式由函数校验；
   - 按钮调 `POST local/tasks/<id>/run { input }`（要带参数的，比如写哪个选题，放进 input），页面显示「正在写」和「看过程」（打开那段对话）。
4. **开放的、要边看边判断、要改代码的活**（修站点代码、排查问题、用户临时提的需求）→ `AskButton` 交给助手，按钮文字写明是「交给助手」。

**所有交给助手的按钮（任务、「交给助手」）都一个样子**：点了新开一段对话在后台跑（`POST local/tasks/<id>/run` 或 `POST local/ask`），
在右侧打开那段对话（会弹提示），按钮变成「进行中 · 看过程」，点了回到那段对话，跑完恢复、页面重拉数据。不要把话塞进用户当前的对话里。

一次性的配置（比如第一次给渠道配发布方式）可以让助手做，但做完的结果要落成配置，之后每次都由按钮直接执行。

## 项目的约定

| 文件 | 作用 |
|---|---|
| `SHUTTLE.md` | 这个运营后台是什么、有哪些模块、先读哪个 skill。Annulo 每次开对话会把它放进系统提示，改了下一段对话生效 |
| `tables/<表>.json` | 业务表，一张表一个文件，文件名就是表的 key：`{ name, desc, json_schema }`。Annulo 按它建表（文件变了会自动补建）、放行数据读写。加表就加一个文件 |
| `local/*.ts` | 本机函数，见下 |
| `schedules/<id>.json` | 定时跑的本机函数、任务或交给助手的提示，一个任务一个文件，文件名就是 id：`{ fn / task / prompt, every 或 at, name, input? }`，见下文「定时任务」 |
| `tasks/<id>.md` | 项目的任务：交给助手做的固定的长流程（写周报、写文章），页面按钮和定时任务都按它开对话，见下文「任务」 |
| `skills/<name>/SKILL.md` | 这个运营后台的业务 skill（体检、内容、GEO…），和已安装的 skill 一样会出现在可用列表里 |

这些都是项目的源码，在本机 git 里（在线项目 `annulo push` 后也进 creght 管理），不会被当成网页对外提供。

## 运营后台的版本：本地 git

项目是个 git 仓库，改动历史在 git 里（在线项目的 creght 平台版本只管上线）。下面提到 creght 的只对**在线项目**（项目在 creght 上）适用；离线项目 `annulo push` 只在本机提交。

- **什么时候要推**：页面、本机函数、表声明、任务、skill 都在本机生效，保存就行（左侧后台自动刷新，表按 `tables/` 自动补建）。
  在线项目的 **`backend/func/*.ts` 跑在 creght 平台上，改了必须推**，不推平台用的还是旧代码。另外要存档、同步到别的设备或别人时推。
- **提交运营后台只用 `annulo push -m '为什么改'`**，在线项目也不要直接 `creght push`（工作目录里 AGENTS.md 写的 creght push 对运营后台不适用）。
  它会先提交；在线项目再看远端，有编辑器或其他设备的改动就先合进来单独提交，再推到预览，不会强推。报冲突就改掉文件里的冲突标记再重跑。
- **解冲突**（模板升级、换模板时 git 合并的冲突 `<<<<<<< HEAD` / `>>>>>>> template`；推送时和远端改动的冲突 `<<<<<<< local` / `>>>>>>> remote`）：
  先看两边各改了什么——模板这边 `git log template` / `git diff <上一版模板>..template -- <文件>`，项目这边 `git log -- <文件>`；推送冲突的远端那边就是标记里 remote 的部分。
  - 两边各改了一小处、互不矛盾（模板加了功能，项目改了文案、配置），**合在一起、两边都保留**，这是大多数情况；
  - 一边把整个页面、文件改成了别的用途（比如模板的后台首页被改成了一个网站首页），或者两边的改法互相矛盾、拼在一起跑不起来的，**不要自己选**：
    用 `request_user_input` 把选项摆给用户——保留哪一边、各自会怎样（比如「用模板的：这个地址变回运营后台，网站首页没了」）——按用户选的改；
  - 在线项目的推送冲突也可以整份取一边：`creght resolve <文件> --ours`（本机）/ `--theirs`（远端）只处理冲突的那几段，不冲突的远端改动已经合进来了，要整份用某一边就 `git checkout <提交> -- <文件>` 或照那一边重写。
  改完 `grep -rn '^<<<<<<<\|^>>>>>>>' .` 确认没有标记，再 `annulo push`；推送又撞上新的远端改动就照样再解一轮。
- 每轮对话结束 Annulo 会自动提交一次，你也可以自己 `git commit`。
- 看历史、回滚都用 git：`git log`、`git diff`、`git revert <提交>`、`git checkout <提交> -- <文件>`，然后 `annulo push`。在线项目也不要用 `creght version rollback` 回滚运营后台。
- 用户说模板带的页面、功能坏了、想「变回原来的样子」：除了你自己 `git checkout template -- <文件>`，用户也能在 设置 → 项目 → 「恢复模板文件」里勾选文件、换成模板最新版（能撤销）。
- **git 只管代码。** 业务表里的数据、CMS 内容、平台配置回滚代码不会跟着回去；回滚前告诉用户这一点，数据要改就另外改。
- 渠道站点不在这个仓库里，也不用 `annulo push`：按渠道自己的流程来（creght 站点用 creght CLI 原生的 pull / push / version）。

## 检查后台页面有没有报错（page_errors）

后台页面的错误大多在浏览器里运行时才出现（读了 null 的字段、组件抛异常），curl 预览地址返回 200 也可能白屏。
`page_errors` 工具读的是左侧后台在用户 Annulo 窗口里实际运行时的报错（带调用栈）。它不会自动告诉你，要你自己查：

- **改完页面代码**（文件保存了就行：左侧后台直接渲染工作目录，不用先 `annulo push`；改了 `backend/func/*.ts` 才要推，见上文「运营后台的版本」）：先读项目 `SHUTTLE.md`、页面入口和导航代码确认真实 URL，再 `page_errors({ reload: true, path: "<真实页面地址，保留查询参数>" })`，没报错再告诉用户改好了。
  有报错就接着修、再查，别让用户替你发现。只改了本机函数或数据时不用查页面。
- **不要根据组件名或导航名猜路径**：如果入口用查询参数切换视图，应传 `/?view=<代码中的视图值>`，不能改成 `/<视图值>`。只刷新当前页面时用 `page_errors({ reload: true })`，不必传 path。检查返回的 `page` 是否确实是目标页面；404 页面即使没有 JavaScript 异常，也不算验证成功。
- **用户说后台空白、打不开、有红色报错**：先 `page_errors` 拿报错原文和调用栈，按它定位，别只凭读代码猜。
- 返回 `open: false`：用户没开着 Annulo 的窗口，查不到，如实说明「没法在页面里验证」。
- **报错可能是别人造成的**：同一个后台可能有别人（或另一段对话）正在改，改到一半会报错。看调用栈里的文件是不是你这次改过的：
  是就修；不是（或看不出来）就把报错告诉用户、问要不要处理，不要顺手去改别人的代码。

## 能力版本（shuttle.json）

项目根目录的 `shuttle.json` 写 `{"min_shuttle_api": N}`：这个项目的代码至少要能力版本 N 的 Annulo 才跑得了（低了会提示用户先更新 Annulo，模板升级也会拒绝）。
这台 Annulo 的能力版本在 `ctx.workspace.shuttle_api`。你写的代码用到了比 `min_shuttle_api` 新的能力时，把它调到当前的 `shuttle_api`。

## 本机函数（local/*.ts）

要在本机跑的确定性逻辑写成本机函数：调外部 API（带用户自己的 key）、批量检测、抓取、读平台数据再加工。
Annulo 直接执行它，不经过模型：页面按钮一按几秒到几十秒跑完，不花模型的钱，结果稳定。**不要为这类活每次都自己一条条执行命令。**

形状和 creght 云端 Func 一样：`export function 名字(input, ctx)`，可以是 async。文件名 + 函数名就是调用名（`local/geo.ts` 的 `check` 是 `geo.check`）。

```ts
export async function check(input: { channel_id: string }, ctx: any) {
  const key = ctx.secrets.get('PERPLEXITY_API_KEY')
  if (!key) throw new Error('还没配置 PERPLEXITY_API_KEY：到 Annulo 的 设置 → 密钥 里添加')
  const qs = ctx.db.query('geo_questions', { where: { channel_id: input.channel_id }, limit: 100 }).list
  for (const [i, q] of qs.entries()) {
    ctx.progress({ done: i, total: qs.length, message: '正在问：' + q.question })
    const r = await fetch('https://api.example.com/x', { method: 'POST', headers: { authorization: `Bearer ${key}` }, body: JSON.stringify({ q: q.question }) })
    ctx.db.insert('results', { question_id: q.id, answer: (await r.json()).answer })
  }
  return { ok: true }
}
```

`ctx` 能用的（只有这些：不能读写本机文件、不能执行命令、不能 import npm 包）：

| | |
|---|---|
| `ctx.db.query(表, { where, filter, order_by, limit, offset, cursor })` | 和站点 Func（`backend/func`）的 `ctx.db.query` 一样，返回 `{ total, list, limit, next_cursor }`，同步，过滤和排序在平台上做。`where` 是字段相等；`filter` 是 `{ match: 'and', conditions: [{ fieldId, operator, value }] }`（AND；也认简写 `[{ field, op, value }]`），`operator`：`eq` `neq` `in` `gt` `gte` `lt` `lte` `between`（value `[起, 止]`，两端都含），不写是 `eq`；数字按数值比，字符串按字典序比（`YYYY-MM-DD`、ISO 时间能直接比）。`order_by` 如 `'body.date desc'`（业务字段带 `body.`），默认新的在前。`limit` 默认 20、最多 1000，**要多少写多少**；`total` 是满足条件的总行数。翻页用 `offset`，读完全表用 `cursor`：第一页传 `cursor: ''`，之后传上一页返回的 `next_cursor`，它为空就读完了（按 id 顺序，不能同时指定 `order_by` 或 `offset`）。只是要合计，用下面的 `aggregate`，不用翻页 |
| `ctx.db.aggregate(表, { where, filter, group_by, metrics, order_by, limit, timezone })` | 在平台上分组汇总，行再多也不用拉回来。`group_by: ['post_id', { field: 'date', trunc: 'day'｜'week'｜'month'｜'year', as: 'day' }]`，`metrics: [{ op: 'sum'｜'avg'｜'min'｜'max'｜'count'｜'first'｜'last', field, as, order_by }]`（first / last 是组内按 order_by 排序后的首尾值）。返回 `{ list, truncated }`，最多 10000 组；时区默认 Asia/Shanghai。**要按天 / 按月合计、算一段时间涨了多少，用它，不要 query 全表自己算** |
| `ctx.db.get(表, id)` / `insert(表, data)` / `update(表, id, data)` / `delete(表, id)` | 同步，和站点 Func 一样。get 不存在返回 null；insert 返回写入的行（带 id）；update 是顶层浅合并，值传 null 删字段，返回 `{ ok: true }`（要最新的行就再 get）。只能碰 tables/ 里声明的表 |
| `await fetch(url, { method, headers, body })` | 出网请求（全局 `fetch`，和站点 Func 一样，`ctx` 上没有 fetch），不能访问本机和内网。收到响应头就返回 `{ ok, status, headers.get(), body, text(), json(), arrayBuffer(), timing: { ttfb_ms, total_ms }, truncated }`。响应体最多 10MB：`text()` / `json()` 一次读完（超了或读到一半断了就截断、`truncated` 为 true）；**SSE、要等一两分钟的流式接口边读边用**（能力版本 24 起）：`const reader = res.body.getReader(), dec = new TextDecoder()`，循环 `const { done, value } = await reader.read()`，`dec.decode(value, { stream: true })` 拼成文字。等响应头最多 2 分钟，之后连续 1 分钟没数据才断，不限总时长。也有 `TextEncoder` |
| `await ctx.fetchAll([url 或 {url, method, headers, body}, …])` | 并发请求（最多 4 个同时），按顺序返回；失败的那项是 `{ ok: false, error }`。抓一批页面用它 |
| `ctx.html(text)` | 解析 HTML：`.find('css 选择器')` 返回 `[{ tag, text, attrs }]`，`.text()` 是可见文字，`.markdown()` 转成 Markdown |
| `ctx.secrets.get('NAME')` | 本机密钥（用户在 设置 → 密钥 里填的，或终端环境变量），没有是 null |
| `await ctx.mcp(server, tool, args)` | 调用户在 Annulo 里连上的 MCP 工具（你看到的 `mcp__<server>__<tool>` 就是它），返回工具的结果（文本是 JSON 就解析好），工具报错就抛出。没连上、没授权、工具在设置里关掉了都会抛出能看懂的错。**接外部平台（creght、WordPress…）的数据读写走它**。creght 站点：`ctx.mcp('creght', 'list_sites' / 'site_status' / 'visit_stats' / 'cms_collections' / 'cms_content_list' / 'cms_content_create' / 'cms_content_update', {…})`，项目里封装在 `local/_creght.ts` |
| `await ctx.oauth('google')` | 用户在 Annulo 的 设置 → 连接 里授权过的账号的 access token（过期自动刷新），自己带上 `authorization: Bearer <token>` 调对方的 REST 接口。现在能连的是 `google`（Search Console 只读：`https://www.googleapis.com/webmasters/v3/…`、`https://searchconsole.googleapis.com/v1/…`）。没连接会抛「到 设置 → 连接 里连接 Google」，原样抛给用户就行。一台电脑可以连多个 Google 账号（不同网站在不同账号下）：`ctx.oauth.accounts('google')` 列出连着的账号（邮箱，最早连的在前），`await ctx.oauth('google', { account })` 用指定账号，不给用最早连的；按网站记住用哪个账号（比如存在渠道上），不要假设只有一个 |
| `const b = await ctx.browser.open({ profile, url?, show? })` | 用本机 Chrome 操作网页：**没有开放接口的平台（小红书、抖音…）的发布、采集走它**。一个账号一个 `profile`（小写字母、数字、`-`，登录态留在本机的这个 profile 里，不导出 cookie）；同一个 profile 同时只能被一个任务打开。`ctx.browser.profile(profile)` → `{ id }`，本机没有这个 profile 返回 `null`（能力版本 17 起）：id 存在 profile 目录里、跟着登录态走，登录成功后把它记在账号上；之后 id 对不上或本机没有，就是登录态在别的电脑 / 别的 Annulo 上，定时任务跳过这个账号，不要当成登录过期。**登录**：写一个登录函数，`show: true` 弹出窗口让用户扫码，轮询 `b.exists('登录后才有的元素')` 等到登录成功（给足几分钟）；之后的函数默认后台跑（headless），被网站识别时用 `offscreen: true`。页面操作：`b.goto(url, { wait: 选择器 })`、`b.waitFor(选择器, { timeout })`、`b.exists(选择器)`、`b.click(选择器)` 或 `b.click({ text: '发布' })`、`b.type(选择器, 文字, { clear })`（富文本编辑器也行，`\n` 会按回车分段）、`b.press('Enter')`、`b.upload(file input 选择器, [图片 / 视频 URL、本机文件 'local:<name>' 或截图句柄])`（网址流式下载，最多 2GB，能力版本 10 起；`local:` 是页面存在本机的文件，能力版本 12 起）、`b.eval(() => …)`（在页面里执行，返回值要能转 JSON）、`b.text(选择器?)`、`b.html(选择器?)`、`b.url()`。**读平台数据别解析页面，抓它自己的接口**：先 `b.listen('接口地址的一段')` 再打开页面，`await b.responses('…', { min, timeout })` 拿 `[{ url, status, json, text }]`。`b.setContent(html)` + `b.screenshot({ selector })` 能生成图片（文字卡片），返回的 `{ file }` 直接给 `upload`。单个操作默认等 30 秒；函数结束时浏览器自动关掉。用户手动查看页面时可传 `show: true, keep_open: true`（能力版本 13 起），函数结束后保留窗口给用户。之后助手/本机函数用相同 `profile` 调 `ctx.browser.open` 会接管同一个窗口（不填 `url` 保持当前页面，填了会导航），任务结束归还操作权并继续保留窗口；同一时间只允许一个任务操作，用户关闭窗口后释放 profile。`b.close()` 对这种窗口是归还操作权，旧句柄不能继续操作。自动任务不必传 `keep_open`，只在首次把可见窗口交给用户时传它。**现场**（能力版本 11 起）：`goto` / `waitFor` / `click` / `type` / `upload` / `responses` 失败时自动存一份现场（截图 `shot.png`、页面上看得见的可操作元素 `outline.txt`、源码 `page.html`、`meta.json`），报错末尾带「（现场：目录）」，`err.snapshot` 是目录；`await b.snapshot({ label })` 主动存一份。平台改版、选择器失效时先读 `outline.txt` 找新的元素（aria-label、data-* 属性、文字），再改选择器常量。**发布、采集这些确定的动作写成本机函数，不要让模型自己去点页面**；选择器写成常量放在文件开头，平台改版时只改一处 |
| `await ctx.llm('问题')` / `ctx.llm({ system, prompt })` | 用当前模型答一次（不带工具），用户看不到过程。只用于起标题、翻译、摘要、分类这类短的一次性生成；写文章、周报这类长流程交给助手（见上文） |
| `ctx.llm.providers()` / `await ctx.llm.fetch(服务商 id, 路径, { method, headers, body })` | 用用户在 设置 → 模型 里配好的服务商（能力版本 24 起），**要模型做 `ctx.llm` 不管的事（联网搜索、指定某个模型）时用它，不要让用户在页面上另填 key 和接口地址**。`providers()` 返回 `[{ id, name, api, base_url, builtin, enabled, ready, error, models: [{ id, model, name, api, web_search }] }]`（creght 平台的 id 是 `creght`，不含 key；`web_search` 是 creght 平台标了能用 `/responses` 联网搜索的模型，能力版本 25 起，自己加的服务商不知道、都是 false）；`fetch` 和全局 `fetch` 一样返回能流式读的 Response，路径接在服务商地址后面（OpenAI 兼容的一般接 `/responses`、`/chat/completions`；Anthropic 接 `/v1/messages`），地址和鉴权 Annulo 补。请求体按那个接口自己写。页面选服务商和模型用 `GET local/llm/providers`（同样的列表） |
| `ctx.progress({ done, total, message })`、`ctx.log(...)` / `console.log` | 进度和日志 |
| `await ctx.sleep(ms)`、`ctx.workspace` | 等待（没有 setTimeout）；`{ project_id, site_id, api_host, shuttle_projects, shuttle_api, machine }`（shuttle_projects 是账号下所有 Annulo 项目的 creght 项目 id：列站点、加渠道时排除它们；shuttle_api 是这台 Annulo 的能力版本；machine 是这台电脑 `{ id, name }`，能力版本 15 起，name 给用户看是哪台电脑；认登录态在不在这里用 `ctx.browser.profile`，不用 machine.id） |
| `ctx.locale` | 界面语言 `'zh'` / `'en'`（Annulo 设置里的语言，跟随系统时按系统语言，定时任务也一样）。本机函数抛出的报错、返回给页面显示的说明按它出两种语言，比如 `const L = (zh, en) => ctx.locale === 'en' ? en : zh`；没有这个字段的旧版 Annulo（能力版本 < 6）当中文 |

你自己在对话里查数据（回答用户、写函数前看看表里有什么）用 `db_query` / `db_aggregate` 工具，参数和上面的 `ctx.db.query` / `aggregate` 一样，`db_query` 还能用 `fields` 只取几个字段。

也有 `URL`、`btoa`、`atob`；可以相对路径 import 同目录的代码。抛错（`throw new Error('…')`）的 message 会原样显示给用户，写成能看懂的话。
要用户的 key 就 `ctx.secrets.get`，缺了抛「到 Annulo 的 设置 → 密钥 添加 XXX」（页面上会自动出「去设置」）。

**写完先自己试跑**：`annulo run 文件.函数 --input '{...}'`（`--input @文件` 从文件读；不带参数的 `annulo run` 列出所有函数）。
进度打到 stderr，返回值打到 stdout，报错会带出错的行。改到跑通再 `annulo push`。

### 手机上也能用：本机函数的云端版本（cloud / remote，能力版本 20 起）

只对**在线项目**（项目在 creght 上）适用：运营后台也能在手机、别的电脑上打开（预览域名，用 creght 账号登录成项目成员），那里没有 Annulo。离线项目没有云端版本，`cloud` 声明了也不生效。
文件里用 `export const cloud = ['list', 'stats']` 列出能在云端跑的函数，`annulo push` 时 Annulo 把它们打包成站点 Func `backend/func/local/<文件>.ts`；
页面不在 Annulo 里时，`runLocal('文件.函数', …)` 自动改调这一份，页面代码不用改。

- **能列进去的**：只用 `ctx.db`（包括 `aggregate`）、`ctx.locale`、`ctx.mcp('creght', …)`（只读工具，只给项目所有者）的函数：读表、算统计、只改表（改状态、保存）的都可以。
  `ctx.progress` / `ctx.log` / `ctx.sleep` 在云端什么都不做，`ctx.workspace` 是 undefined（`ctx.workspace?.x ?? 默认值` 照常）。
- **不能列的**：用到 `ctx.secrets`、`ctx.browser`、`ctx.llm`、`ctx.oauth`、`ctx.fetchAll`、`ctx.html` 的；在云端一碰就抛「要在电脑上的 Annulo 里跑」。发布、采集、调要 key 的外部接口都属于这类。
  全局 `fetch` 云端也有（站点 Func 自己的 fetch，同一套写法，从平台服务器出网），但云端拿不到本机密钥，也不走用户自己的网络。
- 云端版本只给这个项目的成员调（没登录 401 `member_login_required`）。同一份函数本机和云端结果要一样：写法照常，`order_by` 不带 `body.`、filter 用简写也行，云端会换成站点 Func 的写法。
- `backend/func/local/` 是生成的：不进 git，**不要手改**，改 `local/<文件>.ts`。改了 `cloud` 或这些函数要 `annulo push` 才到云端。
- 试云端那份：`annulo push` 后 `creght func run --site_id <项目>/<站点> --key local/<文件>.<函数> --input <文件>`，input 文件写 `{ "input": {…}, "locale": "zh" }`；拿 `annulo run` 的结果对一下。

**要电脑的（发布、采集）：`export const remote = ['publish']`**（能力版本 21 起）。手机上点了，平台把调用转给项目所有者电脑上开着的 Annulo，
用这台电脑的浏览器登录态、密钥照常跑，进度一路推回手机页面，体验和在电脑上一样。条件：电脑上 Annulo 开着，并且在 设置 → 远程访问 里打开了
「允许远程调用这台电脑」（默认关）；只有项目所有者能调；这台电脑上的项目都能调，不用先切过去。同一个函数不能既在 `cloud` 又在 `remote`。
`remote` 的函数不生成站点 Func、**不用 `annulo push`**：Annulo 连平台时自己把声明了 `remote` 的函数报上去（文件改了会重报），
页面经模板的 `backend/func/shuttle.ts`（`call`）转给电脑，执行的就是这台电脑上的那份文件。

`annulo push` 还会生成清单 `backend/func/local/_manifest.ts`（哪些函数是 cloud），页面按它和电脑报上来的 remote 判断按钮能不能点。

## 定时任务（schedules/<id>.json）

要定时做的确定性的活（同步数据、拉取询盘、发布到点的排期、采集帖子数据、到期复查）写成本机函数，再在项目根目录的
`schedules/` 目录里声明，一个任务一个文件，文件名就是它的 id。Annulo 开着时按时跑，不经过模型：

```
schedules/leads.sync.json     { "fn": "leads.sync", "every": "30m", "name": "拉取表单询盘" }
schedules/review.check.json   { "fn": "review.check", "at": "09:00", "name": "改站复查" }
schedules/gsc-weekly.json     { "fn": "gsc.sync", "every": "1d", "name": "同步 GSC", "input": { "days": 28 } }
```

- `every`：间隔，`30m` / `6h` / `1d`，最短 5 分钟；`at`：每天几点（本机时区）。文件名是运行记录和暂停状态的 key：同一个函数配多条就用不同的文件名，别随手改名。
- 只在 Annulo 开着时跑；错过的下次打开补跑一次。所以函数要**幂等、能接着上次做**：记住同步到哪了（存在表里），重复跑不会重复写。
- 定时跑的函数没有人在看：失败就抛出能看懂的错（记在 设置 → 定时任务 里），不要弹浏览器窗口等人操作（需要登录就抛「先登录」）。
- 用户能在 设置 → 定时任务 里看每个任务上次的结果、立即运行、暂停。页面上要显示时用 `GET local/schedules`。
- 改完文件马上生效，不用重启。

### 定时跑任务（task）

`schedules/weekly-report.json`：`{ "task": "weekly-report", "every": "7d", "name": "写周报" }`，到点按 `tasks/weekly-report.md` 开一段对话交给你，
和页面上点按钮跑的是同一份说明、同一份运行记录（`GET local/tasks` 里看得到）。`input` 会作为这次的参数。要判断、要写的定时活优先写成任务。

### 定时交给助手（prompt）

一两句话就说得清、不需要用户改的活，可以直接写成 `prompt`，到点 Annulo 开一段新对话把这段话交给你，和用户发消息一样
（同样的工具和 skill，结束自动提交项目），对话出现在对话列表里，跑完标成有新回复：

```
schedules/weekly-report.json   { "prompt": "按 weekly-report skill 写上周的运营周报，写进 reports 表", "every": "7d", "name": "写周报" }
```

- `prompt`、`task`、`fn` 三选一；`prompt` 和 `task` 的文件名只能用字母、数字、`-`、`_`。一轮最长 30 分钟，到了就停。
- 跑的时候用户不在：提示里会自动加一句让你别等问卷。所以 prompt 要写全：做什么、数据从哪来（优先 `annulo run` 本机函数拿数字，
  别自己加总）、结果落到哪（业务表，页面能显示；只写在对话里用户在后台看不到）。流程长就写成任务（task）。
- 确定的活仍然写成本机函数用 `fn`：不花模型的钱、结果稳定。
- `last.chat_id` 是这次开的对话，`last.result` 是你最后的回复（截到 500 字）。
- 提示开头会告诉你这段对话的 id，结果写进表时可以一起存下；页面要打开那段对话：`window.parent.postMessage({ type: 'shuttle:open-chat', chat_id }, location.origin)`。

## 任务（tasks/<id>.md）

交给你做的固定的长流程（写周报、写文章、出一批选题）写成任务。一个文件一件事：

```markdown
---
name: 写运营周报
description: 一句话：做什么、结果落到哪（对话里的任务列表和页面都显示它）
---
给助手的完整说明：先跑哪个本机函数拿数据、怎么写、用哪个本机函数存……（和 skill 的正文一样写）
```

- **任务文件是系统流程**（取数、存表的命令和格式、规则），用户不用看；容易被写坏的部分交给本机函数校验。
- **「怎么写」单独一个文件，归用户**：模板给默认的 `prompts/<id>.md`；用户在页面上改了就存到 `user/prompts/<id>.md`，有它就用它。
  产出内容、用户有自己偏好的任务（写文章、写周报、出选题…）才有；配置发布这类系统任务没有。
  页面在发起任务的按钮旁边放「AI 要求」（`GET local/tasks/<id>` 的 `prompt` / `prompt_file` / `prompt_custom`；`PUT { prompt }` 存用户的，`PUT { reset_prompt: true }` 恢复默认，都会在项目的 git 里提交）。
  用户在对话里让你改怎么写，把改后的完整写法存到 `user/prompts/<id>.md`。**`user/` 是用户自己的东西，模板不写这个目录**（模板升级因此不会和用户改的冲突）；也不要去改 `prompts/` 下的默认。
- **执行**：页面 `POST local/tasks/<id>/run { input }`，Annulo 开一段新对话交给你（标题「任务：名字」），马上返回 `chat_id`；
  开头会告诉你这是哪个任务、参数是什么、对话 id 是多少，用户不一定在看，别等问卷。一轮最长 30 分钟。
  同一个任务可以同时跑几次（参数不同）；同样的参数正在跑会返回 409。
- **看进度**：`GET local/tasks` 里每个任务的 `running`（正在跑的：chat_id、input、started_at）和 `last`（最近跑完的：ok、error、chat_id、result），
  页面轮询它显示「正在写」，`running` 变少了就重拉数据；「看过程」用 `shuttle:open-chat` 打开那段对话。
- 系统提示里会列出项目的任务：用户在对话里说「写周报」，你读对应的任务文件照做。
- 用到任务的模板，`shuttle.json` 的 `min_shuttle_api` 至少 7；用到「怎么写」文件的至少 8。

## 页面能调的本机接口

运营后台的页面在 Annulo 里打开，请求 `/_shuttle/api/…`（带 `X-Shuttle: 1` 头）。项目的 `lib/shuttle.ts` 通常已经封装好：

| 接口 | 作用 |
|---|---|
| `POST local/run { fn, input }` | 执行本机函数，SSE 推 `progress` / `log`，最后 `result` 或 `error`。页面关掉也会跑完 |
| `GET local/functions` | 能调的函数 |
| `GET local/llm/providers` | 设置 → 模型 里的服务商和模型（不含 key），给页面做「选哪个服务商 / 模型」（能力版本 24 起）；本机函数里是 `ctx.llm.providers()` |
| `POST local/upload` | 上传文件拿公开地址（multipart，字段 `file`，单个最多 200 MB）：在线项目传到运营后台所在站点的 creght 素材（离线项目存在本机，地址是 `/_shuttle/uploaded/…`），返回 `{ url, path, size, content_type, existed }`（existed：同样内容以前传过）。素材库、社媒配图这类要给外部平台用的文件走它。你自己要传本机文件用 `creght upload --site_id=<运营后台站点> --file=… --json`，是同一套 |
| `POST local/files` | 把文件存在**本机**、不传云端（能力版本 12 起）：请求体就是文件本身（`fetch(…, { method: 'POST', body: file, headers: { 'X-Shuttle': '1', 'X-Filename': encodeURIComponent(file.name), 'content-type': file.type } })`，要进度用 XMLHttpRequest），单个最多 8 GB，90 天后清理。返回 `{ ref: 'local:<name>', url: '/_shuttle/files/<name>', filename, size }`：`ref` 存进表、给本机函数的 `b.upload`，`url` 给页面预览（`<video src>`）。社媒视频这类只给本机浏览器发布用的大文件走它；`DELETE local/files/<name>` 删 |
| `PUT local/secrets` | 写一个本机密钥 `{ name, value }`（比如添加渠道时用户填的应用密码、API key）：只能写，读不回来 |
| `GET local/secrets?names=A,B` | 这几个密钥配了没有：`{ set: { A: true, B: false } }`，不返回值 |
| `GET local/schedules` | 定时任务和每个任务上次运行的结果（`list: [{ id, fn, prompt, name, every, at, disabled, running, next_at, last: { started_at, ok, error, ms, result, chat_id } }]`） |
| `GET local/tasks` / `GET local/tasks/<id>` | 项目的任务（`list: [{ id, name, description, file, body, running: [{ chat_id, input, started_at }], last: { ok, error, chat_id, result, started_at, ms } }]`） |
| `PUT local/tasks/<id> { body }` / `{ prompt }` / `{ reset_prompt: true }` | 改任务说明的正文（frontmatter 不动）/ 存用户改的「怎么写」（`user/prompts/<id>.md`）/ 恢复默认，在项目的 git 里提交 |
| `POST local/ask { text, title? }` | 「交给助手」：新开一段对话把这句话交给助手（后台跑），返回 `{ chat_id }`；`GET agent/running` 的 `chats` 里有它就是还在跑 |
| `POST local/tasks/<id>/run { input? }` | 开一段对话把任务交给助手（后台跑），返回 `{ chat_id, task }`；同样的参数正在跑返回 409 |
| `GET/POST/PATCH/DELETE db/<表>` | 业务表读写（`?project_id=` 过滤，`?id=` 定位）。只放行 tables/ 里声明的表 |
| `POST fetch { url, method, headers, body }` | 代页面请求外部地址（不能访问内网），页面上看一眼外部数据用；要 key、要批量的写本机函数 |

**外站图片**（小红书、公众号这类有防盗链的图床，页面里直接 `<img src>` 会 403）：写成 `<img src={'/_shuttle/img?url=' + encodeURIComponent(图片地址)}>`，
由本机 Annulo 不带 Referer 去取、缓存 7 天。只能在 Annulo 里打开的页面用，只返回图片。

页面还能 `postMessage` 给外壳（`window.parent`，同源）：`{ type: 'shuttle:ask', text }` 把一句话交给右侧的助手；
`{ type: 'shuttle:navigate', view: 'settings', hash: '#secrets' }` 打开 Annulo 的设置页；
`{ type: 'shuttle:open-chat', chat_id }` 在右侧打开某段对话（比如定时任务开的那段）。

**运营后台只能在 Annulo 里打开**：单独打开预览地址时没有这些接口（页面会改走站点 Func，免费版每天只有 200 次，用完返回 402）。
所以改完后台先用 `page_errors` 查一遍，不要把运营后台的预览地址给用户。左侧后台渲染的是工作目录里的文件，保存后几秒内自动刷新。

## 密钥

第三方 API key 只存在用户电脑上（设置 → 密钥），**只给本机函数用**：本机函数里 `ctx.secrets.get('NAME')`。你（助手）的命令行里没有密钥，要调需要 key 的外部服务就写成本机函数，用 `annulo run` 跑。
**不要把 key 的值写进命令、代码、表、对话**，也不要让用户去改 ~/.zshrc。
要用户当场填 key 的地方（添加渠道、接一个平台），页面上放一个密码输入框，提交时 `PUT local/secrets` 写进本机密钥，
表里只存密钥名；本机函数再用 `ctx.secrets.get` 读。页面只写不读，也别把值放进表、日志或返回值。

## 查本机函数日志

能力版本 14 起，每次本机函数运行的 progress、ctx.log、异常和调用栈会保留到本项目的日志目录，最多 30 天；不自动记录 input、结果正文、密钥或 OAuth token。

- `annulo logs --fn social.publish --limit 5` 查当前项目最近的运行，返回运行 id、状态、时间和日志文件路径。
- `annulo logs --id <运行 id>` 读整次运行的 JSONL 记录。
- 本机函数可用 `ctx.workspace.logs_dir` 定位目录，用 `ctx.log('阶段、接口状态和结果说明')` 补充诊断；不得记录密钥、Cookie 或 Authorization。
- 浏览器失败现场仍在异常中的 snapshot 目录：读取 outline.txt、meta.json 和截图；不要因超时直接重发，先通过平台只读接口确认是否已经成功。
- 查单条业务记录使用 `db_query({table:'articles',where:{id:'记录 id'}})`；id 是系统主键，不能把未查到 JSON body.id 当成记录不存在。
