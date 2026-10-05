package server

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// 这台电脑是谁（ctx.workspace.machine）：浏览器登录态只在登录的那台电脑上，
// 本机函数记下账号是在哪台电脑登录的，别的电脑（或者另起的一个 Shuttle，数据目录不同）跑定时任务时就知道跳过它，不会误判成登录过期。
var machines sync.Map // 数据目录 → map[string]any{id, name}

// machine 返回 { id, name }：id 第一次用到时随机生成，存在 <数据目录>/machine-id，跟着数据目录走；
// name 是电脑名（macOS 的「电脑名称」，其他系统用主机名），只用来给用户看。
func machine(dir string) map[string]any {
	if v, ok := machines.Load(dir); ok {
		return v.(map[string]any)
	}
	file := filepath.Join(dir, "machine-id")
	id := ""
	if b, err := os.ReadFile(file); err == nil {
		id = strings.TrimSpace(string(b))
	}
	if id == "" {
		buf := make([]byte, 12)
		_, _ = rand.Read(buf)
		id = hex.EncodeToString(buf)
		_ = os.WriteFile(file, []byte(id+"\n"), 0o600)
	}
	m := map[string]any{"id": id, "name": computerName()}
	v, _ := machines.LoadOrStore(dir, m)
	return v.(map[string]any)
}

func computerName() string {
	if runtime.GOOS == "darwin" {
		if out, err := exec.Command("scutil", "--get", "ComputerName").Output(); err == nil {
			if n := strings.TrimSpace(string(out)); n != "" {
				return n
			}
		}
	}
	n, _ := os.Hostname()
	return strings.TrimSuffix(n, ".local")
}
