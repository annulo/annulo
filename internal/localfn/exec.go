package localfn

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/dop251/goja"

	"github.com/annulo/annulo/internal/i18n"
	"github.com/annulo/annulo/internal/localcmd"
)

// ctx.exec 的限制：默认等 30 秒、最多 10 分钟；stdout、stderr 各留 10MB，多的截掉
const (
	execTimeout    = 30 * time.Second
	execMaxTimeout = 10 * time.Minute
	execMaxOutput  = 10 << 20
)

// exec：await ctx.exec('openssl', ['x509', '-noout', '-enddate'], { cwd, input, timeout, env })，
// 返回 { code, stdout, stderr, truncated }。命令和参数分开传，不经过 shell（要管道、重定向就 ctx.exec('sh', ['-c', 脚本, '_', 参数…])）。
// 退出码不是 0 不算出错，看 code；找不到命令、超时、cwd 出了项目才 reject。
// 命令按登录 shell 的 PATH 找（localcmd），带路径的（scripts/x.sh）相对 cwd；cwd 默认项目根目录，不能出项目；
// 没给 input 时 stdin 是空的（不会卡在等输入上）；env 加在 Annulo 的环境变量上（密钥用 ctx.secrets.get 读了放这里传给命令）。
func (r *runner) exec(name string, args goja.Value, opts goja.Value) *goja.Promise {
	// ctx.exec('ls', { cwd }) 也认：第二个参数是对象不是数组时当 opts
	if o, ok := args.(*goja.Object); ok && o.ClassName() != "Array" && (opts == nil || goja.IsUndefined(opts)) {
		args, opts = nil, args
	}
	var argv []string
	if args != nil && !goja.IsUndefined(args) && !goja.IsNull(args) {
		list, ok := args.Export().([]any)
		if !ok {
			return r.rejected(i18n.New("ctx.exec 的第二个参数是参数数组：ctx.exec('命令', ['参数1', '参数2'], { … })", "The second argument of ctx.exec is the argument array: ctx.exec('cmd', ['arg1', 'arg2'], { … })"))
		}
		for _, a := range list {
			if a == nil {
				return r.rejected(i18n.New("ctx.exec 的参数里有 null / undefined", "ctx.exec arguments contain null / undefined"))
			}
			argv = append(argv, r.vm.ToValue(a).String())
		}
	}
	var o map[string]any
	if opts != nil && !goja.IsUndefined(opts) && !goja.IsNull(opts) {
		m, ok := toPlain(opts.Export()).(map[string]any)
		if !ok {
			return r.rejected(i18n.New("ctx.exec 的第三个参数要是对象：{ cwd, input, timeout, env }", "The third argument of ctx.exec must be an object: { cwd, input, timeout, env }"))
		}
		o = m
	}

	dir := r.h.WorkDir
	if cwd, _ := o["cwd"].(string); cwd != "" {
		d := filepath.Join(r.h.WorkDir, cwd)
		if filepath.IsAbs(cwd) {
			d = filepath.Clean(cwd)
		}
		if rel, err := filepath.Rel(r.h.WorkDir, d); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return r.rejected(i18n.Errorf("ctx.exec 的 cwd 要在项目里（相对项目根目录写）：%s", "ctx.exec's cwd must be inside the project (relative to the project root): %s", cwd))
		}
		if st, err := os.Stat(d); err != nil || !st.IsDir() {
			return r.rejected(i18n.Errorf("ctx.exec 的 cwd 不是目录：%s", "ctx.exec's cwd isn't a directory: %s", cwd))
		}
		dir = d
	}

	path := name
	if strings.ContainsAny(name, `/\`) {
		if !filepath.IsAbs(name) {
			path = filepath.Join(dir, name)
		}
	} else if path = localcmd.Find(name); path == "" {
		return r.rejected(i18n.Errorf("找不到命令 %s：这台电脑没装，或者不在 PATH 里", "Command not found: %s (not installed on this computer, or not on PATH)", name))
	}

	timeout := execTimeout
	if ms := intOf(o["timeout"], 0); ms > 0 {
		timeout = min(time.Duration(ms)*time.Millisecond, execMaxTimeout)
	}
	var env []string
	if m, ok := o["env"].(map[string]any); ok {
		for k, v := range m {
			if v != nil {
				env = append(env, k+"="+r.vm.ToValue(v).String())
			}
		}
	}

	ctx, cancel := context.WithTimeout(r.ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, argv...)
	localcmd.SetProcGroup(cmd)
	cmd.WaitDelay = 2 * time.Second // 孙进程拿着输出管道不放时，不跟着一直等
	cmd.Dir = dir
	cmd.Env = localcmd.Env(env...)
	if in, ok := o["input"]; ok && in != nil {
		cmd.Stdin = strings.NewReader(r.vm.ToValue(in).String())
	}
	stdout, stderr := &capBuffer{max: execMaxOutput}, &capBuffer{max: execMaxOutput}
	cmd.Stdout, cmd.Stderr = stdout, stderr

	err := cmd.Run()
	code := 0
	var exitErr *exec.ExitError
	switch {
	case r.ctx.Err() != nil:
		return r.rejected(r.ctx.Err())
	case ctx.Err() != nil:
		return r.rejected(i18n.Errorf("ctx.exec %s 超过 %s 没结束，已停止（要等更久传 timeout，单位毫秒，最多 10 分钟）", "ctx.exec %s didn't finish within %s and was stopped (pass a longer timeout in ms, up to 10 minutes)", name, timeout))
	case errors.As(err, &exitErr):
		code = exitErr.ExitCode()
	case err != nil:
		return r.rejected(i18n.Errorf("ctx.exec %s 失败：%w", "ctx.exec %s failed: %w", name, err))
	}
	return r.resolved(map[string]any{
		"code":      code,
		"stdout":    stdout.String(),
		"stderr":    stderr.String(),
		"truncated": stdout.cut || stderr.cut,
	})
}

// capBuffer 只留前 max 字节，多的丢掉（照样返回写成功，命令不会因为管道写不进去而卡住或报错）
type capBuffer struct {
	bytes.Buffer
	max int
	cut bool
}

func (b *capBuffer) Write(p []byte) (int, error) {
	if room := b.max - b.Len(); room < len(p) {
		b.cut = true
		b.Buffer.Write(p[:max(room, 0)])
		return len(p), nil
	}
	return b.Buffer.Write(p)
}
