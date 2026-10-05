package wsgit

import (
	"os"
	"path/filepath"
	"testing"
)

func read(t *testing.T, dir, p string) string {
	b, err := os.ReadFile(filepath.Join(dir, p))
	if err != nil {
		return "<没有>"
	}
	return string(b)
}
