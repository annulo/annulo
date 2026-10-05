package localfn

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

// 流式读：一次一个字节吐出来（中文被切成半个字符），getReader + TextDecoder({stream}) 拼回来不乱码；
// 读完后 truncated / timing 有值；没读完的响应体在运行结束时关掉。
func TestFetchStream(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "s.ts", `
export async function read(_: any, ctx: any) {
  const res = await fetch('https://api.example.com/sse', { method: 'POST' })
  const reader = res.body.getReader()
  const dec = new TextDecoder()
  let text = '', chunks = 0
  for (;;) {
    const { done, value } = await reader.read()
    if (done) break
    chunks++
    text += dec.decode(value, { stream: true })
  }
  const again = await reader.read()
  const enc = new TextEncoder().encode('中')
  return { text, chunks, again: again.done, truncated: res.truncated, total: res.timing.total_ms >= 0, ct: res.headers.get('Content-Type'), enc: enc.length }
}
export async function whole() {
  const r = await fetch('https://api.example.com/json')
  return (await r.json()).a
}
export async function leave() {
  await fetch('https://api.example.com/sse')
  return 'ok'
}`)
	var closed int
	h := host(dir, &memDB{rows: map[string][]map[string]any{}})
	h.Fetch = func(_ context.Context, r FetchRequest) (*FetchResponse, error) {
		if !r.Stream {
			t.Fatal("fetch 应该用流式请求")
		}
		body := "data: 你好\n\ndata: [DONE]\n\n"
		if strings.HasSuffix(r.URL, "/json") {
			body = `{"a":1}`
		}
		return &FetchResponse{Status: 200, Headers: map[string]string{"content-type": "text/event-stream"},
			Stream: &closeCounter{Reader: iotest.OneByteReader(strings.NewReader(body)), n: &closed}}, nil
	}
	res, err := Run(context.Background(), h, "s.read", nil, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	m := res.(map[string]any)
	if m["text"] != "data: 你好\n\ndata: [DONE]\n\n" || m["again"] != true || m["truncated"] != false || m["total"] != true || m["ct"] != "text/event-stream" || m["enc"] != int64(3) {
		t.Fatalf("流式读不对：%#v", m)
	}
	if m["chunks"].(int64) < 10 {
		t.Fatalf("应该是一块一块读的：%#v", m["chunks"])
	}
	if res, err := Run(context.Background(), h, "s.whole", nil, func(Event) {}); err != nil || res != int64(1) {
		t.Fatalf("json() 应该读完整个流：%v %v", res, err)
	}
	closed = 0
	if _, err := Run(context.Background(), h, "s.leave", nil, func(Event) {}); err != nil || closed != 1 {
		t.Fatalf("没读的响应体运行结束要关掉：closed=%d %v", closed, err)
	}
}

type closeCounter struct {
	io.Reader
	n *int
}

func (c *closeCounter) Close() error { *c.n++; return nil }

// ctx.llm.providers() / ctx.llm.fetch()：路径和请求交给 Host，响应和 fetch 一样能流式读；ctx.llm('…') 照旧能用
func TestLLMFetch(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "l.ts", `
export async function go(_: any, ctx: any) {
  const ps = ctx.llm.providers()
  const res = await ctx.llm.fetch(ps[0].id, '/responses', { method: 'POST', body: { model: ps[0].models[0].model } })
  const reader = res.body.getReader()
  const { value } = await reader.read()
  const old = await ctx.llm('hi')
  let bad = ''
  try { await ctx.llm.fetch('nope', '/x') } catch (e: any) { bad = e.message }
  let gone = ''
  try { ctx.fetch('https://x') } catch (e: any) { gone = e.message }
  return { first: new TextDecoder().decode(value), old, bad, gone: gone.includes('全局 fetch') }
}`)
	h := host(dir, &memDB{rows: map[string][]map[string]any{}})
	h.LLMProviders = func(context.Context) []map[string]any {
		return []map[string]any{{"id": "p1", "models": []map[string]any{{"model": "gpt-x"}}}}
	}
	h.LLMFetch = func(_ context.Context, provider string, r FetchRequest) (*FetchResponse, error) {
		if provider != "p1" {
			return nil, errors.New("没有这个服务商")
		}
		if r.URL != "/responses" || !r.Stream || r.Body != `{"model":"gpt-x"}` {
			t.Fatalf("请求不对：%#v", r)
		}
		return &FetchResponse{Status: 200, Stream: io.NopCloser(strings.NewReader("data: ok"))}, nil
	}
	res, err := Run(context.Background(), h, "l.go", nil, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	m := res.(map[string]any)
	if m["first"] != "data: ok" || m["old"] != "llm:hi" || !strings.Contains(m["bad"].(string), "没有这个服务商") || m["gone"] != true {
		t.Fatalf("结果不对：%#v", m)
	}
}
