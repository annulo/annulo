package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProjectOf(t *testing.T) {
	root := t.TempDir()
	b := filepath.Join(root, "backends")
	os.MkdirAll(filepath.Join(b, "p1", "local"), 0o755)
	for dir, want := range map[string]string{
		filepath.Join(b, "p1"):          "p1",
		filepath.Join(b, "p1", "local"): "p1",
		b:                               "",
		root:                            "",
	} {
		if got := projectOf(b, dir); got != want {
			t.Errorf("%s → %q，要 %q", dir, got, want)
		}
	}
}
