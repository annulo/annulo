package coding

import (
	"os"
	"path/filepath"
)

// taskkillPath resolves Windows' taskkill.exe by absolute path so process-tree
// cleanup does not depend on PATH, porting upstream 7af2d27dc (pi#6596):
//
//	join(process.env.SystemRoot ?? "C:\\Windows", "System32", "taskkill.exe")
//
// It lives outside proc_windows.go — with no build constraint — purely so the
// path arithmetic is testable on every host; killProcessTree is its only caller.
//
// LookupEnv rather than Getenv reproduces `??`: only an *absent* SystemRoot
// takes the fallback, while a present-but-empty one is kept and join() drops
// the empty leading segment, exactly as pi does.
func taskkillPath() string {
	systemRoot, ok := os.LookupEnv("SystemRoot")
	if !ok {
		systemRoot = `C:\Windows`
	}
	return filepath.Join(systemRoot, "System32", "taskkill.exe")
}
