//go:build !windows

package localcmd

import (
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// SetProcGroup 让命令（外部 agent、ctx.exec）和它起的子进程在一个进程组里；停止时把整棵进程树都杀掉
// （Codex 跑命令时会另起进程组，只杀进程组的话它跑的 sleep、脚本会留下来）。
func SetProcGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		pid := cmd.Process.Pid
		for _, p := range descendants(pid) {
			syscall.Kill(p, syscall.SIGKILL)
		}
		return syscall.Kill(-pid, syscall.SIGKILL)
	}
}

// descendants 是 pid 的全部子孙进程（按 ps 的父子关系）。
func descendants(pid int) []int {
	out, err := exec.Command("ps", "-A", "-o", "pid=", "-o", "ppid=").Output()
	if err != nil {
		return nil
	}
	kids := map[int][]int{}
	for _, l := range strings.Split(string(out), "\n") {
		f := strings.Fields(l)
		if len(f) != 2 {
			continue
		}
		c, _ := strconv.Atoi(f[0])
		p, _ := strconv.Atoi(f[1])
		kids[p] = append(kids[p], c)
	}
	var all []int
	queue := []int{pid}
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		for _, c := range kids[p] {
			all = append(all, c)
			queue = append(queue, c)
		}
	}
	return all
}
