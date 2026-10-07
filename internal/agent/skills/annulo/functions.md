# 本机函数（local/*.ts）

要在本机跑的确定的逻辑写成本机函数，比如调外部接口（带用户自己的 key）、批量检测、抓取、读数据再加工。
Annulo 直接执行它，不经过模型：页面按钮一按，几秒到几十秒跑完，不花模型的钱，结果稳定。**这类活不要每次都自己一条条执行命令。**

写法和 creght 站点 Func 一样：`export function 名字(input, ctx)`，可以是 async。调用名是文件名加函数名（`local/orders.ts` 的 `sync` 就是 `orders.sync`）。
文件名以 `_` 开头的（`local/_api.ts`）放共用代码，用相对路径 import 同目录的文件。

```ts
export async function sync(input: { since?: string }, ctx: any) {
  const L = (zh: string, en: string) => (ctx.locale === 'en' ? en : zh)
  const key = ctx.secrets.get('EXAMPLE_API_KEY')
  if (!key) throw new Error(L('还没配置 EXAMPLE_API_KEY：到 Annulo 的 设置 → 密钥 里添加', 'EXAMPLE_API_KEY is missing: add it in Annulo Settings → Secrets'))
  const res = await fetch('https://api.example.com/orders?since=' + encodeURIComponent(input.since ?? ''), { headers: { authorization: `Bearer ${key}` } })
  if (!res.ok) throw new Error(L('拉取失败：', 'Fetch failed: ') + res.status)
  const list = (await res.json()).orders
  for (const [i, o] of list.entries()) {
    ctx.progress({ done: i, total: list.length, message: o.id })
    ctx.db.insert('orders', { order_no: o.id, amount: o.amount, date: o.date })
  }
  return { added: list.length }
}
```

抛错（`throw new Error('…')`）的 message 会原样显示给用户，要写成用户能看懂的话。缺密钥时抛「到 Annulo 的 设置 → 密钥 添加 XXX」，页面上会自动出现「去设置」。

## ctx 能用的

只有下面这些：不能读写本机文件，不能 import npm 包。要用本机的命令行工具，用 `ctx.exec`。

| | |
|---|---|
| `ctx.db.query(表, { where, filter, order_by, limit, offset, cursor })` | 读业务表，和站点 Func 的 `ctx.db.query` 一样，同步执行，过滤和排序都在平台上做，返回 `{ total, list, limit, next_cursor }`。`where` 是字段相等；`filter` 是 `{ match: 'and', conditions: [{ fieldId, operator, value }] }`（也认简写 `[{ field, op, value }]`），`operator` 有 `eq` `neq` `in` `gt` `gte` `lt` `lte` `between`（value 写 `[起, 止]`，两端都含），不写是 `eq`；数字按数值比，字符串按字典序比（`YYYY-MM-DD`、ISO 时间能直接比）。`order_by` 写成 `'date desc'` 这样，默认新的在前。`limit` 默认 20、最多 1000，**要多少写多少**；`total` 是满足条件的总行数。翻页用 `offset`；读完全表用 `cursor`：第一页传 `cursor: ''`，之后传上一页返回的 `next_cursor`，它为空就读完了（按 id 顺序，不能同时给 `order_by` 或 `offset`）。只要合计就用 `aggregate`，不用翻页 |
| `ctx.db.aggregate(表, { where, filter, group_by, metrics, order_by, limit, timezone })` | 在平台上分组汇总，行再多也不用拉回来。`group_by: ['category', { field: 'date', trunc: 'day'｜'week'｜'month'｜'year', as: 'day' }]`，`metrics: [{ op: 'sum'｜'avg'｜'min'｜'max'｜'count'｜'first'｜'last', field, as, order_by }]`（first / last 是组内按 order_by 排序后的首尾值）。返回 `{ list, truncated }`，最多 10000 组；时区默认 Asia/Shanghai。**按天、按月合计，或者算一段时间涨了多少，都用它，不要 query 全表自己算** |
| `ctx.db.get(表, id)` / `insert(表, data)` / `update(表, id, data)` / `delete(表, id)` | 同步执行。get 查不到返回 null；insert 返回写入的行（带 id）；update 是顶层浅合并，值传 null 会删掉这个字段，返回 `{ ok: true }`（要最新的行就再 get 一次）。只能碰 `tables/` 里声明的表 |
| `await fetch(url, { method, headers, body })` | 出网请求，用全局 `fetch`（`ctx` 上没有 fetch），不能访问本机和内网。收到响应头就返回 `{ ok, status, headers.get(), body, text(), json(), arrayBuffer(), timing: { ttfb_ms, total_ms }, truncated }`。响应体最多 10MB：`text()` / `json()` 一次读完，超出或读到一半断了就截断，`truncated` 为 true。**SSE、要等一两分钟的流式接口边读边用**：`const reader = res.body.getReader(), dec = new TextDecoder()`，循环 `const { done, value } = await reader.read()`，用 `dec.decode(value, { stream: true })` 拼成文字。等响应头最多 2 分钟，之后连续 1 分钟没数据才断，不限总时长。也有 `TextEncoder`、`URL`、`btoa`、`atob` |
| `await ctx.fetchAll([url 或 {url, method, headers, body}, …])` | 并发请求（最多 4 个同时），按传入顺序返回；失败的那一项是 `{ ok: false, error }`。抓一批页面用它 |
| `ctx.html(text)` | 解析 HTML：`.find('css 选择器')` 返回 `[{ tag, text, attrs }]`，`.text()` 是看得见的文字，`.markdown()` 转成 Markdown |
| `ctx.secrets.get('NAME')` | 本机密钥（用户在 设置 → 密钥 里填的，或终端里的环境变量），没有是 null |
| `await ctx.mcp(server, tool, args)` | 调用户在 Annulo 里连上的 MCP 工具（对话里的 `mcp__<server>__<tool>` 就是它）。返回工具的结果（文本是 JSON 的会解析好），工具报错就抛出；没连上、没授权、工具在设置里关掉了，都会抛出能看懂的错。**接外部平台的数据读写，优先走它**。封装成项目里的一个文件（比如 `local/_<平台>.ts`），平台换接口时只改一处。还没连的 MCP 怎么接，见 `extend.md`。`ctx.mcp.servers()` 列出用户加了的 MCP：`[{ name, status }]`，status 是 `connected` / `needs_auth` / `connecting` / `failed` / `disabled`；要按连没连某个平台决定显不显示功能时用它，不要靠调用的报错去猜。没连 creght 时列表里没有 `creght` |
| `ctx.agent.current()` | 助手当前用的模型：`{ id, name, provider, model, ready, error }`。`ready` 是 false 时助手跑不起来（没配模型、缺 key），`error` 说明原因和去哪配。页面上要交给助手的功能（跑任务、AI 预填）先看它：没准备好就别承诺 AI、给出手动的路，旁边带去 设置 → 模型 的入口 |
| `await ctx.oauth(provider, { account })` | 用户在 设置 → 连接 里授权过的账号的 access token，过期自动刷新；自己带上 `authorization: Bearer <token>` 调对方的 REST 接口。现在能连的是 `google`（Search Console 只读）。没连接会抛出「到 设置 → 连接 里连接」，原样抛给用户就行。一种连接可以连多个账号：`ctx.oauth.accounts(provider)` 列出连着的账号（最早连的在前），`account` 不给就用最早连的。要按对象记住用哪个账号，不要假设只有一个 |
| `await ctx.browser.open({ profile, url?, show? })` | 用本机 Chrome 操作网页，见 `browser.md` |
| `await ctx.exec(命令, [参数…], { cwd, input, timeout, env })` | 跑本机的命令行工具：`fetch` 做不到、本机有现成命令的事（`openssl` 查证书、`dig` 查解析、`ffmpeg` 转视频、`git`、某个平台自己的 CLI）。返回 `{ code, stdout, stderr, truncated }`；退出码不是 0 不算出错，自己看 `code`；只有找不到命令、超时才抛出。**命令和参数分开传，不经过 shell**：参数原样交给命令，网址、用户输入直接放进数组，不要拼进命令字符串。要管道、重定向，就用 `ctx.exec('sh', ['-c', '… "$1" …', '_', host])`，值用 `$1`、`$2` 传进去；长一点的脚本放进项目（比如 `scripts/x.sh`），用 `ctx.exec('sh', ['scripts/x.sh', 参数])` 跑。命令按用户登录 shell 的 PATH 找（Homebrew 装的也找得到）。`cwd` 相对项目根目录，不能出项目。没给 `input` 时 stdin 是空的。`timeout` 单位毫秒，默认 30 秒、最多 10 分钟。stdout、stderr 各保留 10MB。命令要用密钥时，用 `env: { TOKEN: ctx.secrets.get('TOKEN') }` 传，不要放进参数（参数会出现在进程列表里）。先在你的 bash 里把命令试通，再写进函数。Windows 上没有 `sh`、`openssl` 这些，用到它们的函数要写明只能在 macOS 上用，或者换个办法 |
| `await ctx.llm('问题')` / `ctx.llm({ system, prompt })` | 用当前模型答一次（不带工具），用户看不到过程。只用于起标题、翻译、摘要、分类这类短的一次性生成；长流程交给助手（见 `SKILL.md`「做成软件」） |
| `ctx.llm.providers()` / `await ctx.llm.fetch(服务商 id, 路径, { method, headers, body })` | 用用户在 设置 → 模型 里配好的服务商。**要模型做 `ctx.llm` 管不了的事（联网搜索、指定某个模型）时用它，不要让用户在页面上另填 key 和接口地址**。`providers()` 返回 `[{ id, name, kind, api, base_url, builtin, enabled, ready, error, models: [{ id, model, name, api, web_search }] }]`，不含 key；`kind` 是 `api`（能 `fetch`）或 `cli`（本机装了的 Claude Code / Codex：助手能用，但没有接口地址，`fetch` 用不了，要发请求就只列 `kind === 'api'` 的）；`web_search` 是平台标明能用 `/responses` 联网搜索的模型。`fetch` 和全局 `fetch` 一样，返回能流式读的 Response；路径接在服务商地址后面（OpenAI 兼容的一般是 `/responses`、`/chat/completions`，Anthropic 是 `/v1/messages`），地址和鉴权由 Annulo 补上，请求体按那个接口的格式自己写 |
| `ctx.progress({ done, total, message })`、`ctx.log(...)` / `console.log` | 进度和日志，页面和 `annulo run` 上都看得到 |
| `await ctx.sleep(ms)` | 等待（没有 setTimeout），最长 60 秒 |
| `ctx.workspace` | `{ project_id, site_id, api_host, annulo_projects, annulo_api, machine, logs_dir, offline }`。`annulo_api` 是这台 Annulo 的能力版本；`annulo_projects` 是账号下所有 Annulo 项目在 creght 上的项目 id；`machine` 是这台电脑的 `{ id, name }`，`name` 拿来告诉用户是哪台电脑；`offline` 为 true 时是离线项目（数据在本机，没有 `site_id`） |
| `ctx.chat_id` | 你在对话里用 `annulo run` 跑这个函数时，是哪段对话的 id；页面按钮、定时任务直接调的是空字符串。要让结果能点回「生成它的对话」（周报、文章旁边的「看助手怎么写的」），在保存函数里把它存进表的 `chat_id` 字段，**不要让助手在 JSON 里照抄对话 id** |
| `ctx.locale` | 界面语言 `'zh'` / `'en'`（Annulo 设置里选的语言，跟随系统时按系统语言，定时任务也一样）。抛出的报错、返回给页面显示的说明，按它出两种语言 |

你自己在对话里查数据（回答用户、写函数前看看表里有什么）用 `db_query` / `db_aggregate` 工具，参数和 `ctx.db.query` / `aggregate` 一样，`db_query` 还能用 `fields` 只取几个字段。
查单条记录用 `db_query({ table, where: { id } })`：`id` 是系统主键，不是 body 里的 `id` 字段。

## 写完先自己试跑

`annulo run 文件.函数 --input '{...}'`（`--input @文件` 表示从文件读；不带参数的 `annulo run` 列出所有函数）。
进度打到 stderr，返回值打到 stdout，报错会带上出错的行。改到跑通再交给用户。

## 查运行日志

每次运行的 progress、`ctx.log`、异常和调用栈都会保留在项目的日志目录（`ctx.workspace.logs_dir`），最多 30 天；input、返回的正文、密钥、token 不会自动记录。

- `annulo logs --fn orders.sync --limit 5` 查最近几次运行，返回运行 id、状态、时间和日志文件路径；`annulo logs --id <运行 id>` 读这次运行的完整 JSONL 记录。
- 用 `ctx.log('阶段、接口状态和结果')` 补充排查用的信息；不要记密钥、Cookie、Authorization。
- 一个操作超时了，不要直接重做：先用对方的只读接口确认它是不是其实已经成功了（比如发布了、写进去了）。

## 手机上也能用：云端版本（cloud / remote）

只对**在线项目**（项目在 creght 上）适用：项目的页面也能在手机、别的电脑上打开（预览域名，用 creght 账号登录、是项目成员），那边没有 Annulo。离线项目没有云端版本。

**只读表、算统计的：`export const cloud = ['list', 'stats']`**。`annulo push` 时 Annulo 把它们打包成站点 Func `backend/func/local/<文件>.ts`；
页面不在 Annulo 里打开时，页面对本机函数的调用会自动改调这一份，页面代码不用改。

- **能放进 cloud 的**：只用到 `ctx.db`（包括 `aggregate`）、`ctx.locale`、`ctx.mcp('creght', …)`（只读工具，只给项目所有者）的函数，比如读表、算统计、只改表（改状态、保存）的。
  `ctx.progress` / `ctx.log` / `ctx.sleep` 在云端什么都不做，`ctx.workspace` 是 undefined（照常写 `ctx.workspace?.x ?? 默认值`）。
- **不能放的**：用到 `ctx.secrets`、`ctx.browser`、`ctx.llm`、`ctx.oauth`、`ctx.exec`、`ctx.fetchAll`、`ctx.html` 的，在云端一调用就抛出「要在电脑上的 Annulo 里跑」。
  全局 `fetch` 云端也有，但请求从平台服务器发出，拿不到本机密钥。
- 云端版本只给这个项目的成员调。同一个函数在本机和云端的结果要一样：照常写就行，`order_by`、`filter` 的写法云端会自动转换。
- `backend/func/local/` 是生成的：不进 git，**不要手改**，要改就改 `local/<文件>.ts`。改了 `cloud` 列表或这些函数，要 `annulo push` 才会到云端。
- 试云端那份：`annulo push` 后跑 `creght func run --site_id <项目>/<站点> --key local/<文件>.<函数> --input <文件>`，input 文件写 `{ "input": {…}, "locale": "zh" }`，再和 `annulo run` 的结果对一下。

**要用电脑的（浏览器登录态、密钥）：`export const remote = ['publish']`**。手机上点了，平台把调用转给项目所有者电脑上开着的 Annulo，
在这台电脑上照常跑，进度一路推回手机页面。条件是：电脑上 Annulo 开着，并且 设置 → 远程访问 里打开了「允许远程调用这台电脑」（默认关）；只有项目所有者能调。
同一个函数不能既在 `cloud` 又在 `remote`。`remote` 的函数不生成站点 Func，也**不用 `annulo push`**：Annulo 连上平台时自己把它们报上去，文件改了会重报。
