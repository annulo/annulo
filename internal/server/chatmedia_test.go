package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestChatMedia(t *testing.T) {
	s := newTestServer(nil, nil, nil)
	dir := t.TempDir()
	img := filepath.Join(dir, "二维码 1.png")
	os.WriteFile(img, []byte("0123456789"), 0o600)
	os.WriteFile(filepath.Join(dir, "a.command"), []byte("echo hi"), 0o700)

	ask := func(path string) (int, map[string]any) {
		body, _ := json.Marshal(map[string]string{"path": path})
		w := httptest.NewRecorder()
		s.apiChatMedia(w, httptest.NewRequest(http.MethodPost, "/_shuttle/api/local/media", strings.NewReader(string(body))))
		var got map[string]any
		json.Unmarshal(w.Body.Bytes(), &got)
		return w.Code, got
	}

	code, got := ask("file://" + img)
	if code != 200 || got["kind"] != "image" || got["name"] != "二维码 1.png" {
		t.Fatalf("图片：%d %v", code, got)
	}
	mp3 := filepath.Join(dir, "旁白.mp3")
	os.WriteFile(mp3, []byte("ID3"), 0o600)
	if code, got := ask(mp3); code != 200 || got["kind"] != "audio" {
		t.Fatalf("音频：%d %v", code, got)
	}
	// PDF 不带 CSP sandbox，不然浏览器的 PDF 查看器打不开
	pdf := filepath.Join(dir, "报价.pdf")
	os.WriteFile(pdf, []byte("%PDF-1.4"), 0o600)
	if code, pg := ask(pdf); code != 200 || pg["kind"] != "pdf" {
		t.Fatalf("PDF：%d %v", code, pg)
	} else {
		pw := httptest.NewRecorder()
		s.handleChatMedia(pw, httptest.NewRequest(http.MethodGet, pg["url"].(string), nil))
		if pw.Code != 200 || pw.Header().Get("Content-Security-Policy") != "" || pw.Header().Get("Content-Type") != "application/pdf" {
			t.Fatalf("PDF 预览：%d %v", pw.Code, pw.Header())
		}
	}

	// 地址能给 <img> 用，支持 Range
	req := httptest.NewRequest(http.MethodGet, got["url"].(string), nil)
	req.Header.Set("Range", "bytes=2-4")
	w := httptest.NewRecorder()
	s.handleChatMedia(w, req)
	if b, _ := io.ReadAll(w.Body); w.Code != http.StatusPartialContent || string(b) != "234" {
		t.Fatalf("预览：%d %q", w.Code, b)
	}

	// 不是图片、视频、音频，相对路径，不存在的文件，都不给
	for _, bad := range []string{filepath.Join(dir, "a.command"), "a.png", filepath.Join(dir, "nope.png"), dir + "/"} {
		if code, _ := ask(bad); code != http.StatusBadRequest {
			t.Errorf("%s 应该被拒，得到 %d", bad, code)
		}
	}
	// 没发过的 token 不给
	w = httptest.NewRecorder()
	s.handleChatMedia(w, httptest.NewRequest(http.MethodGet, chatMediaURL+"0123456789abcdef0123456789abcdef/x.png", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("乱猜的 token：%d", w.Code)
	}
}
