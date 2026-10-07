package version

// API 是 Shuttle 的「能力版本」：本机函数、页面、助手能用到的平台原语每加一样（或者改了用法）就 +1。
// 模板在 shuttle.json 里写 min_shuttle_api，Shuttle 低于它时升级卡片提示先更新 Shuttle、升级接口拒绝合并。
// App 的版本号（Version）是 git 提交，比不出新旧；这里只管「能不能跑这份模板的代码」。
//
// 每一版加了什么（加原语时在这里记一行，并把模板用到它的地方的 min_shuttle_api 调上来）：
//
//	1  本机函数（local/*.ts）和 ctx.db（query 按字段相等 / get / insert / update / delete）、fetch / fetchAll、
//	   html、secrets、llm、mcp、progress / log / sleep、workspace；业务表、shuttle run / push、模板升级
//	2  ctx.browser（本机浏览器）、ctx.oauth（设置 → 连接）、定时任务（shuttle.schedules.json）
//	3  ctx.workspace.shuttle_projects；ctx.db.query 的 filter / order_by / cursor 翻页（服务端过滤，不再只读前 1000 条）、
//	   ctx.db.aggregate（按字段 / 日期分组汇总）
//	4  定时任务可以是给助手的一段提示（shuttle.schedules.json 的 prompt）；页面 postMessage shuttle:open-chat 打开某段对话
//	5  页面能调 local/upload（上传文件到运营后台站点的 creght 素材拿公开地址，≤200MB）、local/secrets（写本机密钥、查配了没有）
//	6  ctx.locale（界面语言 zh / en）：本机函数给用户看的报错和说明按它出两种语言
//	7  项目的任务（tasks/<id>.md）：页面调 local/tasks 列任务、改说明、执行（开一段对话交给助手，返回 chat_id）；
//	   定时任务可以写 task 引用任务；系统提示里列出项目的任务
//	8  任务的「怎么写」：模板默认 prompts/<id>.md、用户改的 user/prompts/<id>.md（优先）；local/tasks 返回 prompt / prompt_file / prompt_custom，
//	   PUT { prompt } 存用户的、{ reset_prompt: true } 恢复默认；开任务时提示助手照生效的那份写
//	   页面能调 local/ask { text, title? } 新开一段对话交给助手（后台跑），返回 chat_id；轮询 agent/running 看跑完没有
//	9  「连接」的 Google 加了 GA4 只读权限（analytics.readonly）：ctx.oauth('google') 能调 Analytics Admin / Data API；
//	   连接信息返回 missing_scopes（连接后新加的权限），设置页提示重新连接
//	10 b.upload 传网址时流式下载到临时文件（最多 2GB、30 分钟），能传视频；按 content-type 补对扩展名（mp4 / mov / webm…）
//	11 ctx.browser 的现场：goto / waitFor / click / type / upload / responses 失败时存截图、可操作元素、源码到
//	   ~/.shuttle/snapshots/<项目 id>/，报错带目录（err.snapshot）；b.snapshot({ label }) 主动存
//	12 本机文件：页面 POST local/files（请求体是文件本身，头 X-Filename）把视频这类大文件存在本机（最多 8GB，90 天清理），
//	   返回 { ref: 'local:<name>', url: '/_shuttle/files/<name>' }；b.upload 认 'local:<name>'；DELETE local/files/<name>
//
// 13 ctx.browser.open({ show: true, keep_open: true }) 保留可见窗口；后续同 profile 任务接管同一个窗口，
//
//	操作权互斥，任务结束归还用户，关窗口时释放 profile。
//
// 14 ctx.db.query / db_query 的 where.id 和 filter id eq 按记录系统主键读取，避免误查 JSON body.id。
//
//	本机函数持久日志（进度、ctx.log、异常栈），ctx.workspace.logs_dir；shuttle logs 按函数或运行 id 查询。
//
// 15 ctx.workspace.machine = { id, name }：这台电脑（id 存在数据目录的 machine-id，name 是电脑名），
//
//	给浏览器登录态记是哪台电脑登录的，别的电脑上跑定时任务时跳过。
//
// 16 业务表和定时任务改成一个一个文件：tables/<表>.json（{ name, desc, json_schema }）、schedules/<id>.json（一条定时任务），
//
//	文件名就是 key；不再读 shuttle.tables.json、shuttle.schedules.json（还只有它们的项目会提示去升级模板）。
//
// 17 ctx.browser.profile(name) → { id } | null：本机这个浏览器 profile 的 id（存在 profile 目录里，跟着登录态走），
//
//	认「账号的登录态在不在这里」用它，不再用 ctx.workspace.machine.id（数据目录里的 id 会变，登录态却还在）。
//
// 18 页面在本机渲染（internal/localsite，只做客户端渲染）：pages/ 下每个文件一条 react-router 路由，
//
//	页面收到 { params }，可以 import { Link, useNavigate } from 'react-router'（importMap 里 Shuttle 补了 react-router 7）。
//
// 19 ctx.db 以站点 Func 为准（docs/mobile-remote.md）：filter 认 { match, conditions: [{ fieldId, operator, value }] }（简写照旧），
//
//	operator 认 = != > >= < <= 这类写法、不写是 eq；query 加 offset，total 是总行数，limit 默认 20（以前 100）；update 返回 { ok: true }。
//
// 20 本机函数的云端版本：文件里 export const cloud = ['fn', …]，shuttle push 时打包成站点 Func backend/func/local/<文件>.ts（不进 git），
//
//	只给项目成员调，云端 ctx 只有 db / locale / mcp('creght')；手机上打开后台时页面改调它（internal/localfn/cloud.go，docs/mobile-remote.md）。
//
// 21 本机函数的 export const remote：手机上点了转给电脑上开着的 Shuttle 跑（internal/relay，设置 → 远程访问，默认关），进度推回页面；
//
//	shuttle push 生成 backend/func/local/_manifest.ts（cloud / remote 清单）。
// 22 手机上用助手：电脑开着远程访问时，中转多报几个内置函数 _shuttle.chats / chat / send / abort / tasks / task（只给电脑上正在打开的项目，
//
//	internal/server/remoteai.go）：看对话、发消息、停止、跑任务，不推流，手机轮询 _shuttle.chat 拿已存的消息和正在跑的这一轮的快照。
//	离线项目（数据在本机 SQLite，internal/localdb）：ctx.workspace.offline 为 true，site_id 是空的；页面上传的文件地址是本机的 /_shuttle/uploaded/…。
// 23 「连接」一种可以连多个账号（比如两个项目的网站在两个 Google 账号下）：ctx.oauth('google', { account }) 指定账号，不给是最早连的；
//
//	ctx.oauth.accounts('google') 列出这台电脑连着的账号（邮箱）。授权存到 ~/.shuttle/connections/<provider>/<账号>.json。
// 24 本机函数的 fetch 和站点 Func 对齐：响应体能流式读（res.body.getReader().read() → { done, value: Uint8Array }，
//
//	加 TextDecoder / TextEncoder、arrayBuffer()），等响应头 2 分钟、之后按连续 1 分钟没数据算超时，不限总时长（SSE、联网搜索）；
//	去掉 ctx.fetch（调用会提示改用全局 fetch）。internal/localfn/fetchbody.go，internal/server/localrun.go localFetchStream。
//	ctx.llm.providers() / ctx.llm.fetch(服务商, 路径, init)：用 设置 → 模型 里配的服务商发请求（Shuttle 补地址和鉴权，响应能流式读），
//	页面读 GET local/llm/providers 选服务商和模型（internal/server/llmfetch.go）。
// 25 ctx.llm.providers() / GET local/llm/providers 的模型带 web_search：creght 平台标了能用 /responses 联网搜索的模型（平台 /models 的 web_search）。
// 26 改名 Annulo 的兼容层（internal/brand，docs/annulo-plan.md 第 1 步）：新名字和旧名字一样管用——
//
//	annulo.json / min_annulo_api、ANNULO.md、.annulo/project.json、/_annulo/ 前缀、X-Annulo 请求头（响应也带）、远程函数 _annulo.*、
//	页面消息 annulo:*（外壳发给页面的两个名字都发）、ctx.workspace.annulo_projects / annulo_api、环境变量 ANNULO_*（优先于 SHUTTLE_*）。
//	模板用新名字时仍要带 shuttle.json：低于 26 的 App 只认它。
// 27 助手面板的名字、介绍、示例问题由项目决定：annulo.json / shuttle.json 的 "assistant": { name, intro, suggestions }，
//
//	每项写字符串或 {zh, en}（suggestions 是数组或 {zh: [...], en: [...]}）；没写用通用的（internal/server/assistantinfo.go）。
// 28 插件（docs/plugins.md）：项目 = 模板 + 插件，插件装在 plugins/<id>/（有 plugin.json），东西都带插件 id——
//
//	本机函数 <id>/x.f（plugins/<id>/local/x.ts，云端站点 Func local/<id>__x）、表 <id>_t、任务 <id>/w（用户写法 user/plugins/<id>/prompts/w.md）、
//	定时任务 <id>/s、skill；模板在 annulo.json 的 "plugins": {"<id>": "<仓库>#<目录>"} 声明（新建项目、模板升级时自动装），用户装卸的记在 user/annulo.json；
//	接口 GET plugins、POST plugins/install | upgrade | remove
//	（internal/plugin、internal/wsgit/plugin.go、internal/server/plugins.go）。
// 29 ctx.exec(命令, [参数…], { cwd, input, timeout, env })：本机函数跑本机的命令行工具，不经过 shell，返回 { code, stdout, stderr, truncated }
//
//	（internal/localfn/exec.go；找命令按登录 shell 的 PATH，internal/localcmd）。云端不能用。
// 30 ctx.mcp.servers() → [{ name, status }]：用户加了哪些 MCP 和连接状态（connected / needs_auth / connecting / failed / disabled），
//
//	没连 creght 时没有 creght 这一项（Annulo 连了 creght 才加它的 MCP）；页面据此决定显不显示某个集成，不用靠调用报错来猜。
//	云端固定返回 creght（internal/localfn/cloud.go）。
//	ctx.agent.current() → { id, name, provider, model, ready, error }：助手当前的模型和能不能跑（外部 agent 也算），
//	没配模型时 ready=false、error 说去哪配；页面据此决定交给助手的按钮能不能用。云端不能用。
//	ctx.llm.providers() / GET local/llm/providers 加 kind：api（能 ctx.llm.fetch）或 cli（本机装了的 Claude Code / Codex，也列出来了，
//	没有接口地址，ctx.llm.fetch 调它会报错）。要发请求的只用 kind === 'api' 的。
//
// 31 任务文件的 frontmatter 可以写 thinking: low（internal/tasks）：任务跑的那一轮固定用这个思考档位，不跟用户的设置走；
//
//	老版本 Annulo 不认这个字段，照常按设置跑（不用调 min_shuttle_api）。
//
// 32 ctx.chat_id：助手在对话里用 annulo run 跑的函数能拿到对话 id（Annulo 给助手的 bash、Claude Code / Codex 都设了
//
//	ANNULO_CHAT_ID，annulo run 带过去）；页面按钮、定时任务、云端是空字符串。任务开头不再写对话 id 让助手照抄。
//
// 33 离线项目上传的文件地址改成不带端口的 /_annulo/uploaded/<文件>（端口会变；老数据里带端口的完整地址照样认）：
//
//	ctx.fetch、b.upload、图片代理都认这种地址；annulo upload <文件…> 命令把本机文件传进当前项目拿地址（和 local/upload 同一条路）。
const API = 33
