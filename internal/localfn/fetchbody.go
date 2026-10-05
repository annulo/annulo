package localfn

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dop251/goja"

	"github.com/annulo/annulo/internal/i18n"
)

// fetch 的响应体和 creght 站点 Func 的 fetch 同一套写法（creght 平台的站点 Func 运行时），
// 带 export const cloud 的函数在两边都能跑：
//
//	const reader = res.body.getReader()
//	const dec = new TextDecoder()
//	for (;;) { const { done, value } = await reader.read(); if (done) break; buf += dec.decode(value, { stream: true }) }
//
// text() / json() / arrayBuffer() 读剩下的全部。读是同步的（函数里一次一个），read() 返回已完成的 Promise。

// maxFetchBody 是一个响应体最多读多少。text() 超了截断、标 truncated（和以前一样）；
// 流式读超了直接报错，和站点 Func 一样。
const maxFetchBody = 10 << 20

type fetchBody struct {
	rc        io.Reader
	closer    io.Closer
	start     time.Time
	read      int
	done      bool
	truncated bool
	totalMs   int64 // 读完的时候记下来
}

func newFetchBody(resp *FetchResponse, start time.Time) *fetchBody {
	if resp.Stream != nil {
		return &fetchBody{rc: resp.Stream, closer: resp.Stream, start: start}
	}
	// Host 已经整段读完的（测试、本机存的上传文件）：当成读完了的流
	return &fetchBody{rc: bytes.NewReader(resp.Body), start: start, truncated: resp.Truncated, totalMs: resp.TotalMs}
}

func (b *fetchBody) finish() {
	if b.done {
		return
	}
	b.done = true
	if b.totalMs == 0 {
		b.totalMs = time.Since(b.start).Milliseconds()
	}
	if b.closer != nil {
		b.closer.Close()
	}
}

// chunk 读下一块；读完返回 nil, nil
func (b *fetchBody) chunk() ([]byte, error) {
	if b.done {
		return nil, nil
	}
	buf := make([]byte, 32<<10)
	for {
		n, err := b.rc.Read(buf)
		if n > 0 {
			b.read += n
			if b.read > maxFetchBody {
				b.finish()
				return nil, i18n.Errorf("响应超过 %d MB", "The response is larger than %d MB", maxFetchBody>>20)
			}
			return buf[:n], nil
		}
		if err == io.EOF {
			b.finish()
			return nil, nil
		}
		if err != nil {
			b.finish()
			return nil, err
		}
	}
}

// all 读剩下的全部。超过上限、读到一半断了（超时）不算失败：读到的照样返回，标 truncated
func (b *fetchBody) all() []byte {
	if b.done {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(b.rc, int64(maxFetchBody-b.read)+1))
	b.read += len(data)
	if err != nil || b.read > maxFetchBody {
		b.truncated = true
	}
	if b.read > maxFetchBody {
		data = data[:len(data)-(b.read-maxFetchBody)]
	}
	b.finish()
	return data
}

func (r *runner) response(resp *FetchResponse, start time.Time) *goja.Object {
	b := newFetchBody(resp, start)
	r.bodies = append(r.bodies, b)
	vm := r.vm
	o := vm.NewObject()
	timing := vm.NewObject()
	timing.Set("ttfb_ms", resp.TTFBMs)
	// total_ms、truncated 要等读完才知道
	timing.DefineAccessorProperty("total_ms", vm.ToValue(func() int64 {
		if !b.done {
			return time.Since(b.start).Milliseconds()
		}
		return b.totalMs
	}), nil, goja.FLAG_FALSE, goja.FLAG_TRUE)
	o.Set("timing", timing)
	o.DefineAccessorProperty("truncated", vm.ToValue(func() bool { return b.truncated }), nil, goja.FLAG_FALSE, goja.FLAG_TRUE)
	o.Set("status", resp.Status)
	o.Set("statusText", resp.StatusText)
	o.Set("ok", resp.Status >= 200 && resp.Status < 300)
	o.Set("url", resp.URL)
	headers := vm.NewObject()
	headers.Set("get", func(name string) any {
		for k, v := range resp.Headers {
			if strings.EqualFold(k, name) {
				return v
			}
		}
		return nil
	})
	headers.Set("forEach", func(fn goja.Callable) {
		for k, v := range resp.Headers {
			fn(goja.Undefined(), vm.ToValue(v), vm.ToValue(k))
		}
	})
	o.Set("headers", headers)

	reader := vm.NewObject()
	reader.Set("read", func() *goja.Promise {
		c, err := b.chunk()
		if err != nil {
			return r.rejected(i18n.Errorf("读 %s 的响应失败：%w", "Reading the response from %s failed: %w", resp.URL, err))
		}
		if c == nil {
			return r.resolved(map[string]any{"done": true, "value": goja.Undefined()})
		}
		return r.resolved(map[string]any{"done": false, "value": r.uint8Array(c)})
	})
	reader.Set("cancel", func() *goja.Promise { b.finish(); return r.resolved(goja.Undefined()) })
	reader.Set("releaseLock", func() {})
	body := vm.NewObject()
	body.Set("getReader", func() *goja.Object { return reader })
	o.Set("body", body)

	o.Set("text", func() *goja.Promise { return r.resolved(string(b.all())) })
	o.Set("json", func() *goja.Promise {
		data := b.all()
		var v any
		if err := json.Unmarshal(data, &v); err != nil {
			return r.rejected(i18n.Errorf("响应不是 JSON（%d）：%.200s", "The response isn't JSON (%d): %.200s", resp.Status, data))
		}
		return r.resolved(v)
	})
	o.Set("arrayBuffer", func() *goja.Promise { return r.resolved(vm.NewArrayBuffer(b.all())) })
	return o
}

func (r *runner) uint8Array(data []byte) goja.Value {
	ab := r.vm.NewArrayBuffer(append([]byte(nil), data...))
	if ctor, ok := goja.AssertConstructor(r.vm.Get("Uint8Array")); ok {
		if v, err := ctor(nil, r.vm.ToValue(ab)); err == nil {
			return v
		}
	}
	return r.vm.ToValue(ab)
}

// bytesOf 把 Uint8Array / ArrayBuffer / 字符串转成字节
func (r *runner) bytesOf(v goja.Value) []byte {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return nil
	}
	switch x := v.Export().(type) {
	case goja.ArrayBuffer:
		return x.Bytes()
	case string:
		return []byte(x)
	}
	var data []byte
	if err := r.vm.ExportTo(v, &data); err != nil {
		r.throw(i18n.Errorf("要传 Uint8Array 或 ArrayBuffer：%v", "Expected a Uint8Array or ArrayBuffer: %v", err))
	}
	return data
}

// installText 装上 TextDecoder / TextEncoder（只有 UTF-8）。decode(x, { stream: true }) 会把
// 被分块切开的半个字符留到下一次，流式读中文不会出乱码。
func (r *runner) installText() {
	r.vm.Set("TextDecoder", func(call goja.ConstructorCall) *goja.Object {
		var pending []byte
		d := call.This
		d.Set("encoding", "utf-8")
		d.Set("decode", func(v goja.Value, opts map[string]any) string {
			data := append(pending, r.bytesOf(v)...)
			pending = nil
			if stream, _ := opts["stream"].(bool); stream {
				// 末尾不完整的 UTF-8 序列留到下一块
				for i := 1; i <= 3 && i <= len(data); i++ {
					c := data[len(data)-i]
					if c&0xC0 != 0x80 { // 找到这个字符的首字节
						if !utf8.FullRune(data[len(data)-i:]) {
							pending = append([]byte(nil), data[len(data)-i:]...)
							data = data[:len(data)-i]
						}
						break
					}
				}
			}
			return strings.ToValidUTF8(string(data), "�")
		})
		return nil
	})
	r.vm.Set("TextEncoder", func(call goja.ConstructorCall) *goja.Object {
		call.This.Set("encoding", "utf-8")
		call.This.Set("encode", func(s string) goja.Value { return r.uint8Array([]byte(s)) })
		return nil
	})
}
