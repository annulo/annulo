package localfn

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var lastLogPrune sync.Map

// 日志记录运行阶段和诊断；不自动保存 input、返回正文、密钥或 OAuth token。
func runLogger(h Host, name string, emit func(Event)) (func(Event), func(error)) {
	if h.LogDir == "" {
		return emit, func(error) {}
	}
	if err := os.MkdirAll(h.LogDir, 0700); err != nil {
		return emit, func(error) {}
	}
	id := h.RunID
	if id == "" {
		id = fmt.Sprintf("r%d", time.Now().UnixNano())
	}
	f, err := os.OpenFile(filepath.Join(h.LogDir, id+".jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return emit, func(error) {}
	}
	start := time.Now()
	size := 0
	var mu sync.Mutex
	closed := false
	write := func(kind string, data any) {
		if closed || size > 4<<20 && (kind == "progress" || kind == "log") {
			return
		}
		b, err := json.Marshal(map[string]any{"at": time.Now().UTC().Format(time.RFC3339Nano), "type": kind, "data": data})
		if err != nil {
			return
		}
		n, _ := f.Write(append(b, '\n'))
		size += n
	}
	write("start", map[string]any{"run_id": id, "fn": name})
	report := func(e Event) {
		mu.Lock()
		if e.Type == "progress" || e.Type == "log" {
			write(e.Type, e.Data)
		}
		mu.Unlock()
		if emit != nil {
			emit(e)
		}
	}
	finish := func(err error) {
		mu.Lock()
		defer mu.Unlock()
		data := map[string]any{"ms": time.Since(start).Milliseconds()}
		if err != nil {
			data["message"] = err.Error()
			if e, ok := err.(*Error); ok {
				data["stack"] = e.Stack
			}
			write("error", data)
		} else {
			write("result", data)
		}
		f.Close()
		closed = true
	}
	// 保留 30 天；只处理本目录生成的 .jsonl 文件。
	last, loaded := lastLogPrune.LoadOrStore(h.LogDir, time.Now())
	if loaded && time.Since(last.(time.Time)) < time.Hour {
		return report, finish
	}
	lastLogPrune.Store(h.LogDir, time.Now())
	entries, _ := os.ReadDir(h.LogDir)
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".jsonl" {
			if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > 30*24*time.Hour {
				os.Remove(filepath.Join(h.LogDir, e.Name()))
			}
		}
	}
	return report, finish
}
