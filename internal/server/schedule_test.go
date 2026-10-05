package server

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeSchedules 把 { 文件名: 内容 } 写成 schedules/ 目录（先清空）
func writeSchedules(t *testing.T, root string, files map[string]string) {
	t.Helper()
	dir := filepath.Join(root, WorkspaceSchedulesDir)
	os.RemoveAll(dir)
	os.MkdirAll(dir, 0o755)
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name+".json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestParseSchedules(t *testing.T) {
	root := t.TempDir()
	if defs, err := parseSchedules(root); err != nil || defs != nil {
		t.Fatalf("没有 schedules/ 应该是空：%v %v", defs, err)
	}
	writeSchedules(t, root, map[string]string{
		"leads.sync":    `{"fn":"leads.sync","every":"30m"}`,
		"review.check":  `{"fn":"review.check","at":"09:00"}`,
		"a":             `{"fn":"x.y","every":"1d"}`,
		"b":             `{"fn":"x.y","every":"2h"}`,
		"weekly-report": `{"prompt":"  写周报 ","every":"7d"}`,
	})
	defs, err := parseSchedules(root)
	if err != nil || len(defs) != 5 {
		t.Fatalf("defs=%+v err=%v", defs, err)
	}
	byKey := map[string]scheduleDef{}
	for _, d := range defs {
		byKey[d.key()] = d
	}
	if byKey["leads.sync"].every != 30*time.Minute || byKey["a"].every != 24*time.Hour || byKey["a"].Fn != "x.y" || byKey["b"].Fn != "x.y" {
		t.Errorf("key 应该是文件名：%+v", byKey)
	}
	if d := byKey["weekly-report"]; d.Prompt != "写周报" || d.every != 7*24*time.Hour {
		t.Errorf("prompt 条目：%+v", d)
	}
	for name, bad := range map[string]string{
		"a.b":       `{"fn":"leads","every":"30m"}`,
		"a.c":       `{"fn":"a.b","every":"1m"}`,
		"a.d":       `{"fn":"a.b","at":"25:00"}`,
		"a.e":       `{"fn":"a.b"}`,
		"a.f":       `{`,
		"has space": `{"prompt":"写周报","every":"7d"}`,
		"a.g":       `{"prompt":"写周报","every":"7d"}`,
		"h":         `{"fn":"a.b","prompt":"写周报","every":"7d"}`,
		"i":         `{"prompt":"   ","every":"7d"}`,
	} {
		writeSchedules(t, root, map[string]string{name: bad})
		if _, err := parseSchedules(root); err == nil {
			t.Errorf("%s.json %s 应该报错", name, bad)
		}
	}
}

func TestParseSchedulesLegacy(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, legacySchedulesFile), []byte(`[{"fn":"a.b","every":"1h"}]`), 0o644)
	if _, err := parseSchedules(root); err == nil {
		t.Fatal("只有老的 shuttle.schedules.json 时应该提示升级模板")
	}
}

func TestScheduleNext(t *testing.T) {
	loc := time.Local
	now := time.Date(2026, 9, 25, 10, 0, 0, 0, loc)
	every := scheduleDef{Fn: "a.b", Every: "1h", every: time.Hour}
	if n := every.next(time.Time{}, now); !n.Equal(now) {
		t.Errorf("没跑过应该马上跑：%v", n)
	}
	if n := every.next(now.Add(-30*time.Minute), now); !n.Equal(now.Add(30 * time.Minute)) {
		t.Errorf("every：%v", n)
	}

	at := scheduleDef{Fn: "a.b", At: "09:00"}
	today9 := time.Date(2026, 9, 25, 9, 0, 0, 0, loc)
	// 过了点、今天没跑过：补跑
	if n := at.next(today9.Add(-24*time.Hour), now); n.After(now) {
		t.Errorf("应该补跑：%v", n)
	}
	// 今天跑过了：明天
	if n := at.next(today9.Add(time.Minute), now); !n.Equal(today9.AddDate(0, 0, 1)) {
		t.Errorf("应该是明天：%v", n)
	}
	// 还没到点
	early := time.Date(2026, 9, 25, 8, 0, 0, 0, loc)
	if n := at.next(today9.Add(-24*time.Hour), early); !n.Equal(today9) {
		t.Errorf("应该是今天 9 点：%v", n)
	}
}
