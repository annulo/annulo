package webui

import (
	"embed"
	"io/fs"
)

// dist 由 `make web` 从 web/dist 拷进来；仓库里只提交 .gitkeep，否则新克隆编译不过。
//
//go:embed all:dist
var embedded embed.FS

// FS 返回前端产物；没构建过（只有 .gitkeep）时返回 nil。
func FS() fs.FS {
	sub, err := fs.Sub(embedded, "dist")
	if err != nil {
		return nil
	}
	if _, err := sub.Open("index.html"); err != nil {
		return nil
	}
	return sub
}
