package localsite

import (
	_ "embed"
	"sort"
	"strings"
)

// default.css 照抄平台（tailwind/default.css）：shadcn 的默认色板。本地用 Tailwind browser，
// 里面的 @import "tailwindcss" 和 typography 插件在 runtime.go 的 baseCSS 里去掉。
//
//go:embed default.css
var defaultCSS string

// indexCSS 收集项目里所有 index.css（按路径排序拼起来），和平台 pickIndexCssFromFileMap 一样。
func indexCSS(files []string, read func(string) string) string {
	var ps []string
	for _, p := range files {
		if l := strings.ToLower(p); l == "index.css" || strings.HasSuffix(l, "/index.css") {
			ps = append(ps, p)
		}
	}
	sort.Strings(ps)
	var b strings.Builder
	for _, p := range ps {
		b.WriteString(strings.TrimSpace(read(p)))
		b.WriteByte('\n')
	}
	return b.String()
}
