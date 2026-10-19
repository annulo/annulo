# 任务和定时任务

## 任务（tasks/<id>.md）

交给你做的固定长流程（写周报、写报告、批量起草）写成任务，一个文件一件事：

```markdown
---
name: 写周报
description: 一句话：做什么、结果落到哪（对话里的任务列表和页面上都会显示）
thinking: low   # 可选：这个任务固定用的思考档位，不跟用户的设置走
---
给助手的完整说明：先跑哪个本机函数拿数据、怎么写、用哪个本机函数存……（和 skill 的正文一样写）
```

- **任务文件是系统流程**：取数、存表的命令和格式、规则，用户不用看。容易写坏的部分交给本机函数校验。
  字段的含义、函数的参数直接写在任务里（给表格和 JSON 示例），别让跑任务的助手去翻代码猜，那样又慢又容易错。
- **`thinking`**：抽资料、按格式填表这类不用深想的任务写 `low`，每一步快很多；写文章这类要想的不写，跟用户的设置走。
  档位是 off / minimal / low / medium / high / xhigh / max，当前模型没有这档时用最接近的。只管任务跑的那一轮，用户接着聊按自己的设置。
- **「怎么写」单独一个文件，归用户**。只有产出内容、用户有自己偏好的任务（写文章、写周报…）才有；按固定流程把系统配好的任务没有。
  模板给默认的 `prompts/<id>.md`，用户在页面上改了就存到 `user/prompts/<id>.md`，有它就用它。
  页面上在发起任务的按钮旁边放一个「AI 要求」，用来显示和编辑它：读 `GET local/tasks/<id>` 的 `prompt` / `prompt_file` / `prompt_custom`，
  `PUT { prompt }` 存用户的写法，`PUT { reset_prompt: true }` 恢复默认。这两个操作都会在项目的 git 里提交。
- **执行**：页面调 `POST local/tasks/<id>/run { input }`，Annulo 开一段新对话交给你（标题是「任务：名字」），马上返回 `chat_id`。
  对话开头会告诉你是哪个任务、参数是什么。结果要记「出自哪段对话」的，保存函数自己读 `ctx.chat_id`，不用你传。用户不一定在看，不要停下来等用户回答问题。一轮最长 30 分钟。
  同一个任务可以同时跑几次（参数不同）；同样的参数正在跑会返回 409。
- **看进度**：`GET local/tasks` 里每个任务的 `running`（正在跑的）和 `last`（最近一次跑完的）。页面轮询它，显示「进行中 · 看过程」，`running` 变少了就重新拉数据；
  「看过程」用 `annulo:open-chat` 打开那段对话（见 `pages.md`）。

## 定时任务（schedules/<id>.json）

到点自动做的事在项目的 `schedules/` 里声明，一个任务一个文件，文件名就是它的 id。Annulo 开着时按时跑：

```
schedules/orders.sync.json    { "fn": "orders.sync", "every": "30m", "name": "同步订单" }
schedules/ssl.check.json      { "fn": "ssl.check", "at": "09:00", "name": "检查证书到期", "input": { "days": 14 } }
schedules/weekly-report.json  { "task": "weekly-report", "every": "7d", "name": "写周报" }
schedules/daily-look.json     { "prompt": "看一眼昨天的数据，有异常写进 alerts 表", "at": "09:00", "name": "每日检查" }
schedules/publish.json        { "fn": "posts.publish", "input": { "post_id": "$id" }, "name": "发布到点的帖子",
                                "due": { "table": "posts", "field": "scheduled_at", "where": { "status": "scheduled" } } }
```

- `fn`、`task`、`prompt` 三选一：
  - **`fn`**：本机函数，不经过模型。确定的活（同步、检测、拉数据、到点发布）都用它，不花模型的钱，结果稳定。
  - **`task`**：按 `tasks/<id>.md` 开一段对话交给你，和页面上点按钮跑的是同一份说明、同一份运行记录。要判断、要写东西的定时活，优先写成任务。
  - **`prompt`**：一两句话就说得清、不需要用户改的活。到点 Annulo 开一段新对话，把这段话交给你，和用户发消息一样（同样的工具和 skill，结束后自动提交项目）。
- `every` 是间隔：`30m` / `6h` / `1d`，最短 5 分钟。`at` 是每天几点（本机时区）。`input` 是这次运行的参数。
- **`due`：按表里的时间到点跑**（排期发布、到期提醒这类「每一行有自己的时间」的事），不写 `every` / `at`，只能配 `fn`。
  表里满足 `where`（字段相等）和 `filter`（其余条件，写法同 `ctx.db.query`）、`field` 有值且时间到了的行，每行跑一次 `fn`；
  `input` 里的 `"$id"` 换成这一行的 id，`"$row.<字段>"` 换成这一行的字段值（不写 input 是 `{ "id": "<行 id>" }`）。时间存 ISO8601。
  - 排期就存在业务行上：排期 = 写时间字段，改时间 = 改字段，取消 = 清空字段或改掉状态。不要再写一个每隔几分钟扫表的 `every` 任务。
  - 同一行的同一个时间只跑一次，跑之前就记下：失败了不会重跑，函数要自己把失败记在行上（状态、报错），让用户在页面上看到、重发；改了时间会再跑。
  - Annulo 每分钟看一次表，没开着时错过的下次打开补跑；同一个声明的行一条条跑。设置 → 定时任务里列出接下来要跑的行。
- 文件名是运行记录和暂停状态的 key：同一个函数配多条（不同 input）就用不同的文件名，不要随手改名。`prompt` 和 `task` 的文件名只能用字母、数字、`-`、`_`。
- 只在 Annulo 开着时跑；错过的那次，下次打开时补跑一次。所以函数要**幂等、能从上次停下的地方接着做**：同步到哪了记在表里，重复跑也不会重复写。
- 定时跑的时候没有人在看：失败就抛出能看懂的错（记在 设置 → 定时任务 里），不要弹浏览器窗口等人操作（需要登录就抛出「先登录」）。
  `task` / `prompt` 跑的时候，提示里会自动加一句让你别等用户回答。所以 prompt 要写全：做什么、数据从哪来（数字优先用 `annulo run` 跑本机函数拿，别自己加总）、
  结果落到哪（业务表，页面才显示得出来；只写在对话里，用户在页面上看不到）。一轮最长 30 分钟。
- 用户能在 设置 → 定时任务 里看每个任务上次的结果、立即运行、暂停。页面上要显示，就用 `GET local/schedules`；`last.chat_id` 是 `task` / `prompt` 这次开的对话，`last.result` 是你最后的回复（截到 500 字）。
- 改完文件马上生效，不用重启。

## 收件箱（inboxes/<id>.json）

外面主动发进来的东西（网站表单、Stripe 付款、GitHub、平台回调这类 webhook）用收件箱接，不要自己起一个 HTTP 服务、也不要写一个定时去扫的任务。
一个收件箱一个文件，文件名就是它的 id：

```json
{
  "name": "询盘",
  "sources": [
    { "id": "site_form", "type": "webhook" },
    { "id": "github", "type": "webhook",
      "auth": { "kind": "hmac", "header": "X-Hub-Signature-256", "algo": "sha256", "prefix": "sha256=", "secret": "secret:GITHUB_HOOK" },
      "dedupe": { "header": "X-GitHub-Delivery" } }
  ],
  "handler": "leads.receive",
  "notify": [
    { "when": "received", "to": "secret:FEISHU_BOT", "text": "新询盘：{{payload.json.name}} {{payload.json.email}}",
      "body": { "msg_type": "text", "content": { "text": "{{text}}" } } }
  ],
  "keep_days": 7
}
```

- **来源**（`sources`，可以有几个）：现在只有 `webhook`。每个来源有自己的地址 `/_inbox/<令牌>`，在 设置 → 收件箱 里复制，填到对方平台。
  - `auth`：默认只靠地址里的令牌；平台给签名的写 `hmac`（签名在哪个头、`sha256`/`sha1`/`sha512`、`prefix`、`encoding` 是 `hex` 或 `base64`、密钥 `secret:<名字>`）。
    写不出来的签名（比如 Stripe 签的是「时间戳.内容」）、要按平台规则握手的，在 handler 里自己校验，原始请求都在 `ev.payload` 里。
  - `verify: { "echo_query": "hub.challenge" }`：平台接入时用 GET 带这个参数握手的，原样回给它。
  - `dedupe`：平台给了事件 id 就写上（`header` 或 JSON 里的 `field`），平台重试不会处理两遍；不写时同样的请求只算一条。
  - 本机版连着 creght、账号开了代收时，设置 → 收件箱 里显示的是 creght 替它收的公网地址（电脑关着也收、照样发通知，打开 Annulo 后交给 handler）。
    没有代收时本机版的地址只有这台电脑上的程序调得到（可以用 `curl` 自己试），要外面的网站发进来得用服务端部署。照管理页上显示的告诉用户，不要让他以为填上本机地址就能收到。
- **处理**（`handler`）：本机函数，一条事件调一次，`input` 是整条事件：
  `{ id, inbox, source: { id, type }, received_at, payload: { method, headers, query, content_type, body, json?, form? }, attempts, test? }`。
  正常返回算处理完，抛错算失败：每分钟重试，3 次还不行就停下，用户在 设置 → 收件箱 里看到错误、点「重新处理」或「交给助手修」。
  所以 handler 要能重复跑（同一条可能被处理两次），按 `source.id` 区分来源；`test: true` 是管理页「发一条测试」造的。
- **通知**（`notify`）：收到就发，不跑代码、不调 AI；`text` 里用 `{{路径}}` 取事件的字段（`payload.json.name`、`payload.form.email`、`source.id`）。
  `to` 是密钥里存的机器人 webhook 地址（飞书、企业微信、钉钉、Slack），各家要的 JSON 写在 `body` 里，`{{text}}` 换成上面那段；不写 `body` 发 `{"text": …}`。
  要先判断再通知（只推高意向的、带上客户历史），写在 handler 里用 `ctx.fetch` 发。
- 处理完的留 `keep_days` 天（默认 7）。改完文件马上生效。邮箱（登录用户的邮箱读信）这类来源还没做，见 Annulo 的 docs/inbox.md。
