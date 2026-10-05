package config

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
)

// 界面语言：设置里的 Language 是 "" / "auto"（跟随系统，默认）/ "zh" / "en"。
// 生效语言只有 zh / en：auto 时看系统的首选语言（macOS 读 AppleLanguages，Windows 读用户区域，其他看 LANG / LC_ALL），
// 中文开头的是 zh，其余都是 en。外壳界面、左侧后台的 CREGHT_LOCALE cookie、给助手的系统提示都用同一个生效语言，
// 由服务端算好从 status 返回，前端不自己判断，免得几处不一致。

var Languages = []string{"auto", "zh", "en"}

var (
	sysOnce   sync.Once
	sysLocale string
)

// SystemLocale 是系统首选语言对应的 zh / en（只算一次）。
func SystemLocale() string {
	sysOnce.Do(func() {
		lang := ""
		if runtime.GOOS == "darwin" {
			if out, err := exec.Command("defaults", "read", "-g", "AppleLanguages").Output(); err == nil {
				// 输出形如 ( "zh-Hans-CN", "en-US" )，取第一个
				for _, f := range strings.FieldsFunc(string(out), func(r rune) bool { return r == '(' || r == ')' || r == ',' || r == '"' || r == '\n' || r == ' ' }) {
					if f != "" {
						lang = f
						break
					}
				}
			}
		}
		if lang == "" {
			lang = platformLocale() // Windows：系统的用户区域
		}
		if lang == "" {
			for _, k := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
				if v := os.Getenv(k); v != "" {
					lang = v
					break
				}
			}
		}
		sysLocale = "en"
		if strings.HasPrefix(strings.ToLower(lang), "zh") {
			sysLocale = "zh"
		}
	})
	return sysLocale
}

// LanguagePref 是设置里的语言选择，没设过是 auto。
func (c *Config) LanguagePref() string {
	if c.Language == "zh" || c.Language == "en" {
		return c.Language
	}
	return "auto"
}

// Locale 是生效的界面语言：zh / en。
func (c *Config) Locale() string {
	if p := c.LanguagePref(); p != "auto" {
		return p
	}
	return SystemLocale()
}
