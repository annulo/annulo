package localfn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type memDB struct {
	rows      map[string][]map[string]any
	lastQuery Query
	lastAgg   map[string]any
}

// Query 翻页时游标就是上一页最后一行的 id（行按 id 顺序放）
func (d *memDB) Query(_ context.Context, table string, q Query) ([]map[string]any, string, int64, error) {
	d.lastQuery = q
	var out []map[string]any
	total := int64(0)
	skip := q.Offset
	for _, r := range d.rows[table] {
		ok := true
		for k, v := range q.Where {
			if r[k] != v {
				ok = false
			}
		}
		if q.Cursor != nil && *q.Cursor != "" && r["id"].(string) <= *q.Cursor {
			ok = false
		}
		if !ok {
			continue
		}
		total++
		if skip > 0 {
			skip--
			continue
		}
		if len(out) < q.Limit {
			out = append(out, r)
		}
	}
	next := ""
	if q.Cursor != nil && int64(len(out)) < total && len(out) == q.Limit {
		next = out[len(out)-1]["id"].(string)
	}
	return out, next, total, nil
}

func (d *memDB) Aggregate(_ context.Context, table string, req map[string]any, filter []Cond) ([]map[string]any, bool, error) {
	d.lastAgg, d.lastQuery.Filter = req, filter
	return []map[string]any{{"day": "2026-09-01", "views": 3}}, false, nil
}
func (d *memDB) Get(_ context.Context, table, id string) (map[string]any, error) {
	for _, r := range d.rows[table] {
		if r["id"] == id {
			return r, nil
		}
	}
	return nil, nil
}
func (d *memDB) Insert(_ context.Context, table string, data map[string]any) (map[string]any, error) {
	if table == "nope" {
		return nil, errors.New("不支持的表：nope")
	}
	data["id"] = "r1"
	d.rows[table] = append(d.rows[table], data)
	return data, nil
}
func (d *memDB) Update(context.Context, string, string, map[string]any) (map[string]any, error) {
	return map[string]any{}, nil
}
func (d *memDB) Delete(context.Context, string, string) error { return nil }

func write(t *testing.T, dir, name, code string) {
	t.Helper()
	os.MkdirAll(filepath.Join(dir, Dir), 0o755)
	if err := os.WriteFile(filepath.Join(dir, Dir, name), []byte(code), 0o644); err != nil {
		t.Fatal(err)
	}
}

func host(dir string, db *memDB) Host {
	return Host{
		WorkDir:   dir,
		Workspace: map[string]any{"project_id": "p1"},
		DB:        db,
		Secret:    func(n string) (string, bool) { return map[string]string{"K": "sk-1"}[n], n == "K" },
		Fetch: func(_ context.Context, r FetchRequest) (*FetchResponse, error) {
			if strings.Contains(r.URL, "fail") {
				return nil, errors.New("网络断了")
			}
			return &FetchResponse{Status: 200, Body: []byte(`{"echo":"` + r.Headers["authorization"] + `","method":"` + r.Method + `"}`)}, nil
		},
		LLM: func(_ context.Context, system, prompt string) (string, error) { return "llm:" + prompt, nil },
	}
}

func TestRun(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "util.ts", `export const double = (n: number): number => n * 2`)
	write(t, dir, "demo.ts", `
import { double } from './util'
type In = { channel: string }
export async function check(input: In, ctx: any) {
  const qs = ctx.db.query('qs', { where: { channel: input.channel } }).list
  const r = await fetch('https://api.example.com/x', { method: 'post', headers: { authorization: 'Bearer ' + ctx.secrets.get('K') }, body: { a: 1 } })
  const j = await r.json()
  ctx.progress({ done: 1, total: qs.length })
  console.log('got', j.method)
  const row = ctx.db.insert('runs', { n: double(qs.length) })
  let caught = ''
  try { ctx.db.insert('nope', {}) } catch (e: any) { caught = e.message }
  const answer = await ctx.llm({ prompt: 'hi' })
  const host = new URL('/a?x=1', 'https://Example.com/b').hostname + new URL('https://e.com/?k=v').searchParams.get('k')
  return { host, b64: atob(btoa('hi')), n: qs.length, echo: j.echo, id: row.id, caught, answer, missing: ctx.secrets.get('NOPE') ?? 'none', ws: ctx.workspace?.project_id, locale: ctx.locale }
}
export function boom() { throw new Error('坏了') }
`)
	db := &memDB{rows: map[string][]map[string]any{"qs": {{"id": "q1", "channel": "c1"}, {"id": "q2", "channel": "c1"}, {"id": "q3", "channel": "c2"}}}}
	var events []Event
	res, err := Run(context.Background(), host(dir, db), "demo.check", map[string]any{"channel": "c1"}, func(e Event) { events = append(events, e) })
	if err != nil {
		t.Fatal(err)
	}
	m := res.(map[string]any)
	if m["host"] != "example.comv" || m["b64"] != "hi" || m["n"] != int64(2) || m["echo"] != "Bearer sk-1" || m["id"] != "r1" || m["caught"] != "不支持的表：nope" || m["answer"] != "llm:hi" || m["missing"] != "none" || m["ws"] != "p1" || m["locale"] != "zh" {
		t.Fatalf("结果不对：%#v", m)
	}
	if len(events) != 2 || events[0].Type != "progress" || events[1].Type != "log" {
		t.Fatalf("事件不对：%#v", events)
	}
	if db.rows["runs"][0]["n"] != int64(4) {
		t.Fatalf("写表不对：%#v", db.rows["runs"])
	}

	if _, err := Run(context.Background(), host(dir, db), "demo.boom", nil, func(Event) {}); err == nil || !strings.Contains(err.Error(), "坏了") {
		t.Fatalf("异常应该带上消息，得到 %v", err)
	}
	if _, err := Run(context.Background(), host(dir, db), "demo.nothere", nil, func(Event) {}); err == nil {
		t.Fatal("不存在的导出应该报错")
	}
	if _, err := Run(context.Background(), host(dir, db), "../x.y", nil, func(Event) {}); err == nil {
		t.Fatal("不合法的文件名应该报错")
	}

	fns, err := List(dir)
	if err != nil || len(fns) != 3 { // demo.boom demo.check util 的 double 是箭头函数常量也算导出函数
		t.Fatalf("List 不对：%v %#v", err, fns)
	}
}

func TestRunErrors(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "bad.ts", `export function f( {`)
	write(t, dir, "loop.ts", `export function spin() { while (true) {} }
export async function net() { return (await fetch('https://fail.example')).status }`)
	db := &memDB{rows: map[string][]map[string]any{}}
	if _, err := Run(context.Background(), host(dir, db), "bad.f", nil, func(Event) {}); err == nil || !strings.Contains(err.Error(), "编译失败") {
		t.Fatalf("语法错误应该报编译失败，得到 %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := Run(ctx, host(dir, db), "loop.spin", nil, func(Event) {}); err == nil || !strings.Contains(err.Error(), "已停止") {
		t.Fatalf("超时应该打断死循环，得到 %v", err)
	}
	if _, err := Run(context.Background(), host(dir, db), "loop.net", nil, func(Event) {}); err == nil || !strings.Contains(err.Error(), "网络断了") {
		t.Fatalf("fetch 失败应该变成异常，得到 %v", err)
	}
}

func TestHTMLAndFetchAll(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "page.ts", `
export async function parse(_: any, ctx: any) {
  const doc = ctx.html('<html lang="zh"><head><title> 你好 </title><meta name="description" content="d"></head><body><h1>标题 <b>一</b></h1><img src="a.png"><img src="b.png" alt="x"><script>var x=1</script><p>正文  内容</p></body></html>')
  const rs = await ctx.fetchAll(['https://a.example/1', { url: 'https://fail.example/2' }, 'https://a.example/3'])
  let bad = ''
  try { doc.find('h1[') } catch (e: any) { bad = e.message }
  return {
    title: doc.find('title')[0].text,
    lang: doc.find('html')[0].attrs.lang,
    desc: doc.find('meta[name=description]')[0].attrs.content,
    h1: doc.find('h1').map((h: any) => h.text),
    noAlt: doc.find('img:not([alt])').map((i: any) => i.attrs.src),
    text: doc.text(),
    oks: rs.map((r: any) => r.ok),
    err: rs[1].error,
    timing: typeof rs[0].timing.total_ms,
    bad,
    md: ctx.html('<h2>标题</h2><p>一段 <strong>加粗</strong> 和 <a href="https://e.com">链接</a></p><ul><li>甲</li></ul>').markdown(),
  }
}`)
	res, err := Run(context.Background(), host(dir, &memDB{rows: map[string][]map[string]any{}}), "page.parse", nil, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	m := res.(map[string]any)
	if m["title"] != "你好" || m["lang"] != "zh" || m["desc"] != "d" || m["text"] != "标题 一 正文 内容" || m["err"] != "网络断了" || m["timing"] != "number" || !strings.Contains(m["bad"].(string), "选择器不对") {
		t.Fatalf("结果不对：%#v", m)
	}
	if m["md"] != "## 标题\n\n一段 **加粗** 和 [链接](https://e.com)\n\n- 甲" {
		t.Fatalf("markdown 不对：%q", m["md"])
	}
	if h1 := m["h1"].([]any); len(h1) != 1 || h1[0] != "标题 一" {
		t.Fatalf("h1 不对：%#v", m["h1"])
	}
	if na := m["noAlt"].([]any); len(na) != 1 || na[0] != "a.png" {
		t.Fatalf("noAlt 不对：%#v", m["noAlt"])
	}
	if oks := m["oks"].([]any); oks[0] != true || oks[1] != false || oks[2] != true {
		t.Fatalf("fetchAll 不对：%#v", m["oks"])
	}
}

func TestMCP(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "m.ts", `
export async function ok(_: any, ctx: any) {
  const r = await ctx.mcp('creght', 'list_sites', { limit: 2 })
  let caught = ''
  try { await ctx.mcp('creght', 'publish') } catch (e: any) { caught = e.message }
  const viaCatch = await ctx.mcp('creght', 'publish').catch((e: any) => 'caught: ' + e.message)
  return { r, caught, viaCatch }
}
export async function badArgs(_: any, ctx: any) { return await ctx.mcp('creght', 'x', 'nope') }
`)
	h := host(dir, &memDB{})
	h.MCP = func(_ context.Context, server, tool string, args map[string]any) (any, error) {
		if tool == "publish" {
			return nil, errors.New("MCP creght 的工具 publish 在设置里关掉了")
		}
		return map[string]any{"server": server, "tool": tool, "limit": args["limit"]}, nil
	}
	res, err := Run(context.Background(), h, "m.ok", nil, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	m := res.(map[string]any)
	r := m["r"].(map[string]any)
	if r["server"] != "creght" || r["tool"] != "list_sites" || r["limit"] != int64(2) || !strings.Contains(m["caught"].(string), "关掉了") || !strings.HasPrefix(m["viaCatch"].(string), "caught: ") {
		t.Fatalf("结果不对：%#v", m)
	}
	if _, err := Run(context.Background(), h, "m.badArgs", nil, func(Event) {}); err == nil || !strings.Contains(err.Error(), "要是对象") {
		t.Fatalf("参数不是对象应该报错：%v", err)
	}
}

func TestDBFilterAndAggregate(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "q.ts", `
export function run(_: any, ctx: any) {
  const q = ctx.db.query('qs', { where: { channel: 'c1' }, filter: [{ field: 'date', op: 'gte', value: '2026-09-01' }], order_by: 'date desc', limit: 5 })
  const a = ctx.db.aggregate('daily', { group_by: [{ field: 'date', trunc: 'month', as: 'month' }], metrics: [{ op: 'sum', field: 'views', as: 'views' }], filter: [{ field: 'date', op: 'between', value: ['2026-09-01', '2026-09-30'] }] })
  let bad = ''
  try { ctx.db.query('qs', { filter: [{ field: 'date', op: 'after', value: 1 }] }) } catch (e: any) { bad = e.message }
  return { n: q.list.length, agg: a.list[0].views, truncated: a.truncated, bad }
}
`)
	db := &memDB{rows: map[string][]map[string]any{"qs": {{"id": "q1", "channel": "c1"}, {"id": "q2", "channel": "c2"}}}}
	res, err := Run(context.Background(), host(dir, db), "q.run", nil, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	m := res.(map[string]any)
	if m["n"] != int64(1) || m["agg"] != int64(3) || m["truncated"] != false || !strings.Contains(m["bad"].(string), "operator 是") {
		t.Fatalf("结果不对：%#v", m)
	}
	if db.lastAgg["group_by"] == nil || db.lastAgg["filter"] != nil || len(db.lastQuery.Filter) != 1 || db.lastQuery.Filter[0].Op != "between" {
		t.Fatalf("聚合参数没传对：%#v %#v", db.lastAgg, db.lastQuery.Filter)
	}
}

func TestDBQueryCursor(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "page.ts", `export function all(_: any, ctx: any) {
  const ids: string[] = []
  let cursor = ''
  let pages = 0
  do {
    const r = ctx.db.query('qs', { where: { channel: 'c1' }, limit: 2, cursor })
    ids.push(...r.list.map((x: any) => x.id))
    cursor = r.next_cursor
    pages++
  } while (cursor && pages < 10)
  let bad = ''
  try { ctx.db.query('qs', { cursor: '', order_by: 'date desc' }) } catch (e: any) { bad = e.message }
  return { ids, pages, bad, plain: ctx.db.query('qs', { limit: 2 }).next_cursor }
}`)
	var rows []map[string]any
	for _, id := range []string{"q1", "q2", "q3", "q4", "q5"} {
		rows = append(rows, map[string]any{"id": id, "channel": "c1"})
	}
	rows = append(rows, map[string]any{"id": "q6", "channel": "c2"})
	db := &memDB{rows: map[string][]map[string]any{"qs": rows}}
	res, err := Run(context.Background(), host(dir, db), "page.all", nil, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	m := res.(map[string]any)
	if fmt.Sprint(m["ids"]) != "[q1 q2 q3 q4 q5]" || fmt.Sprint(m["pages"]) != "3" {
		t.Fatalf("翻页读到的不对：%v", m)
	}
	if !strings.Contains(fmt.Sprint(m["bad"]), "order_by") {
		t.Fatalf("cursor 和 order_by 同时用应该报错：%v", m["bad"])
	}
	if db.lastQuery.OrderBy != "" || db.lastQuery.Cursor != nil {
		t.Fatalf("不传 cursor 时不该翻页：%+v", db.lastQuery)
	}
}

// 站点 Func 的写法：filter 的 { match, conditions }、operator 的符号写法、offset、total 是总数、默认 20 条、update 返回 { ok: true }
func TestDBLikeSiteFunc(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "f.ts", `export function run(_: any, ctx: any) {
  const q = ctx.db.query('qs', { filter: { match: 'and', conditions: [{ fieldId: 'body.views', operator: '>=', value: 10 }, { fieldId: 'channel', value: 'c1' }] }, limit: 2, offset: 1 })
  const d = ctx.db.query('qs', {})
  const u = ctx.db.update('qs', 'q1', { title: 'x' })
  let bad = ''
  try { ctx.db.query('qs', { filter: { match: 'or', conditions: [] } }) } catch (e: any) { bad = e.message }
  let both = ''
  try { ctx.db.query('qs', { cursor: '', offset: 2 }) } catch (e: any) { both = e.message }
  return { ids: q.list.map((x: any) => x.id), total: q.total, dlen: d.list.length, dtotal: d.total, u, bad, both }
}`)
	var rows []map[string]any
	for i := 1; i <= 25; i++ {
		rows = append(rows, map[string]any{"id": fmt.Sprintf("q%02d", i), "channel": "c1"})
	}
	db := &memDB{rows: map[string][]map[string]any{"qs": rows}}
	res, err := Run(context.Background(), host(dir, db), "f.run", nil, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	m := res.(map[string]any)
	if fmt.Sprint(m["ids"]) != "[q02 q03]" || m["total"] != int64(25) || m["dlen"] != int64(20) || m["dtotal"] != int64(25) {
		t.Fatalf("查询结果不对：%v", m)
	}
	if fmt.Sprint(m["u"]) != "map[ok:true]" || !strings.Contains(fmt.Sprint(m["bad"]), "AND") || !strings.Contains(fmt.Sprint(m["both"]), "offset") {
		t.Fatalf("update 返回或报错不对：%v", m)
	}
}

func TestParseCondsSiteFuncForm(t *testing.T) {
	cs, err := ParseConds(map[string]any{"conditions": []any{
		map[string]any{"fieldId": "body.date", "operator": "between", "value": []any{"a", "b"}},
		map[string]any{"fieldId": "status", "operator": "!=", "value": "done"},
		map[string]any{"fieldId": "channel", "value": "c1"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(cs) != "[{date between [a b]} {status neq done} {channel eq c1}]" {
		t.Fatalf("条件不对：%v", cs)
	}
	for _, bad := range []any{
		map[string]any{"conditions": []any{map[string]any{"fieldId": "a", "op": "eq", "operator": "eq"}}},
		map[string]any{"conditions": []any{map[string]any{"fieldId": "a", "field": "b"}}},
		map[string]any{"conditions": []any{map[string]any{"fieldId": "a", "operator": "like"}}},
		map[string]any{"match": "or", "conditions": []any{}},
		map[string]any{"where": []any{}},
	} {
		if _, err := ParseConds(bad); err == nil {
			t.Fatalf("应该报错：%v", bad)
		}
	}
}

func TestFileExt(t *testing.T) {
	for _, c := range []struct{ ct, url, want string }{
		{"video/mp4", "https://x/a", ".mp4"},
		{"video/quicktime; charset=binary", "https://x/a", ".mov"},
		{"application/octet-stream", "https://x/clip.webm?sig=1", ".webm"},
		{"", "https://x/noext", ".bin"},
		{"image/png", "https://x/a.jpg", ".png"},
	} {
		if got := fileExt(c.ct, c.url); got != c.want {
			t.Errorf("fileExt(%q, %q) = %q, want %q", c.ct, c.url, got, c.want)
		}
	}
}

// ctx.oauth：不给账号用默认的，{ account } 指定账号；ctx.oauth.accounts 列出连着的账号
func TestOAuthAccounts(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "g.ts", `
export async function run(_: unknown, ctx: any) {
  const list = ctx.oauth.accounts('google')
  return { list, def: await ctx.oauth('google'), b: await ctx.oauth('google', { account: list[1] }) }
}`)
	h := host(dir, &memDB{rows: map[string][]map[string]any{}})
	h.OAuth = func(_ context.Context, provider, account string) (string, error) {
		return provider + ":" + account, nil
	}
	h.OAuthAccounts = func(string) ([]string, error) { return []string{"a@x.com", "b@x.com"}, nil }
	res, err := Run(context.Background(), h, "g.run", nil, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(res)
	if string(b) != `{"b":"google:b@x.com","def":"google:","list":["a@x.com","b@x.com"]}` {
		t.Fatalf("res = %s", b)
	}
}
