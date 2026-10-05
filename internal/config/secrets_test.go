package config

import (
	"os"
	"testing"
)

func TestSecretsNotInProcessEnv(t *testing.T) {
	c := &Config{Dir: t.TempDir()}
	s := c.LoadSecrets()
	const name = "SHUTTLE_TEST_SECRET_KEY"
	os.Unsetenv(name)
	if err := s.Set(name, "sk-test-123456"); err != nil {
		t.Fatal(err)
	}
	// 助手的 bash 继承进程环境：密钥不能出现在里面
	if v := os.Getenv(name); v != "" {
		t.Fatalf("密钥不该设进进程环境变量：%q", v)
	}
	if v := Lookup(name); v != "sk-test-123456" {
		t.Fatalf("Lookup = %q", v)
	}
	if got := os.Expand("Bearer ${"+name+"}", Lookup); got != "Bearer sk-test-123456" {
		t.Fatalf("expand = %q", got)
	}
	found := false
	for _, kv := range All() {
		found = found || kv == name+"=sk-test-123456"
	}
	if !found {
		t.Fatal("All 里应该有它（给 MCP 本地进程）")
	}
	// 终端 export 的也认
	t.Setenv("SHUTTLE_TEST_ENV_ONLY", "from-env")
	if Lookup("SHUTTLE_TEST_ENV_ONLY") != "from-env" {
		t.Fatal("环境变量里的也要能取到")
	}
	if err := s.Delete(name); err != nil || Lookup(name) != "" {
		t.Fatalf("删掉后还在：%v", err)
	}
}
