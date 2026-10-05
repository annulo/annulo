package i18n

import (
	"errors"
	"fmt"
	"testing"
)

func TestLocale(t *testing.T) {
	defer SetLocale(nil)
	errX := New("没有登录", "Not signed in")
	wrapped := Errorf("读取失败：%w", "Read failed: %w", errX)

	SetLocale(func() string { return "zh" })
	if T("中", "en") != "中" || errX.Error() != "没有登录" || Locale() != "zh" {
		t.Fatal("zh")
	}
	lang := "zh"
	SetLocale(func() string { return lang })
	lang = "en" // 切换语言立刻生效：哨兵错误在 Error() 时才取
	if T("中", "en") != "en" || errX.Error() != "Not signed in" || !En() {
		t.Fatal("en")
	}
	if !errors.Is(wrapped, errX) {
		t.Fatal("%w 包装后 errors.Is 要能认出来")
	}
	if got := fmt.Sprint(Errorf("%d 个", "%d items", 3)); got != "3 items" {
		t.Fatal(got)
	}
	SetLocale(func() string { return "fr" }) // 不认识的都当中文
	if Locale() != "zh" {
		t.Fatal("fallback")
	}
}
