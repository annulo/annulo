package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/annulo/annulo/internal/config"
	"github.com/annulo/annulo/internal/creght"
	"github.com/annulo/annulo/internal/tasks"
)

func writeTask(t *testing.T, dir, id, content string) {
	t.Helper()
	os.MkdirAll(filepath.Join(dir, "tasks"), 0o755)
	if err := os.WriteFile(filepath.Join(dir, "tasks", id+".md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestParseSchedulesTask(t *testing.T) {
	dir := t.TempDir()
	writeTask(t, dir, "weekly-report", "---\nname: 写周报\n---\n先拿数字")

	writeSchedules(t, dir, map[string]string{"weekly-report": `{"task":"weekly-report","every":"7d"}`})
	defs, err := parseSchedules(dir)
	if err != nil || len(defs) != 1 || defs[0].key() != "weekly-report" || defs[0].Task != "weekly-report" {
		t.Fatalf("defs=%+v err=%v", defs, err)
	}
	for _, bad := range []string{
		`{"task":"missing","every":"7d"}`,
		`{"task":"weekly-report","prompt":"x","every":"7d"}`,
		`{"task":"weekly-report","fn":"a.b","every":"7d"}`,
		`{"task":"../x","every":"7d"}`,
	} {
		writeSchedules(t, dir, map[string]string{"weekly-report": bad})
		if _, err := parseSchedules(dir); err == nil {
			t.Errorf("%s 应该报错", bad)
		}
	}
}

func TestAPITasks(t *testing.T) {
	ws := t.TempDir()
	s := &Server{cfg: &config.Config{Dir: t.TempDir()}, ws: &creght.Workspace{Dir: ws, ProjectID: "p1"}}
	writeTask(t, ws, "weekly-report", "---\nname: 写周报\ndescription: 每周一期\n---\n\n先拿数字\n")

	do := func(method, rest, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, "/x", strings.NewReader(body))
		s.apiTasks(w, r, rest)
		return w
	}
	w := do(http.MethodGet, "", "")
	var list struct {
		List []struct {
			ID, Name, Description, File, Body string
			Running                           []any
		}
	}
	json.Unmarshal(w.Body.Bytes(), &list)
	if w.Code != 200 || len(list.List) != 1 || list.List[0].Name != "写周报" || list.List[0].Body != "先拿数字" || list.List[0].File != "tasks/weekly-report.md" || list.List[0].Running == nil {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}

	if w := do(http.MethodPut, "weekly-report", `{"body":"新的要求\n写完发邮件"}`); w.Code != 200 {
		t.Fatalf("put: %d %s", w.Code, w.Body)
	}
	b, _ := os.ReadFile(filepath.Join(ws, "tasks", "weekly-report.md"))
	if string(b) != "---\nname: 写周报\ndescription: 每周一期\n---\n\n新的要求\n写完发邮件\n" {
		t.Fatalf("saved: %q", b)
	}
	if w := do(http.MethodPut, "weekly-report", `{"body":"  "}`); w.Code != http.StatusBadRequest {
		t.Errorf("empty body should be 400, got %d", w.Code)
	}
	if w := do(http.MethodGet, "nope", ""); w.Code != http.StatusNotFound {
		t.Errorf("missing task should be 404, got %d", w.Code)
	}

	// 同一个任务、同样的参数正在跑：不重复开；参数不同可以同时跑
	tk, _ := loadTaskForTest(ws, "weekly-report")
	if _, err := s.claimTask(tk, map[string]any{"topic_id": "a"}, "c1", false, timeNow()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.claimTask(tk, map[string]any{"topic_id": "a"}, "c2", false, timeNow()); err == nil {
		t.Error("same input should conflict")
	}
	// Windows 时钟粗，紧挨着开的两条时间可能一样，排不出先后
	if _, err := s.claimTask(tk, map[string]any{"topic_id": "b"}, "c3", false, timeNow().Add(time.Second)); err != nil {
		t.Errorf("different input should run: %v", err)
	}
	w = do(http.MethodGet, "weekly-report", "")
	var one struct {
		Running []struct {
			ChatID string `json:"chat_id"`
		}
	}
	json.Unmarshal(w.Body.Bytes(), &one)
	if len(one.Running) != 2 || one.Running[0].ChatID != "c1" {
		t.Errorf("running: %s", w.Body)
	}
}

func loadTaskForTest(root, id string) (*tasks.Task, error) { return tasks.Load(root, id) }

func timeNow() time.Time { return time.Now() }
