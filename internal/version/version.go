package version

import "runtime/debug"

// 由 ldflags 注入；`go install pkg@vX` 装出来的则从 build info 里补。
var (
	Version = "dev"
	Commit  = ""
	Date    = ""
)

func FromBuildInfo() {
	if Version != "dev" {
		return
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		Version = bi.Main.Version
	}
}
