package browser

import (
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
)

// fixMojibake：响应头没写字符集的文本（比如豆包的 text/event-stream），Chrome 交出来时按 Windows-1252 解码了，
// UTF-8 的中文变成「å¹´æœ‰」这种乱码。整段能按 Windows-1252 编回字节、编回来又是合法的 UTF-8，就是这种情况，按 UTF-8 重新解码；
// 真的 Latin-1 文本编回来恰好是合法 UTF-8 的几乎没有。没有非 ASCII 字符、编不回去的原样返回。
func fixMojibake(s string) string {
	ascii := true
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			ascii = false
			break
		}
	}
	if ascii {
		return s
	}
	b, err := charmap.Windows1252.NewEncoder().String(s)
	if err != nil || !utf8.ValidString(b) {
		return s
	}
	return b
}
