package localfn

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPersistentRunLog(t *testing.T) {
	dir := t.TempDir()
	h := host(dir, &memDB{rows: map[string][]map[string]any{}})
	h.LogDir = t.TempDir()
	h.RunID = "r123"
	write(t, dir, "diagnostic.ts", `export function run(input:any,ctx:any){ctx.progress({message:'opening page'});ctx.log('API status 403');throw new Error('publish rejected')}`)
	_, err := Run(context.Background(), h, "diagnostic.run", map[string]any{"password": "never-store-input"}, func(Event) {})
	if err == nil {
		t.Fatal("expected diagnostic failure")
	}
	raw, err := os.ReadFile(filepath.Join(h.LogDir, "r123.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, part := range []string{"opening page", "API status 403", "publish rejected", "stack"} {
		if !strings.Contains(s, part) {
			t.Fatalf("missing %q", part)
		}
	}
	if strings.Contains(s, "never-store-input") {
		t.Fatal("input must not be persisted")
	}
	info, _ := os.Stat(filepath.Join(h.LogDir, "r123.jsonl"))
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 { // Windows 没有 Unix 权限位
		t.Fatal("private logs require mode 0600")
	}
}
