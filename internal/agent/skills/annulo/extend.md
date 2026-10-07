# 给 Annulo 装 skill、接 MCP

## 先判断要不要装

- 调一个有 REST 接口的服务：本机函数里 `fetch` 加上 `ctx.secrets` 就够了，不用装东西（见 `functions.md`）。
- 本机有现成的命令行工具：本机函数里用 `ctx.exec`。
- 网站没有开放接口：用 `ctx.browser`（见 `browser.md`）。
- **对方提供 MCP**（数据读写、搜索这类工具），而且你在对话里、本机函数里都要用：接 MCP。
- **一套做事的方法、流程、写法**（某个平台怎么用、某类文档怎么写）：装 skill，或者写进项目的 `skills/`。

装之前告诉用户要装什么、从哪来、能做什么。来源不明的不装。

## skill

skill 是一个目录，里面的 `SKILL.md` 开头有 frontmatter：

```markdown
---
name: my-skill
description: 做什么、什么时候该读它（这一句每轮都会给你看，写清楚触发的场景）
---
正文……
```

`name` 只用小写字母、数字和 `-`，和目录名一致。

**装到哪**：
- 只跟这个项目的业务有关的（这个项目的流程、写法），写进项目的 `skills/<name>/`：跟着项目走，进 git，换电脑、给别人都还在。
- 通用的、别人做好的（某个平台的用法、某类文件的处理），装到已安装目录（系统提示「Skill 目录」里写的那个，`<已安装目录>/<name>/`）：这台电脑上所有项目都能用，用户能在设置里停用、删除。

**怎么装到已安装目录**：
- GitHub / git 仓库：`git clone --depth 1 <仓库> /tmp/<临时目录>`，把 skill 所在的目录（有 `SKILL.md` 的那层）整个复制到 `<已安装目录>/<name>`。GitHub 的 `…/tree/<分支>/<路径>` 链接，就是仓库加上里面的路径。
- 只有一个 `SKILL.md` 的链接：`curl -fsSL <链接> -o <已安装目录>/<name>/SKILL.md`。
- 本机其他 agent 的 skill（`~/.agents/skills`、`~/.claude/skills`）：复制过来，不要软链。

装好后：
1. 读一遍它的 `SKILL.md`，检查 frontmatter 的 `name` 和目录名一致；
2. 看它要不要命令行工具、MCP、API key：缺命令行工具就按它写的装（装之前告诉用户），缺 MCP 就按下面接，缺 key 让用户到 设置 → 密钥 添加；
3. 这一轮就可以按它做事；从下一条消息开始，它会自动出现在可用 skill 列表里。

卸载就删掉目录。启用、停用由用户在设置页操作，不要改记录启用状态的那个文件。

## MCP

**用 `annulo mcp` 管理**，不要直接改 Annulo 数据目录里的 `mcp.json`：直接改了 Annulo 不会重连，工具也不会出现。

```bash
annulo mcp list                                               # 已连的 MCP 和连接状态
annulo mcp add notion https://mcp.notion.com/mcp              # 远程 MCP（http；老式 SSE 加 --transport sse）
annulo mcp add github https://api.githubcopilot.com/mcp/ --header 'Authorization: Bearer ${GITHUB_TOKEN}'
annulo mcp add playwright -- npx -y @playwright/mcp@latest    # 本地 MCP：-- 后面是启动命令
annulo mcp add db --env DB_URL='${DB_URL}' -- uvx some-mcp-server
annulo mcp remove notion
```

- 名字只用字母、数字、`_`、`-`。已经有同名的会报错，要换掉就加 `--force`。
- **要 key 的**：不要把 key 写进命令。请求头、环境变量、参数里写 `${密钥名}`，Annulo 会用 设置 → 密钥 里的值替换；没有这个密钥，就让用户到 设置 → 密钥 添加，加好后 `annulo mcp add … --force` 重连一次。
- **要登录授权的**（状态 `needs_auth`）：命令会打出一个授权地址。把地址给用户，让用户打开登录（或者在 设置 → MCP 里点授权），授权完会自动连上，再用 `annulo mcp list` 确认。
- **没连上**（`failed`）：按打出来的报错改；本地 MCP 启动命令的输出在报错下面提示的日志文件里。本地 MCP 常用 `npx`、`uvx`，没装 Node / uv 时先告诉用户装。
- 别的工具（Claude Desktop、Cursor、Claude Code）的 MCP 配置 `{"mcpServers": {"x": {…}}}`：`url` 的用 `annulo mcp add x <url>`，`headers` 换成 `--header`；`command` 的用 `annulo mcp add x -- <command> <args…>`，`env` 换成 `--env`。
- 连上以后：从下一条消息开始，你能直接用它的工具 `mcp__<名字>__<工具>`；本机函数里用 `ctx.mcp('<名字>', '<工具>', 参数)`，这一轮就能用 `annulo run` 试。
- 每个工具的定义每轮都占上下文。工具很多、只用得到几个时，告诉用户可以在 设置 → MCP 里把用不到的关掉。
- 用户连着 creght 账号时，Annulo 会自动加上 creght 的 MCP（名字是 `creght`），不用装。
