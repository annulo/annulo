// Package agent 把 pi（sky-valley/pi，pi 的纯 Go 移植）包成 Shuttle 的本地 agent。
//
// 一个 Shuttle 进程同时只跑一段对话；工作目录固定是运营后台站点的本地副本。
package agent

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/annulo/annulo/internal/i18n"
	"github.com/annulo/annulo/internal/plugin"

	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/coding"

	"github.com/annulo/annulo/internal/brand"
	"github.com/annulo/annulo/internal/config"
	"github.com/annulo/annulo/internal/creght"
	"github.com/annulo/annulo/internal/tasks"
)

//go:embed all:skills
var builtinSkills embed.FS

const systemPromptBase = `你是 Annulo 里的助手：帮用户把他们要的工具做进项目（页面、数据表、本机函数、定时任务），也用这些工具替用户做事。工作目录是用户当前的项目（通常从模板创建，左侧显示的就是它的页面）。
项目是什么、有哪些模块、该读哪个 skill，写在下面的「项目」一节（来自项目的 ANNULO.md，老项目叫 SHUTTLE.md）。
用户要你以后在这个项目里一直怎么做（回答风格、不能做的事、固定的做法），写进项目根目录的 INSTRUCTIONS.md（没有就新建，用户也能在 设置 → 项目 里改）：它归用户，每段新对话都会放进下面「用户的要求」一节；ANNULO.md / SHUTTLE.md 是模板带的，不要往里写这类要求。

缺少会实质影响结果的信息时（定位、目标客户、要不要发布、选哪个对象这类），用 request_user_input 一次问清楚（1~6 个问题，选择题给 2~6 个选项）；单独调用它，不和其他工具一起；小事自己合理判断，不要事事都问。拿到回答后直接照做，不要再确认一遍。
消息是项目的任务（以「按项目里的任务「…」做」或 "Do the project task" 开头，页面上的按钮和定时任务发的，也可能是用户复制过来的）或定时任务（以「定时任务「…」：」或 "Scheduled task" 开头）时：它通常在后台跑，用户不一定在看。不要用 request_user_input 等用户回答，缺的信息按合理的默认做完，在最后的回复里说明做了哪些假设、有什么要用户处理的；做出来的东西在项目的某个页面能看到的，最后给出那个页面的链接。
skill 就是目录里的文件，由 Annulo 管理（见下方「Skill 目录」），需要新能力时用 bash 自己安装。
简洁地回复用户：用用户说的语言回复，拿不准时用下面「界面语言」一行写的语言。

## 规则

- Annulo 本身（这个程序、它的安装目录、源码、~/.annulo 下的 config.json / secrets.json / mcp-auth / chats）不归你改，也不要去读里面的 key。
  给项目加能力只改项目：表写进 tables/<表>.json（一张表一个文件），要在本机跑的逻辑写成 local/*.ts 本机函数，业务流程写进 skills/（见 annulo skill）。
  做不到的就告诉用户缺什么，不要绕道去改 Annulo。
- 密钥（第三方 API key）只给本机函数用：本机函数里 ctx.secrets.get('NAME')。你的命令行里没有密钥，要调需要 key 的外部服务，写成或调用本机函数（annulo run）。没有就让用户到「设置 → 密钥」添加。
  不要把 key 的值写进命令、代码、表、对话。
- 调外部 API、批量检测这类确定的、要反复跑的活，写成本机函数，让页面按钮直接调，不要每次都靠你一条条执行。
- 用命令行工具不确定子命令、参数怎么写时，先看 <命令> --help，不要猜。命令报错先读报错和 --help，
  别换着花样乱试；结果和预期不符（比如列表是空的）先怀疑自己用错了，不要直接下结论说「不支持」。
- 执行命令的工具有 description 参数时（bash），每次都要填：用用户的语言写一句 5~10 个字的说明（比如「统计 tables 目录的表」），界面上显示它代替命令原文。
- 用户消息里的 @表:id 或 @表:id#n 是引用：指项目业务表里的一条记录（#n 是这条记录文件列表里的第 n 个，从 1 开始）。Annulo 会在消息后面附上 <references>（是什么、主要字段、文件的本机路径，引用的图片也附给你看），要全文用 db_query 按 id 读。要改引用的东西，照项目的任务和说明做。
- 项目（工作目录）是本地 git 仓库：提交只用 annulo push -m '说明'；回滚用 git（见 annulo skill 的 git.md）。项目对外的网站不在这个仓库里，按它自己的工具推。
- 项目的页面只能在 Annulo 里打开：不要给项目的预览地址（单独打开会报错）。回复里提到用户能在页面上看的东西（你做好、改好了的东西，要用户处理的地方），就写成页面链接 [按钮上的字](/?view=…&…)，提到哪里写到哪里，不要攒到最后统一给一个；地址用页面实际的路径和查询参数（和 page_errors 的 path 一样，以 / 开头、不带域名和端口，有空格时整个地址用 <> 包起来），项目有哪些页面、参数看 ANNULO.md：对话里会显示成按钮，点了左侧跳到那个页面。左侧直接渲染工作目录里的文件，页面代码一保存就自动刷新，不用 push、也不用让用户去刷新；annulo push 是提交改动。项目对外的网站的预览地址照常给。
- 改了项目的页面代码（保存了就行，不用先 push），先用 page_errors（reload: true，path 传改的页面）确认没报错再说改好了；用户说页面空白、报错时也先用它拿报错原文（见 annulo skill 的 pages.md）。
- page_errors 的 path 必须是项目实际使用的页面地址（包括查询参数），先读页面入口、导航代码和 SHUTTLE.md 确认。组件名或导航名字不等于 URL；使用查询参数切页的项目不能把它猜成 /某个名字。只刷新当前页面时不传 path。

## 画图

用户问趋势、对比、排行这类数据问题，查到数据后在回答里写一个 chart 代码块，对话里会画成图：

` + "```chart" + `
{"type":"line","title":"小红书近 30 天每日浏览量","x":"date","y":["views"],"labels":{"views":"浏览量"},"unit":"次","data":[{"date":"09-01","views":1203},{"date":"09-02","views":980}]}
` + "```" + `

- type：line 看随时间的变化，bar 比较分类（类别、对象、Top N）。x 是横轴字段，y 是 1~4 个数值字段（多个就是多条线 / 分组柱，单位要一样），labels 用回复的语言给字段起名。
- data 是按横轴排好序的行，数值写成数字（不带单位、不加引号，缺的写 null）。最多 200 行，点太多先用 db_aggregate 按周 / 月汇总，或只取 Top N。
- 数必须来自刚查到的结果，不要估。某个对象没有这项指标（或没查到）就不放进图里，不要当 0 画；口径不同的指标（比如网站访问量和社媒浏览量）分开画、分开说。只有一两个数就直接说，不画。图后面用一两句话说结论，图里的数不用逐条再列。

## 给用户看本机的图片、视频、音频、PDF

要用户看、听本机的图片、视频或音频（登录二维码、截图、生成或下载的图片和视频、配音和配乐），在回答里写图片语法，路径用绝对路径：![B 站登录二维码](</Users/me/.catbus/cache/bilibili/qrcode.png>)。
对话里会直接显示图片、带播放器播放视频和音频，下面带「打开」「在文件夹中显示」按钮，不用再用 open 命令弹窗。支持图片 png、jpg、gif、webp、bmp、avif、svg，视频 mp4、mov、m4v、webm，音频 mp3、m4a、wav、ogg、opus、aac、flac；PDF 显示成一张卡片，点了在新标签页打开；路径用 <> 包起来，有空格也不会断。
多张图连着写、每行一张、中间不夹文字，会排成缩略图网格；每张要单独说明的，就一张图一段话分开写。`

// creghtPrompt 是连着 creght 时才加的说明：creght CLI 和推送的规矩。
const creghtPrompt = `

## creght

用户连着 creght 账号（creght 平台：网站、CMS、平台模型、MCP）。
- 用 creght CLI 不确定子命令、参数怎么写时，先看 creght <命令> --help，不要猜。
- 推送项目只用 annulo push -m '说明'（在线项目会同步到 creght），不要直接 creght push；项目对外的网站按它自己的工具推（creght 站点用 creght push）。
- 在线项目的 backend/func/*.ts 跑在 creght 平台上，改了必须 annulo push 才生效。`

// creghtConnected：登录了运营后台所在的 creght 集群。
func (a *Agent) creghtConnected() bool {
	_, err := creght.ReadToken(a.creghtHost())
	return err == nil
}

// workspaceNotesFile 是项目给 agent 的总说明：业务是什么、有哪些模块、先读哪个 skill。
// 业务知识放在项目里（跟着运营后台走），不写死在 Shuttle 的系统提示里。
const workspaceNotesFile = "SHUTTLE.md"

// InstructionsFile 是用户写给助手的要求（设置 → 项目里编辑）。只归用户：模板里没有这个文件，升级模板不会碰它。
const InstructionsFile = "INSTRUCTIONS.md"

// Instructions 读项目的 INSTRUCTIONS.md，拼在 SHUTTLE.md 后面、优先级更高（最多 8000 字，调用方持有 a.mu）。
func (a *Agent) instructions() string {
	if a.cwd == "" {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(a.cwd, InstructionsFile))
	notes := strings.TrimSpace(string(b))
	if err != nil || notes == "" {
		return ""
	}
	if r := []rune(notes); len(r) > 8000 {
		notes = string(r[:8000]) + "\n…（太长，后面截掉了，完整的看工作目录的 " + InstructionsFile + "）"
	}
	return "\n\n## 用户的要求（" + InstructionsFile + "）\n\n用户对这个项目的要求，和上面的说明冲突时以这里为准。用户让你改这些要求时改这个文件。\n\n" + notes
}

// workspaceNotes 读项目的 SHUTTLE.md（新名字 ANNULO.md 优先，brand.NotesFiles）放进系统提示（最多 8000 字，调用方持有 a.mu）。
func (a *Agent) workspaceNotes() string {
	if a.cwd == "" {
		return ""
	}
	name, b := workspaceNotesFile, []byte(nil)
	for _, f := range brand.NotesFiles {
		if v, err := os.ReadFile(filepath.Join(a.cwd, f)); err == nil {
			name, b = f, v
			break
		}
	}
	if b == nil {
		return "\n\n## 项目\n\n项目里还没有 " + workspaceNotesFile + "（给你的总说明）。先看工作目录的结构、tables/ 和 skills/，需要时可以补一份。"
	}
	notes := string(b)
	if r := []rune(notes); len(r) > 8000 {
		notes = string(r[:8000]) + "\n…（太长，后面截掉了，完整的看工作目录的 " + name + "）"
	}
	return "\n\n## 项目（" + name + "）\n\n" + strings.TrimSpace(notes)
}

// projectTasks 列出项目的任务（tasks/*.md）：用户在对话里让你做其中一件，照同一份说明做（调用方持有 a.mu）。
func (a *Agent) projectTasks() string {
	if a.cwd == "" {
		return ""
	}
	list := tasks.List(a.cwd)
	if len(list) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n\n## 项目的任务（" + tasks.Dir + "/）\n\n")
	sb.WriteString("交给你做的固定任务，后台页面上的按钮和定时任务按它开一段对话给你。任务文件是系统流程（取数、存表、格式）；" +
		"有「怎么写」的任务，写法在单独的文件里（用户改过的在 " + tasks.UserPromptDir + "/<id>.md，没改过用模板默认的 " + tasks.PromptDir + "/<id>.md），和任务文件里的规则冲突时以规则为准。" +
		"用户在对话里让你做其中一件（比如「写周报」），读任务文件和它的写法照做；用户要改怎么写（加要求、换语气、写完发邮件…），" +
		"把改后的完整写法存到 " + tasks.UserPromptDir + "/<id>.md（没有就从默认那份复制过来再改），不要改任务文件和 " + tasks.PromptDir + "/ 下的默认（那是模板的，升级会覆盖）。" +
		"插件的任务（<插件>/<任务>）同理：用户改的写法存到 user/plugins/<插件>/prompts/<任务>.md。\n")
	for _, t := range list {
		sb.WriteString("\n- " + t.Name + "（" + t.Path)
		if t.PromptFile != "" {
			sb.WriteString("；写法 " + t.PromptFile)
		}
		sb.WriteString("）")
		if t.Description != "" {
			sb.WriteString("：" + t.Description)
		}
	}
	return sb.String()
}

// projectPlugins 列出项目装的插件（plugins/<id>/，docs/plugins.md），调用方持有 a.mu。
func (a *Agent) projectPlugins() string {
	if a.cwd == "" {
		return ""
	}
	ids := plugin.IDs(a.cwd)
	if len(ids) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n\n## 项目装的插件（" + plugin.Dir + "/）\n\n")
	sb.WriteString("插件是装进项目的一包文件，目录结构和项目根目录一样，有自己的版本，在 设置 → 项目 里升级。插件里的东西带插件 id：" +
		"本机函数 plugins/<id>/local/x.ts 的 f 是 <id>/x.f（annulo run <id>/x.f），任务是 <id>/<任务>，表 plugins/<id>/tables/t.json 是 <id>_t。" +
		"用插件之前先读它的 PLUGIN.md：插件提供什么、项目的页面和函数怎么接它。项目的页面和本机函数可以用插件的组件、调插件的函数；插件的文件不 import 项目的文件。" +
		"插件坏了（平台改版之类）可以直接改 plugins/<id>/ 里的文件，升级时会三方合并；用户的定制（插件任务改过的写法、设置）放 user/plugins/<id>/。\n")
	for _, id := range ids {
		m, _ := plugin.ReadMeta(plugin.Root(a.cwd, id), id)
		sb.WriteString("\n- " + id + "：" + m.Name[0])
		if m.Desc[0] != "" {
			sb.WriteString("。" + m.Desc[0])
		}
	}
	return sb.String()
}

// serverPrompt：部署在服务器上（ANNULO_MODE=server，docs/server-and-multi-project.md「服务端部署」）。%[1]s 是数据目录
const serverPrompt = `

## 你在服务器上

Annulo 部署在服务器（通常是 Docker 容器）里，用户在自己的浏览器里远程用，不在这台机器前面：
- **要留下来的东西都在数据目录 %[1]s**（项目目录就在它下面）。容器里别的路径重启就没了；apt、pip、npm -g 装的工具也会丢，要长期用的装到 %[1]s/tools 下（pip install --target、npm --prefix），并告诉用户装在哪。
- **用户打不开这台机器的 localhost 和文件路径**。给用户看项目里的文件，回答里写成链接 [文件名](/_annulo/project-files/<项目里的相对路径>)（HTML、图片、PDF 都能直接打开）；
  起了服务（python -m http.server、npm run dev…）就监听 127.0.0.1 的一个端口（1024 以上），给链接 [打开](/_annulo/port/<端口>/)。不要让用户自己去开 localhost。
  服务要在后台一直跑（nohup … &），页面里的资源用相对路径（/_annulo/port/<端口>/ 下面），绝对路径 / 开头的会找不到。
- 这台机器上没有用户的浏览器：ctx.browser、社媒发布和采集、在用户网页上提问在服务器上用不了，需要时告诉用户这些要在本机版 Annulo 里做。
- 用这个 Annulo 的是一个团队，不止一个人：改项目、删数据前说清楚会影响到谁。
- 项目的业务表存在这台服务器上（Postgres 或数据目录里的 SQLite），**读业务表用 db_query / db_aggregate 工具**，写业务表调本机函数（annulo run）。
  这里没有「离线项目 / 在线项目」之分，不要建议用户「转成在线项目」；annulo push 只在项目的 git 里提交。`

// workFilesPrompt：助手做图片、视频、网站、脚本时的工作文件放哪。只是约定，Annulo 不建、不扫、不记这个目录：
// 项目目录只放项目自己的代码（会进 git、推 creght、参与模板升级），成品用 annulo upload 存进项目，表里只记它给的地址。
// 本机放用户主目录下的 Annulo/<项目名>（看得到、自己能备份和删）；服务端部署只有数据目录不会丢，放 <数据目录>/work/<项目名>。
// 调用方持有 a.mu（和 systemPrompt 一样）。
func (a *Agent) workFilesPrompt() string {
	id, cwd := a.ws, a.cwd
	if id == "" {
		return ""
	}
	name := ""
	if a.ProjectName != nil {
		name = a.ProjectName()
	}
	dir := WorkFilesDir(a.cfg.Dir, a.cfg.Server, name, id)
	if dir == "" {
		return ""
	}
	return fmt.Sprintf(`

## 工作文件放哪

做图片、视频、网站、脚本、数据分析时产生的工作文件（工程、源码、素材、中间结果）**不要放进项目目录**（当前目录 %[1]s），也不要放进 Annulo 的数据目录（%[2]s）里别的地方：项目目录只放项目自己的代码，会进 git、推到 creght、参与模板升级；数据目录是 Annulo 自己的。
- 以后还要接着用的放 %[3]s（没有就建），一件事一个子目录（video/、site/、scripts/…），能共用的工程（比如装好依赖的视频工程）就共用，下次先到这里找。
- 用完就扔的放系统临时目录，或者你自己的临时目录。
- 做出来要进项目的成品用 annulo upload 存进项目（视频这类大文件加 --local，只存在这台电脑上），表里只记它给的地址，不记这些目录里的路径。
- 用户要用 git 管这些文件、推到自己的仓库，照用户说的做；没说就不用管它们的版本。`, cwd, a.cfg.Dir, dir)
}

// WorkFilesDir 是项目的工作目录：本机是 ~/Annulo/<项目名>，服务端部署是 <数据目录>/work/<项目名>。
// 助手的系统提示（上面）和本机函数的 ctx.fs（internal/localfn/fs.go）用同一个。
func WorkFilesDir(dataDir string, server bool, name, id string) string {
	if server {
		return filepath.Join(dataDir, "work", workDirName(name, id))
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "Annulo", workDirName(name, id))
}

// workDirName 把项目名变成能当目录名的一段：去掉路径分隔符和系统不认的字符，空的用项目 id。
func workDirName(name, id string) string {
	name = strings.TrimSpace(strings.Map(func(r rune) rune {
		if strings.ContainsRune(`/\:*?"<>|`, r) || r < 0x20 {
			return '-'
		}
		return r
	}, name))
	name = strings.Trim(name, ". ")
	if name == "" {
		return id
	}
	return name
}

// systemPrompt 在基础提示后面加上项目说明和 skill 目录的说明。skill 就是文件，agent 用 bash 自己装、自己读，不需要专门的工具。
func (a *Agent) systemPrompt() string {
	ops := ""
	if a.OpsSite != "" {
		ops = "\n\n项目的 creght 站点（业务表都在这里）：" + a.OpsSite + "。**读业务表用 db_query / db_aggregate 工具**（过滤、排序、合计都在平台上做），不要用 creght CLI 读、也不要全拉下来再用 jq 筛；" +
			"写业务表优先调本机函数，确实要用 creght CLI 写时整串原样传 `--site_id=" + a.OpsSite + "`。"
	}
	connected := a.creghtConnected()
	if a.Offline && !a.cfg.Server { // 服务端部署没有离线 / 在线之分：数据都在服务器上（serverPrompt 里说）
		ops = "\n\n这是**离线项目**：业务表存在这台电脑上，**读业务表用 db_query / db_aggregate 工具**，写业务表调本机函数（annulo run）。" +
			"annulo push 只在本机 git 提交；backend/func/ 的站点 Func、表单 webhook、手机上访问后台、远程访问都用不了。"
		if connected {
			ops += "不要用 creght table 命令，不要 creght push / pull 项目。用户连着 creght 账号：creght 站点、creght 平台的模型和 MCP 照常能用；" +
				"用户想要手机访问、多设备时，告诉他在 设置 → 项目 里把它转成在线项目（数据会自动迁移）。"
		}
	}
	if a.cfg.Server {
		ops += fmt.Sprintf(serverPrompt, a.cfg.Dir)
	}
	// creght 的说明只给连着 creght 的、或者项目本身在 creght 上的（docs/annulo-plan.md 第 2 步）
	if connected || a.OpsSite != "" {
		ops += creghtPrompt
	}
	lang := "中文"
	if a.cfg.Locale() == "en" {
		lang = "English"
	}
	return systemPromptBase + "\n\n界面语言：" + lang + "（用户在 Annulo 设置里选的，或跟随系统）。" + ops + a.workFilesPrompt() + a.workspaceNotes() + a.instructions() + a.projectPlugins() + a.projectTasks() + fmt.Sprintf(`

## Skill 目录

- 读 skill 用可用 skill 列表里它的 <location>，不要照别的 skill 的目录去猜：skill 分在三个地方（内置、项目的 skills/、已安装），同名的只有一份。
- 内置的 annulo skill 在 %[3]s/annulo/：SKILL.md 讲原则和项目约定，同目录的 functions.md、browser.md、pages.md、tasks.md、git.md、extend.md 讲细节。
  项目文件里说的「shuttle skill」是它的旧名字，读 annulo skill。内置 skill 每次启动会被覆盖，不要改。
- 已安装的 skill 在 %[1]s/<name>/SKILL.md。要装 skill、接 MCP，先读 annulo skill 的 extend.md：装之前告诉用户装什么、从哪来，不装来源不明的；MCP 只用 annulo mcp 命令接。
- 启用 / 停用记录在 %[2]s，由用户在设置页操作，不要改这个文件。`, a.installedDir(), a.skillStateFile(), a.builtinDir())
}

// Event 是推给前端的事件，只保留界面要用的几种。
type Event struct {
	Type    string         `json:"type"` // turn_start | turn_end | text | thinking | tool_start | tool_end | context | steer | background
	Text    string         `json:"text,omitempty"`
	Tool    string         `json:"tool,omitempty"`
	ToolID  string         `json:"tool_id,omitempty"`
	Args    map[string]any `json:"args,omitempty"`
	IsError bool           `json:"is_error,omitempty"`
	// context：每次模型请求结束后的上下文占用，运行中也能看到占用条在涨
	ContextTokens int `json:"context_tokens,omitempty"`
	ContextWindow int `json:"context_window,omitempty"`
	// steer：用户中途插的话被送达模型了（ToolID 是插话的 id，Data 是界面要显示的内容）
	// background：还在跑的后台任务（Data 是 []map[string]string，id / description / type）
	Data any `json:"data,omitempty"`
}

type Agent struct {
	// OpsSite 是运营后台站点 <project_id>/<site_id>，写进系统提示，agent 用 creght CLI 读写业务表时要用
	OpsSite string
	// Offline：当前是离线项目（数据在本机，没有 creght 站点），系统提示里换一段说明
	Offline bool
	// ProjectName 是项目的名字，工作文件目录用它（workFilesPrompt）；没设或是空的用项目 id
	ProjectName func() string

	cfg   *config.Config
	ws    string // 当前项目（运营后台项目 id）：对话历史按它分开存
	cwd   string
	tools []agent.AgentTool // Shuttle 提供的业务工具（渠道、访问数据…），追加在 pi 内置工具之后

	mu sync.Mutex
	// chats 是每段对话各自的 pi 会话：不同对话可以同时跑，同一段对话同一时间只跑一轮
	chats map[string]*chatSession

	waiting map[string]bool // 问卷显示了、在等用户回答的对话（这段对话下一轮开始时清掉）
	// chatThinking：这段对话固定用的思考档位，不跟全局设置走（任务文件写了 thinking 时，任务跑的那一轮用它）
	chatThinking map[string]string
	creght       *creghtModels // 内置 creght 服务商：集群地址和平台模型列表的缓存（见 models.go），一家子共用
	fam          *family       // 同一台电脑上每个项目各一个 Agent（Fork），共用设置；外部 agent 的 MCP 按 token 找到是哪个项目的

	// MCPURL 是本机给外部 agent（Claude Code / Codex）用的 MCP 地址，带上 mcpToken 才能调（climcp.go）
	MCPURL   string
	mcpToken string
}

// chatSession 是一段对话在内存里的 pi 会话。空闲太多时丢掉最久没用的，下次从会话文件恢复。
type chatSession struct {
	sess *coding.Session
	rec  *coding.SessionRecorder // pi 的会话文件，用来在重启后恢复上下文
	// 会话当前用的模型配置和思考强度。设置改了不丢上下文，下一轮开始前切过去
	model, thinking string
	running         bool
	aborted         bool    // 这一轮被用户停止了
	stale           bool    // 工具 / skill 变了：这一轮跑完就丢掉，下一轮带上新的
	steers          []steer // 已经交给 pi、还没送达模型的插话，按顺序
	used            time.Time
	// cli：这一轮是外部 agent（Claude Code / Codex）跑的，没有 pi 会话；cancel 杀掉它的进程
	cli    bool
	cancel context.CancelFunc
}

// steer 是用户在这一轮进行中插的话。pi 在当前这步（工具调用）结束、下一次请求模型前把它交给模型。
type steer struct {
	id      string
	display any // 送达时推给界面显示的内容（文字 + 图片）
}

var ErrNotRunning = i18n.New("这段对话没有在运行", "This chat isn't running")

// Steer 在正在运行的一轮里插一句话。没在运行时返回 ErrNotRunning，调用方当普通消息发。
func (a *Agent) Steer(chatID, id, prompt string, images []string, display any) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	c := a.chats[chatID]
	if c == nil || !c.running || c.cli {
		return ErrNotRunning // 外部 agent 不能中途插话：界面留在队列里，这一轮结束后当普通消息发
	}
	content := ai.ContentList{ai.TextContent{Text: prompt}}
	if a.current().Vision() {
		for _, p := range images {
			img, err := loadImage(p)
			if err != nil {
				return err
			}
			content = append(content, img)
		}
	}
	c.steers = append(c.steers, steer{id: id, display: display})
	c.sess.Steer(ai.UserMessage{Content: content, Timestamp: time.Now().UnixMilli()})
	return nil
}

// 内存里最多留几段空闲对话的会话
const maxIdleSessions = 8

// RunStats 是一轮对话的统计，随回复一起给前端显示。
type RunStats struct {
	Model         string  `json:"model"`
	DurationMs    int64   `json:"durationMs"`
	InputTokens   int     `json:"inputTokens"`
	OutputTokens  int     `json:"outputTokens"`
	CacheRead     int     `json:"cacheReadTokens"`
	CacheWrite    int     `json:"cacheWriteTokens"`
	Cost          float64 `json:"cost,omitempty"`
	ContextTokens int     `json:"contextTokens"` // 当前上下文占用（下一次请求大约要发这么多）
	ContextWindow int     `json:"contextWindow"`
}

func New(cfg *config.Config) *Agent {
	a := &Agent{cfg: cfg, ws: cfg.BackendID(), cwd: cfg.BackendDir(), chats: map[string]*chatSession{}, mcpToken: newMCPToken(), creght: &creghtModels{host: creght.DefaultHost}, fam: &family{members: map[string]*Agent{}}} // 还没有项目时为空，选好后 SetWorkspace
	a.fam.add(a)
	// creght 服务商用设置里选的集群；有了运营后台就跟着它（SetCreghtHost）
	if cfg.Creght != "" {
		a.creght.host = creght.NormHost(cfg.Creght)
	}
	a.migrateLegacyChats()
	go cliModels() // 先找一遍 claude / codex（第一次要跑一下登录 shell 拿 PATH），别等到持着锁列模型时才找
	return a
}

// family 是同一个进程里的所有 Agent：每个打开的项目一个（Fork 出来的），加上最初那个。
type family struct {
	mu      sync.Mutex
	members map[string]*Agent // mcpToken → Agent
}

func (f *family) add(a *Agent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.members[a.mcpToken] = a
}

func (f *family) byToken(tok string) *Agent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.members[tok]
}

func (f *family) all() []*Agent {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*Agent, 0, len(f.members))
	for _, a := range f.members {
		out = append(out, a)
	}
	return out
}

// Fork 给另一个项目一个 Agent：对话、会话、工作目录、工具是它自己的，设置（cfg）、creght 模型缓存、MCP 地址共用。
// 几个项目可以同时开着、各自的对话同时跑（docs/server-and-multi-project.md「多项目同时开」）。
func (a *Agent) Fork(id, dir string) *Agent {
	n := &Agent{cfg: a.cfg, ws: id, cwd: dir, chats: map[string]*chatSession{}, mcpToken: newMCPToken(), creght: a.creght, fam: a.fam, MCPURL: a.MCPURL}
	a.mu.Lock()
	n.tools = a.tools
	a.mu.Unlock()
	a.fam.add(n)
	return n
}

// Workspace 是这个 Agent 的项目 id（没有项目时是空的）。
func (a *Agent) Workspace() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.ws
}

// AllRunningChats 是所有项目里正在跑的对话（退出、更新前确认用）。
func (a *Agent) AllRunningChats() []string {
	out := []string{}
	for _, m := range a.fam.all() {
		out = append(out, m.RunningChats()...)
	}
	return out
}

// SetWorkspace 切换项目：工作目录、对话历史都换成那个项目的，当前会话作废。
func (a *Agent) SetWorkspace(id, dir string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ws, a.cwd = id, dir
	a.dropSessions()
}

// dropSessions 丢掉空闲的会话，正在跑的标记成跑完再丢（调用方持有 a.mu）。
func (a *Agent) dropSessions() {
	for id, c := range a.chats {
		if c.running {
			c.stale = true
		} else {
			delete(a.chats, id)
		}
	}
}

// evictIdle 空闲会话超过上限时丢掉最久没用的（调用方持有 a.mu）。
func (a *Agent) evictIdle() {
	for {
		var idle []string
		for id, c := range a.chats {
			if !c.running {
				idle = append(idle, id)
			}
		}
		if len(idle) <= maxIdleSessions {
			return
		}
		oldest := idle[0]
		for _, id := range idle[1:] {
			if a.chats[id].used.Before(a.chats[oldest].used) {
				oldest = id
			}
		}
		delete(a.chats, oldest)
	}
}

// SetTools 设置 Shuttle 提供的工具（业务工具 + MCP 工具）。
// 工具集是建会话时定的，所以丢掉空闲的会话，下一条消息从 pi 会话文件恢复上下文、带上新工具；
// 正在执行的不打断，这一轮跑完再换。
func (a *Agent) SetTools(tools []agent.AgentTool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.tools = tools
	a.dropSessions()
}

var (
	ErrBusy    = i18n.New("这段对话的上一条还在执行", "The previous message in this chat is still running")
	ErrAborted = i18n.New("已停止", "Stopped")
)

// Busy 报告是否有任何对话正在执行（切换项目、退出登录前要确认）。
func (a *Agent) Busy() bool { return len(a.RunningChats()) > 0 }

// Running 报告这段对话是否正在执行。
func (a *Agent) Running(chatID string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	c := a.chats[chatID]
	return c != nil && c.running
}

// RunningChats 是正在执行的对话 id。
func (a *Agent) RunningChats() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := []string{}
	for id, c := range a.chats {
		if c.running {
			out = append(out, id)
		}
	}
	return out
}

// session 取这段对话的会话，内存里没有就新建（调用方持有 a.mu）。新会话第一轮由 applySettings 同步一次模型和思考强度。
func (a *Agent) session(chatID string) (*chatSession, error) {
	if c := a.chats[chatID]; c != nil && !c.stale && !c.cli {
		return c, nil
	}
	m, key, err := a.model()
	if err != nil {
		return nil, err
	}
	c := &chatSession{}
	c.sess = coding.NewSession(coding.SessionOptions{
		Model:         m,
		ThinkingLevel: agent.ThinkingLevel(a.thinkingFor(chatID)),
		Cwd:           a.cwd,
		BashEnv:       chatEnv(chatID, a.ws), // 它跑的 annulo run 知道是哪段对话（本机函数的 ctx.chat_id）、哪个项目
		SystemPrompt:  a.systemPrompt(),
		APIKey:        key,
		Skills:        a.sessionSkills(), // 只用 Shuttle 管理的 skill，不扫描用户其他 agent 的目录
		CustomTools:   append([]agent.AgentTool{a.requestUserInputTool()}, a.tools...),
		Compaction:    &coding.DefaultCompactionSettings,
		MaxRetries:    llmMaxRetries,
	})
	// 这段对话以前跑过：从 pi 的会话文件恢复上下文，并接着往同一个文件里记。
	// 界面历史里最后一条是本轮刚发的提问（发送时就写了），对账时不算它。
	if ch, err := a.LoadChat(chatID); err == nil && len(ch.Messages) > 1 {
		prior := ch.Messages[:len(ch.Messages)-1]
		uiUsers := countUsers(historyFromUI(prior, m))
		if ch.PiSession != "" {
			if tree, err := coding.LoadSessionTree(ch.PiSession); err == nil {
				p := tree.BuildProjection()
				if countUsers(p.Messages) >= uiUsers {
					c.sess.LoadBranch(p)
					if rec, err := coding.ResumeSession(ch.PiSession); err == nil {
						c.rec = rec
					}
				}
			}
		}
		if c.rec == nil && uiUsers > 0 {
			// pi 那边缺了（进程被杀、会话文件没落盘），按界面历史重建，另起一个会话文件往后记
			c.sess.LoadHistory(historyFromUI(prior, m))
		}
	}
	if c.rec == nil {
		if rec, err := coding.StartSession(a.cwd, m); err == nil {
			c.rec = rec
		}
	}
	if c.rec != nil {
		c.sess.Record(c.rec)
	}
	a.chats[chatID] = c
	return c, nil
}

// Reset 停掉并丢掉所有对话的会话（换了运营后台站点时用），下一条消息重新建。
func (a *Agent) Reset() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, c := range a.chats {
		if c.running {
			c.stop()
		}
	}
	a.dropSessions()
}

// Abort 停止这段对话正在执行的一轮；chatID 为空就停掉所有的（退出前）。
// agent 的运行和浏览器连接无关，停止只能走这里。
func (a *Agent) Abort(chatID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for id, c := range a.chats {
		if (chatID == "" || id == chatID) && c.running {
			c.stop()
		}
	}
}

// stop 停掉正在跑的这一轮（调用方持有 a.mu）。
func (c *chatSession) stop() {
	c.aborted = true
	if c.cli {
		if c.cancel != nil {
			c.cancel()
		}
		return
	}
	c.sess.Abort()
}

// Run 在 chatID 这段对话里执行一轮，过程中的事件交给 emit。不同对话可以同时跑，同一段对话同一时间只允许一轮。
// 调用方不应该把浏览器请求的 ctx 传进来：刷新页面不该停掉 agent，停止用 Abort。
// images 是本机图片路径（对话里上传的），和文字一起交给模型。
func (a *Agent) Run(ctx context.Context, chatID, prompt string, images []string, emit func(Event)) (RunStats, error) {
	start := time.Now()
	if !chatIDRe.MatchString(chatID) {
		return RunStats{}, ErrBadChatID
	}
	// 平台模型列表有缓存；没选过模型时要靠它决定默认用哪个
	a.RefreshCreghtModels(ctx, false)
	a.mu.Lock()
	if cur := a.current(); IsCLIProvider(cur.Provider) {
		a.mu.Unlock()
		return a.runCLI(ctx, chatID, prompt, images, cur, emit)
	}
	if c := a.chats[chatID]; c != nil && c.running {
		a.mu.Unlock()
		return RunStats{}, ErrBusy
	}
	c, err := a.session(chatID)
	if err == nil {
		err = a.applySettings(c, chatID)
	}
	if err != nil {
		a.mu.Unlock()
		return RunStats{}, err
	}
	sess := c.sess
	vision := a.current().Vision()
	c.running, c.aborted = true, false
	delete(a.waiting, chatID)
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		// 没来得及送达的插话（模型在写最后一段时插的）：从 pi 的队列里撤掉，界面会把它当普通消息接着发
		if len(c.steers) > 0 {
			c.sess.Agent.ClearSteeringQueue()
			c.steers = nil
		}
		c.running, c.used = false, time.Now()
		if c.stale && a.chats[chatID] == c {
			delete(a.chats, chatID)
		}
		a.evictIdle()
		a.mu.Unlock()
	}()

	stop := context.AfterFunc(ctx, sess.Abort)
	defer stop()

	seenPrompt := false // 这一轮第一条用户消息是本轮的提问，之后的用户消息都是插话
	unsub := sess.Subscribe(func(_ context.Context, e agent.AgentEvent) error {
		switch e.Type {
		case agent.EvTurnStart:
			emit(Event{Type: "turn_start"})
		case agent.EvTurnEnd:
			emit(Event{Type: "turn_end"})
		case agent.EvMessageUpdate:
			if ev := e.AssistantMessageEvent; ev != nil {
				switch ev.Type {
				case ai.EventTextDelta:
					emit(Event{Type: "text", Text: ev.Delta})
				case ai.EventThinkingDelta:
					emit(Event{Type: "thinking", Text: ev.Delta})
				}
			}
		case agent.EvMessageEnd:
			switch e.Message.(type) {
			case ai.UserMessage, *ai.UserMessage:
				if !seenPrompt {
					seenPrompt = true
					break
				}
				a.mu.Lock()
				var st steer
				if len(c.steers) > 0 {
					st, c.steers = c.steers[0], c.steers[1:]
				}
				a.mu.Unlock()
				if st.id != "" {
					emit(Event{Type: "steer", ToolID: st.id, Data: st.display})
				}
			}
			// 每条助手消息对应一次模型请求，用量记到本机
			var am *ai.AssistantMessage
			switch m := e.Message.(type) {
			case ai.AssistantMessage:
				am = &m
			case *ai.AssistantMessage:
				am = m
			}
			if am != nil {
				a.recordUsage(chatID, am)
				// 这次请求的输入 + 缓存 + 输出就是此刻的上下文大小
				if u := am.Usage; u.Input+u.Output+u.CacheRead+u.CacheWrite > 0 {
					emit(Event{Type: "context", ContextTokens: u.Input + u.Output + u.CacheRead + u.CacheWrite, ContextWindow: sess.Model.ContextWindow})
				}
			}
		case agent.EvToolExecutionStart:
			emit(Event{Type: "tool_start", Tool: e.ToolName, ToolID: e.ToolCallID, Args: e.Args})
		case agent.EvToolExecutionEnd:
			if e.ToolName == requestUserInputName && !e.IsError && strings.Contains(resultText(e.Result), `"waiting_for_user"`) {
				a.mu.Lock()
				if a.waiting == nil {
					a.waiting = map[string]bool{}
				}
				a.waiting[chatID] = true
				a.mu.Unlock()
			}
			emit(Event{Type: "tool_end", Tool: e.ToolName, ToolID: e.ToolCallID, Text: resultText(e.Result), IsError: e.IsError})
		}
		return nil
	})
	defer unsub()

	var imgs []ai.ImageContent
	if !vision {
		images = nil // 模型看不了图：提示里已经带了本机路径，agent 需要时自己处理
	}
	for _, p := range images {
		img, err := loadImage(p)
		if err != nil {
			return RunStats{}, err
		}
		imgs = append(imgs, img)
	}
	res, err := sess.Run(ctx, prompt, imgs...)
	st := RunStats{Model: sess.Model.ID, DurationMs: time.Since(start).Milliseconds(), ContextWindow: sess.Model.ContextWindow}
	if res != nil {
		st.InputTokens, st.OutputTokens = res.Usage.Input, res.Usage.Output
		st.CacheRead, st.CacheWrite = res.Usage.CacheRead, res.Usage.CacheWrite
		st.Cost = res.Usage.Cost.Total
	}
	st.ContextTokens = contextTokens(sess.Agent.State().Messages)
	a.mu.Lock()
	aborted := c.aborted
	a.mu.Unlock()
	if aborted {
		return st, ErrAborted
	}
	if err != nil {
		return st, err
	}
	if res.ErrorMessage != "" {
		return st, errors.New(res.ErrorMessage)
	}
	return st, nil
}

// contextTokens 估算当前上下文占用：以最后一次成功请求上报的用量为准（输入 + 缓存 +
// 输出，就是那次请求的上下文大小），再加上它之后新增消息的估算。和 pi 判断何时压缩的口径一致。
func contextTokens(msgs []agent.AgentMessage) int {
	for i := len(msgs) - 1; i >= 0; i-- {
		var am *ai.AssistantMessage
		switch v := msgs[i].(type) {
		case ai.AssistantMessage:
			am = &v
		case *ai.AssistantMessage:
			am = v
		}
		if am == nil || am.StopReason == ai.StopError || am.StopReason == ai.StopAborted {
			continue
		}
		u := am.Usage
		total := u.Input + u.Output + u.CacheRead + u.CacheWrite
		if total == 0 {
			continue
		}
		for _, m := range msgs[i+1:] {
			total += coding.EstimateMessageTokens(m)
		}
		return total
	}
	return coding.EstimateContextTokens(msgs)
}

func resultText(r any) string {
	var s string
	switch v := r.(type) {
	case agent.AgentToolResult:
		for _, c := range v.Content {
			if t, ok := c.(ai.TextContent); ok {
				s += t.Text
			}
		}
	case *agent.AgentToolResult:
		if v != nil {
			return resultText(*v)
		}
	}
	if len(s) > 4000 {
		s = s[:4000] + "\n…"
	}
	return s
}

// chatEnv：助手在这段对话里跑的命令带上的环境变量。ANNULO_CHAT_ID（老名字 SHUTTLE_CHAT_ID）是对话 id，
// annulo run 把它传给本机函数，函数里读 ctx.chat_id，就能把结果记到「出自哪段对话」，不用助手照抄 id。
// ANNULO_PROJECT 是项目：几个项目同时开着时，命令行（annulo run …）照它找项目，cd 到别的目录也不会跑到别的项目上
func chatEnv(chatID, project string) map[string]string {
	env := map[string]string{}
	kvs := brand.ChildEnv("CHAT_ID", chatID)
	if project != "" {
		kvs = append(kvs, brand.ChildEnv("PROJECT", project)...)
	}
	for _, kv := range kvs {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	return env
}
