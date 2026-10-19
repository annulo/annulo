package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
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

// annulo upload --local 把文件原样作为请求体发给 local/files，文件名在 X-Filename（URL 编码）
func TestUploadLocalFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		name, _ := url.QueryUnescape(r.Header.Get("X-Filename"))
		if r.Header.Get("X-Shuttle") == "" || string(b) != "mp4-bytes" || name != "第 1 版.mp4" || r.Header.Get("Content-Type") != "video/mp4" {
			http.Error(w, `{"error":"bad request"}`, 400)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"ref": "local:abc.mp4", "url": "/_annulo/files/abc.mp4", "size": len(b)})
	}))
	defer srv.Close()
	p := filepath.Join(t.TempDir(), "第 1 版.mp4")
	os.WriteFile(p, []byte("mp4-bytes"), 0o600)
	r, err := uploadLocalFile(srv.URL, p)
	if err != nil {
		t.Fatal(err)
	}
	if r.Ref != "local:abc.mp4" || r.Size != 9 {
		t.Fatalf("%+v", r)
	}
}
