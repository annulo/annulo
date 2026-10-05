package localsite

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// 热更新推给页面（GET /_client/events，Server-Sent Events）：每个打开的后台标签连一条，
// 构建完就把这次的变化（update）推给所有连着的页面，页面里的运行时（runtime.go）决定怎么应用。

type hub struct {
	mu   sync.Mutex
	subs map[chan []byte]struct{}
}

func (h *hub) broadcast(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.subs {
		select {
		case c <- b:
		default: // 页面卡住了收不过来：丢掉，它下次连上会按 hello 刷新
		}
	}
}

// ServeEvents：连上先发 hello（带当前构建号），页面发现和自己加载时的构建号不一样就刷新。
func (r *Renderer) ServeEvents(w http.ResponseWriter, req *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("content-type", "text/event-stream")
	w.Header().Set("cache-control", "no-store")
	c := make(chan []byte, 16)
	r.events.mu.Lock()
	if r.events.subs == nil {
		r.events.subs = map[chan []byte]struct{}{}
	}
	r.events.subs[c] = struct{}{}
	r.events.mu.Unlock()
	defer func() {
		r.events.mu.Lock()
		delete(r.events.subs, c)
		r.events.mu.Unlock()
	}()
	r.mu.Lock()
	cur := r.cur
	r.mu.Unlock()
	if cur != nil {
		b, _ := json.Marshal(map[string]any{"type": "hello", "build": cur.id})
		fmt.Fprintf(w, "data: %s\n\n", b)
	}
	fl.Flush()
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-req.Context().Done():
			return
		case b := <-c:
			fmt.Fprintf(w, "data: %s\n\n", b)
			fl.Flush()
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		}
	}
}
