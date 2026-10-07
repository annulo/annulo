---
name: annulo
description: Annulo 提供的基础能力和项目的约定。怎么把需求做成软件（按钮背后用本机函数、ctx.llm 还是交给助手的任务），项目的目录（ANNULO.md、tables/、local/、schedules/、tasks/、skills/、plugins/、user/、annulo.json），能力版本；同目录的文件讲细节：本机函数和 ctx（functions.md）、用本机 Chrome 操作网页（browser.md）、页面能调的接口和 page_errors（pages.md）、任务和定时任务（tasks.md）、git 版本、升级模板和冲突（git.md）、装 skill 和接 MCP（extend.md）。给项目加功能、改页面、写函数、装 skill 或 MCP 之前先读它。
---

# Annulo 的项目

Annulo 只提供和业务无关的基础能力。**项目（工作目录）能做什么，比如有哪些页面、表、函数、流程，全在项目里定义**，用户要的功能都做进项目。

细节在这个 skill 目录下的几个文件里（和这个 SKILL.md 在同一个目录），用到哪块读哪块：

| 文件 | 讲什么 |
|---|---|
| `functions.md` | 本机函数 `local/*.ts`：写法、`ctx.*` 能用什么、试跑、日志、手机上用的云端版本 |
| `browser.md` | `ctx.browser`：用本机 Chrome 操作没有开放接口的网站（登录、发布、采集） |
| `pages.md` | 项目的页面：能调的本机接口、上传文件、密钥输入、页面标题、用 `page_errors` 查报错 |
| `tasks.md` | 任务 `tasks/<id>.md`（交给助手的固定流程）和定时任务 `schedules/<id>.json` |
| `git.md` | 项目的版本：`annulo push`、升级模板（`annulo template upgrade`）、解冲突、看历史、回滚 |
| `extend.md` | 给 Annulo 装 skill、接 MCP |

命令行是 `annulo`（`annulo run`、`annulo upload`、`annulo template`、`annulo push`、`annulo logs`、`annulo mcp`）；老名字 `shuttle` 也能用。

## 做成软件：确定的活按钮直接做，长的生成交给助手

项目要像**开发出来的软件**：用户点一个按钮，这件事就做完；成功了显示结果，失败了说清原因和下一步。给项目加功能时按这个顺序选：

1. **确定的操作**（同步、检测、抓取、增删改、调外部接口、发布）→ 本机函数 + 按钮（页面调 `POST local/run`）。
   需要的配置（发到哪、字段怎么对应）存进表里，按钮读配置执行；没配置时按钮提示「先配置」，不要转交给助手。
2. **后台自动的判断**（没有按钮、没人盯着，比如给每条新记录分类、打标签）→ 本机函数里调一次 `ctx.llm`，解析成字段写进表。
   只有这一种能在函数里调模型，因为用户看不到函数里的过程。**用户点按钮触发、要 AI 读东西想东西的活，哪怕只起一个标题，也交给助手（下一条）。**
3. **流程长的生成**（写报告、写周报、批量起草、要查好几份数据再判断再写）→ 写成任务（`tasks/<id>.md`，见 `tasks.md`），
   在一段看得见的对话里跑，用户能在右侧看到每一步。不要塞进本机函数里用 `ctx.llm` 硬写：用户只能干等，不知道卡在哪。
   取数和存表仍然用本机函数：数字只有一份来源，存进表的格式由函数校验。
4. **开放的活**（修代码、排查问题、用户临时提的需求）→ 页面上「交给助手」的按钮（`POST local/ask`），按钮文字写明是交给助手。

**交给助手的按钮（任务、「交给助手」）都一个样子**：点了新开一段对话在后台跑，在右侧打开那段对话，按钮变成「进行中 · 看过程」，
点了回到那段对话；跑完按钮恢复、页面重新拉数据。不要把话塞进用户当前的对话。

一次性的配置可以让助手做，但做完要存成配置，之后每次都由按钮直接执行。

## 项目的目录

| 文件 | 作用 |
|---|---|
| `ANNULO.md`（老项目叫 `SHUTTLE.md`） | 项目是什么、有哪些模块、先读哪个 skill。每段新对话都会放进系统提示 |
| `tables/<表>.json` | 业务表，一张表一个文件，文件名就是表的 key：`{ name, desc, json_schema }`。Annulo 按它建表（文件变了自动补建），只放行声明过的表。加表就加一个文件 |
| `local/*.ts` | 本机函数（`functions.md`） |
| `schedules/<id>.json` | 定时任务（`tasks.md`） |
| `tasks/<id>.md`、`prompts/<id>.md` | 任务和它的默认写法（`tasks.md`） |
| `skills/<name>/SKILL.md` | 这个项目自己的业务 skill，和已安装的 skill 一样出现在可用列表里 |
| `plugins/<id>/` | 插件（见下） |
| `user/` | 用户自己的定制（比如改过的任务写法 `user/prompts/<id>.md`）。模板和插件都不写这里，所以升级不会冲突 |
| `annulo.json`（老项目叫 `shuttle.json`） | 能力版本要求、插件、助手面板（见下） |
| 页面代码 | 左侧显示的项目页面（`pages.md`） |

这些都是项目的源码，在本机 git 里（`git.md`），不会当成网页对外提供。

`annulo.json` 的 `"assistant": { name, intro, suggestions }` 决定右侧助手面板的名字、介绍和示例问题：
每项写字符串或 `{ zh, en }`，suggestions 写数组或 `{ zh: [...], en: [...] }`；不写就用 Annulo 通用的。

### 插件（plugins/<id>/）

插件是装进项目的一包文件，有自己的版本，和模板分开升级：用户在 设置 → 项目 → 插件 里装、升级、卸载。每个插件在 git 里有一条 `plugin/<id>` 分支，升级时做三方合并。
`plugins/<id>/` 里的目录和项目根目录一样，里面的东西都带插件 id：

| 插件里的文件 | 在项目里叫 |
|---|---|
| `plugins/<id>/local/x.ts` 的 `f` | 本机函数 `<id>/x.f`（`annulo run <id>/x.f`；云端版本是站点 Func `local/<id>__x.f`） |
| `plugins/<id>/tables/t.json` | 表 `<id>_t`（项目自己的表不要用 `<id>_` 开头） |
| `plugins/<id>/tasks/w.md`、`prompts/w.md` | 任务 `<id>/w` |
| `plugins/<id>/schedules/s.json` | 定时任务 `<id>/s`（里面的 `fn` / `task` 写全名 `<id>/…`） |
| `plugins/<id>/skills/` | skill，和项目的一样加载 |
| `plugins/<id>/components/` | 组件，给项目的页面 import |

项目要用哪些插件：模板写在 `annulo.json` 的 `"plugins": {"<id>": "<仓库>#<目录>"}`，新建项目时自动装上。
用户在设置里装、卸的，Annulo 记在 `user/annulo.json`（写 `null` 表示不要模板带的那个）。给项目加插件依赖就改 `annulo.json`，不要动 `user/annulo.json`。

## 能力版本

`annulo.json` 写 `{"min_annulo_api": N}`（老项目在 `shuttle.json` 里写 `min_shuttle_api`，两个都认），意思是这个项目的代码至少要能力版本 N 的 Annulo 才跑得了。
Annulo 低于它时，会提示用户先更新 Annulo，模板升级也会拒绝。这台 Annulo 的能力版本在 `ctx.workspace.annulo_api`。
你写的代码用到了比 `min_annulo_api` 新的能力时，把它调到当前的 `annulo_api`，改项目里已有的那个文件。
