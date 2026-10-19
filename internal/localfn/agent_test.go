package localfn

import (
	"context"
	"fmt"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/annulo/annulo/internal/browser"
)

// 一张投稿表单：标题（必填）、「发布」按钮（AI 不许点，要经工具）；点了发布弹「确认发布？」，确认后显示结果
const agentPage = `<!doctype html><body>
<div class="item"><span>* 标题</span><div><input id="t" placeholder="填写标题"></div></div>
<div class="item"><span>简介</span><div><div id="d" contenteditable="true"></div></div></div>
<div class="item"><span>* 创作声明</span><div><input id="decl" readonly placeholder="请选择"><ul id="opts" style="display:none"><li>含虚构演绎内容</li><li>含AI生成内容</li></ul></div></div>
<style>#menu { display: none } #row:hover #menu { display: block } #menu span { cursor: pointer }</style>
<div id="row"><a class="more-btn" style="display:inline-block;width:16px;height:16px"><i class="bcc-iconfont bcc-icon-icon_list_more_x"></i></a><div id="menu"><span id="del" style="cursor:pointer">删除稿件</span></div></div>
<button id="pub">发布</button>
<div id="dlg" role="dialog" style="display:none">确认发布？<button id="yes">确定</button></div>
<div id="ok"></div>
<script>
document.getElementById('decl').onclick = () => { document.getElementById('opts').style.display = 'block' }
document.querySelectorAll('#opts li').forEach(li => li.onclick = () => { document.getElementById('decl').value = li.innerText; document.getElementById('opts').style.display = 'none' })
document.getElementById('pub').onclick = () => { document.getElementById('dlg').style.display = 'block' }
document.getElementById('yes').onclick = () => { document.getElementById('dlg').style.display = 'none'; document.getElementById('ok').innerText = '发布成功' }
</script></body>`

func TestAgent(t *testing.T) {
	needChrome(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(agentPage)) }))
	defer srv.Close()

	dir := t.TempDir()
	h := host(dir, &memDB{rows: map[string][]map[string]any{}})
	h.Browser = testBrowser{browser.NewManager(t.TempDir(), nil)}
	idOf := func(prompt, pattern string) string {
		m := regexp.MustCompile(`\[(\d+)\] [^\n]*` + pattern).FindStringSubmatch(prompt)
		if m == nil {
			return "0"
		}
		return m[1]
	}
	var prompts []string
	// b.agent 用对话的模型（ctx.llm 那个）
	h.LLM = func(_ context.Context, system, prompt string) (string, error) {
		if strings.Contains(prompt, "The task is finished") {
			return `{}`, nil
		}
		prompts = append(prompts, prompt)
		switch len(prompts) {
		case 1:
			// 先试着直接点「发布」（不许），同一批里后面的不会再跑
			return `{"thought":"填标题","actions":[{"do":"type","id":` + idOf(prompt, `field="标题"\*`) + `,"text":"我的标题"},{"do":"click","id":` + idOf(prompt, `text="发布"`) + `},{"do":"type","id":1,"text":"不该跑到"}]}`, nil
		case 2:
			return `{"thought":"富文本要用工具","actions":[{"do":"type","id":` + idOf(prompt, `rich`) + `,"text":"x"}]}`, nil
		case 3:
			// 模型一次回好几个对象（把下一轮也预演了）：只认第一个
			return `{"thought":"经工具发布","actions":[{"do":"tool","name":"publish","args":{}}]}{"thought":"预演","actions":[{"do":"done","summary":"不该在这里结束"}]}`, nil
		case 4:
			return `{"thought":"确认弹窗","actions":[{"do":"click","id":` + idOf(prompt, `text="确定"[^\n]*in-dialog`) + `}]}`, nil
		default:
			return `{"do":"done","summary":"发好了","note":"# 测试表单\n- 发布后会弹「确认发布？」，点「确定」"}`, nil
		}
	}
	write(t, dir, "post.ts", `
export async function publish(input: { url: string }, ctx: any) {
  const b = await ctx.browser.open({ profile: 'demo', url: input.url })
  const r = await b.agent('把这条发出去：标题「我的标题」', {
    notes: 'demo.publish',
    avoid: ['发布'],
    tools: { publish: { description: '核对后点发布', run: async () => { await b.click('#pub'); return 'clicked publish; a confirm dialog may open' } } },
  })
  return { r, title: await b.eval(() => document.querySelector('#t').value), ok: await b.text('#ok') }
}
`)
	res, err := Run(context.Background(), h, "post.publish", map[string]any{"url": srv.URL + "/"}, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	out := res.(map[string]any)
	r := out["r"].(map[string]any)
	if r["ok"] != true || out["title"] != "我的标题" || out["ok"] != "发布成功" {
		t.Fatalf("结果：%v", out)
	}
	all := strings.Join(prompts, "\n")
	for _, want := range []string{`is forbidden`, `rich-text editor; use a tool`, `tool publish → clicked publish`, `Open dialogs:`} {
		if !strings.Contains(all, want) {
			t.Errorf("提示里应该有 %q", want)
		}
	}
	if strings.Contains(all, "不该跑到") && strings.Contains(all, "typed into [1]") {
		t.Errorf("一批里出错以后不该接着跑")
	}
	note, err := os.ReadFile(filepath.Join(dir, "user", "notes", "demo.publish.md"))
	if err != nil || !strings.Contains(string(note), "确认发布") {
		t.Fatalf("笔记：%q %v", note, err)
	}
	// 第二次跑：笔记带进提示
	prompts = nil
	if _, err := Run(context.Background(), h, "post.publish", map[string]any{"url": srv.URL + "/"}, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompts[0], "Notes learned in earlier runs here") || !strings.Contains(prompts[0], "点「确定」") {
		t.Fatalf("第二次的提示里应该有笔记：%s", prompts[0])
	}

	// 工具返回 { finish }：直接结束（演练核对完、发布成功），之后再问一次模型要不要更新笔记
	var reflected string
	h.LLM = func(_ context.Context, system, prompt string) (string, error) {
		if strings.Contains(prompt, "The task is finished") {
			reflected = prompt
			return `{"note":"# 测试表单\n- 发布按钮在最下面"}`, nil
		}
		return `{"thought":"演练","actions":[{"do":"tool","name":"publish","args":{}},{"do":"click","id":1}]}`, nil
	}
	write(t, dir, "dry.ts", `
export async function run(input: { url: string }, ctx: any) {
  const b = await ctx.browser.open({ profile: 'demo', url: input.url })
  return b.agent('演练', { notes: 'demo.publish', tools: { publish: { description: '', run: async () => ({ finish: '核对完了，没点发布' }) } } })
}
`)
	res, err = Run(context.Background(), h, "dry.run", map[string]any{"url": srv.URL + "/"}, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if r := res.(map[string]any); r["ok"] != true || r["summary"] != "核对完了，没点发布" || r["noted"] != true || !strings.Contains(reflected, "publish finished") {
		t.Fatalf("finish：%v %q", r, reflected)
	}
}

func TestNotePaths(t *testing.T) {
	r := &runner{h: Host{WorkDir: "/p"}, name: "social/bilibili.publish"}
	seed, mine, err := r.notePaths("bilibili.publish")
	if err != nil || seed != "/p/plugins/social/notes/bilibili.publish.md" || mine != "/p/user/notes/social/bilibili.publish.md" {
		t.Fatalf("%q %q %v", seed, mine, err)
	}
	r.name = "geo.check"
	if seed, mine, _ := r.notePaths("site.form"); seed != "" || mine != "/p/user/notes/site.form.md" {
		t.Fatalf("%q %q", seed, mine)
	}
	if _, _, err := r.notePaths("../x"); err == nil {
		t.Fatal("../x 应该报错")
	}
}

// 函数调用：模型直接回工具调用（一次好几个），插件的工具也是工具
func TestAgentToolCalls(t *testing.T) {
	needChrome(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(agentPage)) }))
	defer srv.Close()
	dir := t.TempDir()
	h := host(dir, &memDB{rows: map[string][]map[string]any{}})
	h.Browser = testBrowser{browser.NewManager(t.TempDir(), nil)}
	h.LLM = func(context.Context, string, string) (string, error) { t.Fatal("有函数调用时不该退回文字 JSON"); return "", nil }
	var turns int
	var gotTools []string
	h.AgentTools = func(_ context.Context, system, prompt string, tools []AgentTool) (*AgentReply, error) {
		if strings.Contains(prompt, "The task is finished") {
			return &AgentReply{Calls: []AgentCall{{Name: "nothing_new", Args: map[string]any{}}}}, nil
		}
		turns++
		if turns == 1 {
			for _, x := range tools {
				gotTools = append(gotTools, x.Name)
			}
			id := regexp.MustCompile(`\[(\d+)\] [^\n]*field="标题"`).FindStringSubmatch(prompt)[1]
			return &AgentReply{Text: "填标题再发布", Usage: AgentUsage{Model: "m1", Input: 100, Output: 10, CacheRead: 50}, Calls: []AgentCall{
				{Name: "type", Args: map[string]any{"id": json.Number(id), "text": "我的标题"}},
				{Name: "select", Args: map[string]any{"id": regexp.MustCompile(`\[(\d+)\] [^\n]*field="创作声明"`).FindStringSubmatch(prompt)[1], "option": "含AI生成内容"}},
				{Name: "publish", Args: map[string]any{}},
			}}, nil
		}
		return &AgentReply{Text: "确认", Usage: AgentUsage{Model: "m1", Input: 200, Output: 20}, Calls: []AgentCall{{Name: "click", Args: map[string]any{"id": regexp.MustCompile(`\[(\d+)\] [^\n]*text="确定"`).FindStringSubmatch(prompt)[1]}}}}, nil
	}
	write(t, dir, "tc.ts", `
export async function run(input: { url: string }, ctx: any) {
  const b = await ctx.browser.open({ profile: 'demo', url: input.url })
  let n = 0
  const r = await b.agent('发出去', { notes: 'tc.publish', avoid: ['发布'], tools: {
    publish: { description: '点发布', params: { type: 'object', properties: {} }, run: async () => {
      n++
      if (n === 1) { await b.click('#pub'); return 'clicked; confirm dialog open' }
      return { finish: 'published' }
    } },
  } })
  return { r, ok: await b.text('#ok'), title: await b.eval(() => document.querySelector('#t').value), decl: await b.eval(() => document.querySelector('#decl').value) }
}
`)
	h.AgentTools = wrapStepThree(h.AgentTools)
	var summary map[string]any
	var progress []string
	res, err := Run(context.Background(), h, "tc.run", map[string]any{"url": srv.URL + "/"}, func(e Event) {
		if e.Type == "progress" {
			if d, ok := e.Data.(map[string]any); ok {
				progress = append(progress, fmt.Sprint(d["message"]))
				if s, ok := d["summary"].(map[string]any); ok {
					summary = s
				}
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	out := res.(map[string]any)
	if r := out["r"].(map[string]any); r["ok"] != true || r["summary"] != "published" || out["ok"] != "发布成功" || out["title"] != "我的标题" || out["decl"] != "含AI生成内容" {
		t.Fatalf("结果：%v", out)
	}
	if strings.Join(gotTools, ",") != "click,type,hover,select,press,scroll,wait,ask_user,done,fail,publish" {
		t.Fatalf("工具：%v", gotTools)
	}
	// 结束时一条带汇总的进度：总耗时、用量累加（两轮 100+200 输入）；每一步的进度只有那句话，没有耗时
	if summary == nil || fmt.Sprint(summary["input"]) != "300" || fmt.Sprint(summary["output"]) != "30" || summary["model"] != "m1" {
		t.Fatalf("汇总：%v", summary)
	}
	if !strings.Contains(strings.Join(progress, "|"), "填标题再发布") || strings.Contains(strings.Join(progress, "|"), "想 ") {
		t.Fatalf("进度：%v", progress)
	}
	if _, err := os.Stat(filepath.Join(dir, "user", "notes", "tc.publish.md")); err == nil {
		t.Fatal("没有新东西不该写笔记")
	}
}

// 第三轮让模型再调一次 publish（这次工具返回 finish）
func wrapStepThree(f func(context.Context, string, string, []AgentTool) (*AgentReply, error)) func(context.Context, string, string, []AgentTool) (*AgentReply, error) {
	n := 0
	return func(ctx context.Context, system, prompt string, tools []AgentTool) (*AgentReply, error) {
		if !strings.Contains(prompt, "The task is finished") {
			n++
			if n == 3 {
				return &AgentReply{Calls: []AgentCall{{Name: "publish", Args: map[string]any{}}}}, nil
			}
		}
		return f(ctx, system, prompt, tools)
	}
}

// 防兜圈：同样的动作、同样的页面第 3 次就停
func TestAgentLoopGuard(t *testing.T) {
	needChrome(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(agentPage)) }))
	defer srv.Close()
	dir := t.TempDir()
	h := host(dir, &memDB{rows: map[string][]map[string]any{}})
	h.Browser = testBrowser{browser.NewManager(t.TempDir(), nil)}
	n := 0
	h.AgentTools = func(_ context.Context, system, prompt string, tools []AgentTool) (*AgentReply, error) {
		n++
		return &AgentReply{Text: "等一下", Calls: []AgentCall{{Name: "wait", Args: map[string]any{"ms": 300}}}}, nil
	}
	write(t, dir, "lg.ts", `
export async function run(input: { url: string }, ctx: any) {
  const b = await ctx.browser.open({ profile: 'demo', url: input.url })
  return b.agent('什么都不做', {})
}
`)
	res, err := Run(context.Background(), h, "lg.run", map[string]any{"url": srv.URL + "/"}, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if r := res.(map[string]any); r["ok"] != false || !strings.Contains(r["reason"].(string), "原地打转") || n != 3 {
		t.Fatalf("%v n=%d", r, n)
	}
}

// hover：菜单靠 CSS :hover 才显示（点击不弹，派发事件也不行），要真的把鼠标移上去
func TestAgentHover(t *testing.T) {
	needChrome(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(agentPage)) }))
	defer srv.Close()
	dir := t.TempDir()
	h := host(dir, &memDB{rows: map[string][]map[string]any{}})
	h.Browser = testBrowser{browser.NewManager(t.TempDir(), nil)}
	h.AgentTools = func(_ context.Context, system, prompt string, tools []AgentTool) (*AgentReply, error) {
		if strings.Contains(prompt, "hovered") {
			return &AgentReply{Calls: []AgentCall{{Name: "done", Args: map[string]any{"summary": "menu open"}}}}, nil
		}
		// 纯图标、没有 href 的 <a>：要能看到它，说明是图标的 class 名
		m := regexp.MustCompile(`\[(\d+)\] a [^\n]*hint="icon: [^"]*more[^"]*"`).FindStringSubmatch(prompt)
		if m == nil {
			t.Fatalf("纯图标按钮没列出来：%s", prompt)
		}
		id := m[1]
		return &AgentReply{Calls: []AgentCall{{Name: "hover", Args: map[string]any{"id": id}}}}, nil
	}
	write(t, dir, "hv.ts", `
export async function run(input: { url: string }, ctx: any) {
  const b = await ctx.browser.open({ profile: 'demo', url: input.url })
  const r = await b.agent('打开更多菜单', {})
  return { r, shown: await b.eval(() => getComputedStyle(document.querySelector('#menu')).display) }
}
`)
	res, err := Run(context.Background(), h, "hv.run", map[string]any{"url": srv.URL + "/"}, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if out := res.(map[string]any); out["shown"] != "block" || out["r"].(map[string]any)["ok"] != true {
		t.Fatalf("%v", out)
	}
}
