package server

import (
	"bytes"
	"log"
	"net/http"
	"time"
)

// logAPI 记录所有失败的 /_shuttle/api 请求：方法、路径、状态码、耗时、响应体开头。
// 前端只看到状态码时，到 shuttle 的终端输出里能查到原因。
func logAPI(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rec := &recorder{ResponseWriter: w, status: 200}
		start := time.Now()
		next(rec, r)
		if rec.status >= 400 {
			log.Printf("api %s %s -> %d (%s) %s", r.Method, r.URL.Path, rec.status, time.Since(start).Round(time.Millisecond), bytes.TrimSpace(rec.head.Bytes()))
		}
	}
}

type recorder struct {
	http.ResponseWriter
	status int
	head   bytes.Buffer
}

func (r *recorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	if r.status >= 400 && r.head.Len() < 1000 {
		r.head.Write(b[:min(len(b), 1000-r.head.Len())])
	}
	return r.ResponseWriter.Write(b)
}

// Flush 透传给 SSE。
func (r *recorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
