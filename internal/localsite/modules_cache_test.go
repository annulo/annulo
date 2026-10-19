package localsite

import (
	"os"
	"path/filepath"
	"testing"
)

// import 的文件后补上：内容没变的文件要重新编译，不能一直报缓存着的「找不到 import 的文件」
func TestCompileErrorNotCached(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.ts"), []byte(`import { b } from './b'; export const a = b`), 0o644)
	c := &moduleCache{dir: t.TempDir()}
	if m, _ := c.compileFile(dir, "a.ts"); m == nil || m.Error == "" {
		t.Fatal("b 还没有，应该报错")
	}
	os.WriteFile(filepath.Join(dir, "b.ts"), []byte(`export const b = 1`), 0o644)
	if m, _ := c.compileFile(dir, "a.ts"); m == nil || m.Error != "" {
		t.Fatalf("b 补上了，不该再报错：%+v", m)
	}
}
