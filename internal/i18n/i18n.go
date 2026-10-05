// Package i18n 让 Go 这边给用户看的文字（返回给界面、助手的报错和提示）按界面语言出中文或英文。
//
// 用法：每处文字中英两份写在一起，按当前语言取一份。
//
//	i18n.T("没有这个渠道", "No such channel")
//	i18n.Errorf("读 %s 失败：%w", "Failed to read %s: %w", path, err)
//	var ErrX = i18n.New("没有登录", "Not signed in")   // 哨兵错误：Error() 调用时才取语言，errors.Is 照常能比
//
// 当前语言由启动时 SetLocale 注入（服务端、命令行都传 cfg.Locale：设置里的语言，跟随系统时按系统语言），
// 默认中文。日志（log.Printf）不翻译，给开发者看的保持中文。
package i18n

import (
	"fmt"
	"sync/atomic"
)

var locale atomic.Value // func() string

// SetLocale 设置取当前语言的函数，返回 "zh" 或 "en"。每次取文字时调用，所以设置里切换语言立刻生效。
func SetLocale(f func() string) { locale.Store(f) }

// Locale 是当前语言：zh / en，没设置过是 zh。
func Locale() string {
	if f, ok := locale.Load().(func() string); ok && f != nil {
		if l := f(); l == "en" {
			return "en"
		}
	}
	return "zh"
}

// En 当前是不是英文。
func En() bool { return Locale() == "en" }

// T 按当前语言取一份。
func T(zh, en string) string {
	if En() {
		return en
	}
	return zh
}

// Tf 按当前语言取格式串再格式化。
func Tf(zh, en string, args ...any) string { return fmt.Sprintf(T(zh, en), args...) }

// Errorf 按当前语言取格式串，和 fmt.Errorf 一样支持 %w。
func Errorf(zh, en string, args ...any) error { return fmt.Errorf(T(zh, en), args...) }

// Err 是中英两份文字的错误，Error() 调用时按当前语言取。
type Err struct{ Zh, En string }

func (e *Err) Error() string { return T(e.Zh, e.En) }

// New 返回中英两份文字的错误，适合做包级的哨兵错误。
func New(zh, en string) error { return &Err{zh, en} }
