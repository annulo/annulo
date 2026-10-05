package server

import (
	"github.com/annulo/annulo/internal/config"
	"github.com/annulo/annulo/internal/creght"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunLogsProjectIsolation(t *testing.T) {
	dir := t.TempDir()
	s := &Server{cfg: &config.Config{Dir: dir}, ws: &creght.Workspace{ProjectID: "p1"}}
	for _, project := range []string{"p1", "p2"} {
		d := filepath.Join(dir, "logs", "local", project)
		os.MkdirAll(d, 0700)
		os.WriteFile(filepath.Join(d, "r123.jsonl"), []byte(`{"type":"start","data":{"run_id":"r123","fn":"social.publish"}}`+"\n"+`{"type":"error","data":{"message":"`+project+`"}}`), 0600)
	}
	w := httptest.NewRecorder()
	s.apiRunLogs(w, httptest.NewRequest("GET", "/?id=r123", nil))
	if !strings.Contains(w.Body.String(), "p1") || strings.Contains(w.Body.String(), "p2") {
		t.Fatal("logs leaked between projects")
	}
	w = httptest.NewRecorder()
	s.apiRunLogs(w, httptest.NewRequest("GET", "/?id=../../secrets", nil))
	if w.Code != 400 {
		t.Fatal("path traversal must be rejected")
	}
	w = httptest.NewRecorder()
	s.apiRunLogs(w, httptest.NewRequest("GET", "/?fn=other.function", nil))
	if !strings.Contains(w.Body.String(), `"list":[]`) {
		t.Fatal("function filter ignored")
	}
}
