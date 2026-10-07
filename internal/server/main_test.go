package server

import (
	"os"
	"testing"
)

// 测试不碰本机真实的数据目录：creght 的登录（connections/）、缓存都在数据目录下。
// 不隔离的话，跑测试的电脑连着 creght 时，助手会真的去调线上模型（慢、花钱、结果看机器）。
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "annulo-server-test-")
	if err != nil {
		panic(err)
	}
	os.Setenv("ANNULO_DIR", dir)
	os.Setenv("SHUTTLE_DIR", dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
