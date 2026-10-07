package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// annulo upload 发的是和页面一样的 multipart（字段 file、文件名、按扩展名的类型），带 X-Shuttle；报错把服务端的 error 带出来
func TestUploadFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Shuttle") == "" {
			http.Error(w, `{"error":"no header"}`, 403)
			return
		}
		f, h, err := r.FormFile("file")
		if err != nil {
			w.WriteHeader(400)
			json.NewEncoder(w).Encode(map[string]string{"error": "要用 multipart 上传"})
			return
		}
		b, _ := io.ReadAll(f)
		json.NewEncoder(w).Encode(map[string]any{"url": "/_annulo/uploaded/x.png", "size": len(b), "content_type": h.Header.Get("Content-Type") + "|" + h.Filename})
	}))
	defer srv.Close()
	p := filepath.Join(t.TempDir(), "cover.png")
	os.WriteFile(p, []byte("png-bytes"), 0o600)
	r, err := uploadFile(srv.URL, p)
	if err != nil {
		t.Fatal(err)
	}
	if r.URL != "/_annulo/uploaded/x.png" || r.Size != 9 || r.ContentType != "image/png|cover.png" {
		t.Fatalf("%+v", r)
	}
	if _, err := uploadFile(srv.URL, filepath.Dir(p)); err == nil {
		t.Fatal("目录应该报错")
	}
}
